package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-service/internal/app"
)

// PendingRepo é usado pelo worker de pendências.
type PendingRepo struct {
	pool *pgxpool.Pool
}

func NewPendingRepo(pool *pgxpool.Pool) *PendingRepo { return &PendingRepo{pool: pool} }

// ClaimDue reserva as pendências vencidas empurrando next_retry_at para a
// frente. Se a instância cair, elas voltam a vencer e outra instância pega.
func (r *PendingRepo) ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]app.PendingRef, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE wager_transactions t
		   SET next_retry_at = now() + make_interval(secs => $2)
		 WHERE t.id IN (
		       SELECT id FROM wager_transactions
		        WHERE status = 'PENDING_REFERENCE' AND next_retry_at <= now()
		        ORDER BY next_retry_at
		        LIMIT $1
		        FOR UPDATE SKIP LOCKED)
		RETURNING t.id, t.wallet_id`, limit, lease.Seconds())
	if err != nil {
		return nil, wrap("claim pending", err)
	}
	defer rows.Close()
	var out []app.PendingRef
	for rows.Next() {
		var p app.PendingRef
		if err := rows.Scan(&p.TransactionID, &p.WalletID); err != nil {
			return nil, wrap("scan pending", err)
		}
		out = append(out, p)
	}
	return out, wrap("iterate pending", rows.Err())
}
