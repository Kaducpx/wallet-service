package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"wallet-service/internal/app"
	"wallet-service/internal/platform/config"
)

// Module registra pool, store e repositórios. As funções no fim dizem ao Fx
// que o *Store é o app.TxRunner, e assim por diante.
var Module = fx.Module("postgres",
	fx.Provide(
		NewPool,
		NewStore,
		NewOutboxRepo,
		NewPendingRepo,
		func(s *Store) app.TxRunner { return s },
		func(r *OutboxRepo) app.OutboxStore { return r },
		func(r *PendingRepo) app.PendingStore { return r },
	),
)

// NewPool cria o pool. No start tenta conectar até o banco responder; no
// stop fecha o pool. O Fx fecha o pool por último, depois de tudo que o usa.
func NewPool(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	// Máximo de conexões por instância.
	pcfg.MaxConns = cfg.DBMaxConns
	pcfg.MinConns = 1
	pcfg.MaxConnIdleTime = 5 * time.Minute
	pcfg.HealthCheckPeriod = 15 * time.Second
	// Nome da instância no pg_stat_activity (ajuda a investigar locks).
	pcfg.ConnConfig.RuntimeParams["application_name"] = "wallet-service:" + cfg.InstanceID
	// Ninguém espera lock para sempre: passou do prazo, erro 55P03 e a
	// aplicação refaz.
	pcfg.ConnConfig.RuntimeParams["lock_timeout"] = fmt.Sprintf("%d", cfg.DBLockTimeout.Milliseconds())
	// Transação esquecida aberta é encerrada pelo banco em 60s.
	pcfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"

	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var lastErr error
			for {
				if lastErr = pool.Ping(ctx); lastErr == nil {
					log.Info("postgres connected")
					return nil
				}
				log.Warn("postgres not ready yet", slog.String("error", lastErr.Error()))
				select {
				case <-ctx.Done():
					return fmt.Errorf("postgres unavailable: %w", lastErr)
				case <-time.After(time.Second):
				}
			}
		},
		OnStop: func(context.Context) error {
			pool.Close()
			log.Info("postgres pool closed")
			return nil
		},
	})
	return pool, nil
}
