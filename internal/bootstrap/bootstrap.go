// Package bootstrap monta a aplicação com o Uber Fx.
//
// Cada pacote só declara construtores (fx.Provide). O Fx olha os parâmetros
// e descobre sozinho a ordem: o WagerService precisa de um TxRunner, que
// precisa do pool, que precisa da config. Nada é montado na mão.
//
// Quem precisa fazer algo ao subir ou ao desligar registra OnStart/OnStop no
// fx.Lifecycle. Subida: config, logs, banco, SQS, Keycloak, casos de uso,
// workers e, por último, o HTTP. O desligamento é o inverso: primeiro o HTTP
// para de aceitar requisições e o banco fecha por último.
package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"wallet-service/internal/adapters/httpapi"
	"wallet-service/internal/adapters/postgres"
	sqsadapter "wallet-service/internal/adapters/sqs"
	"wallet-service/internal/app"
	"wallet-service/internal/auth"
	"wallet-service/internal/domain"
	"wallet-service/internal/platform/config"
	"wallet-service/internal/platform/faults"
	"wallet-service/internal/platform/logging"
	"wallet-service/internal/platform/metrics"
	"wallet-service/internal/worker"
)

// ConfigFromEnv lê a configuração do ambiente. Os testes usam ConfigValue.
var ConfigFromEnv = fx.Module("config", fx.Provide(config.FromEnv))

// ConfigValue usa uma configuração já pronta (e valida).
func ConfigValue(cfg config.Config) fx.Option {
	return fx.Module("config", fx.Provide(func() (config.Config, error) { return cfg, cfg.Validate() }))
}

// AppModule registra os casos de uso.
var AppModule = fx.Module("app",
	fx.Provide(
		func(cfg config.Config) domain.PendingPolicy {
			return domain.PendingPolicy{
				TTL: cfg.PendingTTL, MaxAttempts: cfg.PendingMaxAttempts,
				BaseBackoff: cfg.PendingBackoffBase, MaxBackoff: cfg.PendingBackoffMax,
			}
		},
		func(cfg config.Config) (app.FaultInjector, error) {
			if cfg.FaultPoints == "" {
				return app.NopFaults{}, nil
			}
			return faults.Parse(cfg.FaultPoints)
		},
		app.NewWalletService,
		app.NewWagerService,
	),
)

// SQSModule registra cliente, filas, publicador e consumidor.
var SQSModule = fx.Module("sqs",
	fx.Provide(
		sqsadapter.NewClient,
		sqsadapter.NewQueues,
		sqsadapter.NewEventPublisher,
		func(p *sqsadapter.EventPublisher) app.EventPublisher { return p },
		sqsadapter.NewConsumer,
	),
)

// WorkersModule registra e liga os workers ao ciclo de vida.
var WorkersModule = fx.Module("workers",
	fx.Provide(worker.NewRelay, worker.NewPendingResolver, sqsadapter.NewDLQMonitor),
	fx.Invoke(registerWorkers),
)

// Workers junta os processos de fundo (os testes usam para ver se pararam).
type Workers struct {
	fx.In
	Relay    *worker.Relay
	Pending  *worker.PendingResolver
	Consumer *sqsadapter.Consumer
	DLQ      *sqsadapter.DLQMonitor
}

type startStopper interface {
	Start()
	Stop(ctx context.Context) error
}

// registerWorkers liga Start/Stop de cada worker ao Fx. Worker desligado
// por config não sobe.
func registerWorkers(lc fx.Lifecycle, cfg config.Config, w Workers, log *slog.Logger) {
	add := func(enabled bool, name string, s startStopper) {
		if !enabled {
			log.Info("worker disabled", slog.String("worker", name))
			return
		}
		lc.Append(fx.Hook{
			OnStart: func(context.Context) error { s.Start(); return nil },
			OnStop:  s.Stop,
		})
	}
	// Param na ordem inversa: o consumidor primeiro, o publicador por último
	// (para ainda publicar o que foi gerado durante o desligamento).
	add(cfg.OutboxEnabled, "outbox-relay", w.Relay)
	add(cfg.PendingEnabled, "pending-references", w.Pending)
	add(true, "dlq-monitor", w.DLQ)
	add(cfg.SQSConsumerEnabled, "sqs-consumer", w.Consumer)
}

// Options monta a aplicação inteira. configOption vem do main ou dos testes;
// extra serve para os testes pegarem componentes (fx.Populate).
func Options(configOption fx.Option, extra ...fx.Option) fx.Option {
	return fx.Options(
		configOption,
		logging.Module,
		metrics.Module,
		postgres.Module,
		SQSModule,
		auth.Module,
		AppModule,
		WorkersModule,
		httpapi.Module,
		// Garante que o HTTP seja o último a subir e o primeiro a parar.
		fx.Invoke(func(*httpapi.Server) {}),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger { return logging.FxLogger(log) }),
		fx.StartTimeout(60*time.Second),
		fx.Options(extra...),
	)
}

func StopTimeout(cfg config.Config) fx.Option { return fx.StopTimeout(cfg.ShutdownTimeout) }
