package worker

import (
	"context"
	"log/slog"

	"wallet-service/internal/app"
	"wallet-service/internal/domain"
	"wallet-service/internal/platform/config"
	"wallet-service/internal/platform/metrics"
)

// PendingResolver retoma as operações em PENDING_REFERENCE. Tentativas e
// prazos ficam no banco, então sobrevive a restart e qualquer instância pode
// assumir.
type PendingResolver struct {
	store   app.PendingStore
	wager   *app.WagerService
	metrics *metrics.Metrics
	log     *slog.Logger
	cfg     config.Config
	loop    *Loop
}

func NewPendingResolver(store app.PendingStore, wager *app.WagerService, m *metrics.Metrics, log *slog.Logger, cfg config.Config) *PendingResolver {
	p := &PendingResolver{store: store, wager: wager, metrics: m, log: log, cfg: cfg}
	p.loop = NewLoop("pending-references", cfg.PendingPollInterval, log, p.tick)
	return p
}

func (p *PendingResolver) Start()                         { p.loop.Start() }
func (p *PendingResolver) Stop(ctx context.Context) error { return p.loop.Stop(ctx) }
func (p *PendingResolver) Done() <-chan struct{}          { return p.loop.Done() }

// tick reserva as pendências vencidas e tenta resolver cada uma. Uma falha
// não trava as outras.
func (p *PendingResolver) tick(ctx context.Context) bool {
	due, err := p.store.ClaimDue(ctx, p.cfg.PendingBatchSize, p.cfg.PendingLease)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Warn("pending claim failed", slog.String("error", err.Error()))
		}
		return false
	}
	for _, ref := range due {
		if ctx.Err() != nil {
			// O que sobrar volta a vencer quando o lease expirar.
			return false
		}
		outcome, err := p.wager.Resume(ctx, ref)
		if err != nil {
			p.log.Warn("pending resume failed",
				slog.String("transactionId", ref.TransactionID.String()),
				slog.String("walletId", ref.WalletID.String()), slog.String("error", err.Error()))
			continue
		}
		if outcome == domain.OutcomeRetryScheduled {
			p.metrics.PendingRetries.Inc()
		}
	}
	return len(due) == p.cfg.PendingBatchSize
}
