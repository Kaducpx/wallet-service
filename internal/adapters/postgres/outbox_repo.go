package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-service/internal/app"
)

// OutboxRepo é usado pelo publicador. Cada método é uma transação curta.
type OutboxRepo struct {
	pool *pgxpool.Pool
}

func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo { return &OutboxRepo{pool: pool} }

// Claim reserva um lote de eventos por um tempo (lease).
//
// SKIP LOCKED faz cada publicador pegar eventos diferentes sem esperar os
// outros. Se o publicador morrer, o lease vence e outro assume. attempts
// sobe já aqui, então uma queda no meio também conta como tentativa.
func (r *OutboxRepo) Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]app.OutboxMessage, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE outbox_events o
		   SET locked_by = $1,
		       locked_until = now() + make_interval(secs => $3),
		       attempts = o.attempts + 1
		 WHERE o.id IN (
		       SELECT id FROM outbox_events
		        WHERE published_at IS NULL
		          AND next_attempt_at <= now()
		          AND (locked_until IS NULL OR locked_until < now())
		        ORDER BY occurred_at
		        LIMIT $2
		        FOR UPDATE SKIP LOCKED)
		RETURNING o.id, o.aggregate_type, o.aggregate_id, o.event_type, o.event_version,
		          o.payload::text, o.correlation_id, o.occurred_at, o.attempts`,
		owner, limit, lease.Seconds())
	if err != nil {
		return nil, wrap("claim outbox", err)
	}
	defer rows.Close()
	var out []app.OutboxMessage
	for rows.Next() {
		var m app.OutboxMessage
		var payload string
		if err := rows.Scan(&m.ID, &m.AggregateType, &m.AggregateID, &m.EventType, &m.EventVersion,
			&payload, &m.CorrelationID, &m.OccurredAt, &m.Attempts); err != nil {
			return nil, wrap("scan outbox", err)
		}
		m.Payload = []byte(payload)
		out = append(out, m)
	}
	return out, wrap("iterate outbox", rows.Err())
}

// MarkPublished só confirma se o lease ainda é deste publicador. Se venceu e
// outro assumiu, o evento pode sair duas vezes, mas com o mesmo eventId.
func (r *OutboxRepo) MarkPublished(ctx context.Context, id uuid.UUID, owner string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = now(), locked_by = NULL, locked_until = NULL, last_error = NULL
		 WHERE id = $1 AND locked_by = $2 AND published_at IS NULL`, id, owner)
	return wrap("mark published", err)
}

// MarkRetry agenda a próxima tentativa, libera o lease e guarda o erro.
func (r *OutboxRepo) MarkRetry(ctx context.Context, id uuid.UUID, owner string, nextAttemptAt time.Time, lastErr string) error {
	if len(lastErr) > 1000 {
		lastErr = lastErr[:1000]
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE outbox_events
		   SET next_attempt_at = $3, locked_by = NULL, locked_until = NULL, last_error = $4
		 WHERE id = $1 AND locked_by = $2 AND published_at IS NULL`, id, owner, nextAttemptAt, lastErr)
	return wrap("mark retry", err)
}

// Stats: quantos eventos pendentes e a idade do mais antigo.
func (r *OutboxRepo) Stats(ctx context.Context) (app.OutboxStats, error) {
	var s app.OutboxStats
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(EXTRACT(EPOCH FROM now() - MIN(occurred_at)), 0)::float8
		  FROM outbox_events WHERE published_at IS NULL`).Scan(&s.Pending, &s.OldestAgeSec)
	return s, wrap("outbox stats", err)
}

// InsertOutbox grava os eventos na mesma transação do negócio. Se o commit
// falhar, os eventos somem junto; nada é publicado antes do commit.
func (r *txRepo) InsertOutbox(ctx context.Context, msgs []app.OutboxMessage) error {
	for _, m := range msgs {
		_, err := r.tx.Exec(ctx, `
			INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, event_version,
			                           payload, correlation_id, occurred_at, next_attempt_at)
			VALUES ($1,$2,$3,$4,$5,$6::json,$7,$8,$8)`,
			m.ID, m.AggregateType, m.AggregateID, m.EventType, m.EventVersion,
			string(m.Payload), m.CorrelationID, m.OccurredAt)
		if err != nil {
			return wrap("insert outbox", err)
		}
	}
	return nil
}
