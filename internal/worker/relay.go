package worker

import (
	"context"
	"log/slog"
	"time"

	"wallet-service/internal/app"
	"wallet-service/internal/domain"
	"wallet-service/internal/platform/config"
	"wallet-service/internal/platform/metrics"
)

// Ponto de falha usado nos testes.
const (
	FaultOutboxAfterPublish = "outbox.after_publish" // publicou, não confirmou
)

// Relay publica os eventos da outbox. Só enxerga eventos já commitados.
// Se cair entre publicar e marcar como publicado, outro publicador manda de
// novo, com o mesmo eventId (entrega "pelo menos uma vez").
type Relay struct {
	store     app.OutboxStore
	publisher app.EventPublisher
	faults    app.FaultInjector
	metrics   *metrics.Metrics
	log       *slog.Logger
	cfg       config.Config
	loop      *Loop
	lastStats time.Time
}

func NewRelay(store app.OutboxStore, publisher app.EventPublisher, faults app.FaultInjector,
	m *metrics.Metrics, log *slog.Logger, cfg config.Config) *Relay {
	r := &Relay{store: store, publisher: publisher, faults: faults, metrics: m, log: log, cfg: cfg}
	r.loop = NewLoop("outbox-relay", cfg.OutboxPollInterval, log, r.tick)
	return r
}

func (r *Relay) Start()                         { r.loop.Start() }
func (r *Relay) Stop(ctx context.Context) error { return r.loop.Stop(ctx) }
func (r *Relay) Done() <-chan struct{}          { return r.loop.Done() }

// backoff reaproveita o cálculo do domínio.
func (r *Relay) backoff(attempts int) time.Duration {
	p := domain.PendingPolicy{BaseBackoff: r.cfg.OutboxRetryBase, MaxBackoff: r.cfg.OutboxRetryMax}
	return p.Backoff(attempts - 1)
}

// tick: reserva um lote, publica cada evento e marca como publicado (ou
// agenda nova tentativa).
func (r *Relay) tick(ctx context.Context) bool {
	r.refreshStats(ctx)

	msgs, err := r.store.Claim(ctx, r.cfg.InstanceID, r.cfg.OutboxBatchSize, r.cfg.OutboxLease)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Warn("outbox claim failed", slog.String("error", err.Error()))
		}
		return false
	}
	for i, m := range msgs {
		// Desligando: devolve o resto do lote em vez de esperar o lease vencer.
		if r.loop.Stopping() || ctx.Err() != nil {
			r.release(msgs[i:])
			return false
		}
		// Falhou: agenda nova tentativa e segue para o próximo. Um evento com
		// problema não trava os outros.
		if err := r.publisher.Publish(ctx, m); err != nil {
			r.metrics.OutboxFailures.Inc()
			next := time.Now().Add(r.backoff(m.Attempts))
			r.log.Warn("outbox publish failed",
				slog.String("eventId", m.ID.String()), slog.String("eventType", m.EventType),
				slog.String("correlationId", m.CorrelationID), slog.Int("attempts", m.Attempts),
				slog.String("error", err.Error()))
			if err := r.store.MarkRetry(context.WithoutCancel(ctx), m.ID, r.cfg.InstanceID, next, err.Error()); err != nil {
				r.log.Warn("outbox mark retry failed", slog.String("error", err.Error()))
			}
			continue
		}
		// Publicou mas ainda não marcou. Os testes derrubam o processo aqui.
		_ = r.faults.Hit(FaultOutboxAfterPublish)
		if err := r.store.MarkPublished(context.WithoutCancel(ctx), m.ID, r.cfg.InstanceID); err != nil {
			// Vai ser republicado depois do lease, com o mesmo eventId.
			r.log.Warn("outbox mark published failed", slog.String("eventId", m.ID.String()), slog.String("error", err.Error()))
			continue
		}
		r.metrics.OutboxPublished.Inc()
		r.log.Debug("event published",
			slog.String("eventId", m.ID.String()), slog.String("eventType", m.EventType),
			slog.String("walletId", m.AggregateID.String()), slog.String("correlationId", m.CorrelationID))
	}
	// Lote cheio: deve ter mais, roda de novo já.
	return len(msgs) == r.cfg.OutboxBatchSize
}

// release devolve na hora os eventos reservados e não publicados.
func (r *Relay) release(msgs []app.OutboxMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, m := range msgs {
		_ = r.store.MarkRetry(ctx, m.ID, r.cfg.InstanceID, time.Now(), "released on shutdown")
	}
}

// refreshStats atualiza as métricas da outbox, no máximo a cada 5s.
func (r *Relay) refreshStats(ctx context.Context) {
	if time.Since(r.lastStats) < 5*time.Second {
		return
	}
	r.lastStats = time.Now()
	s, err := r.store.Stats(ctx)
	if err != nil {
		return
	}
	r.metrics.OutboxPending.Set(float64(s.Pending))
	r.metrics.OutboxLagSeconds.Set(s.OldestAgeSec)
}
