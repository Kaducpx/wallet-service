//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"wallet-service/internal/adapters/httpapi"
	sqsadapter "wallet-service/internal/adapters/sqs"
	"wallet-service/internal/bootstrap"
	"wallet-service/internal/platform/config"
	"wallet-service/internal/worker"
)

// Configuração de teste e instâncias da aplicação rodando no mesmo processo (Fx).

// testConfig usa intervalos curtos para que retries, leases e backoffs
// aconteçam em segundos.
func testConfig(q queueSet) config.Config {
	return config.Config{
		InstanceID: "it-" + uuid.NewString()[:8], LogLevel: envOr("IT_LOG_LEVEL", "error"),
		HTTPAddr: "127.0.0.1:0", HTTPRequestTimeout: 5 * time.Second, ShutdownTimeout: 10 * time.Second,

		DatabaseURL: appDBURL, DBMaxConns: 20, DBLockTimeout: 10 * time.Second,

		OIDCIssuer: oidcIssuer, OIDCJWKSURL: keycloakURL + "/realms/wallet/protocol/openid-connect/certs",
		OIDCAudience: "wallet-service",

		AWSRegion: "us-east-1", AWSEndpointURL: awsEndpoint,
		SQSQueueName: q.Name, SQSDLQName: q.DLQName, SQSEventsQueueName: q.EventsName,
		SQSConsumerEnabled: true, SQSConsumerName: "wager-transactions-consumer", SQSConsumerWorkers: 2,
		SQSWaitSeconds: 1, SQSMaxMessages: 10, SQSVisibilityTimeout: 5,
		SQSRetryBase: time.Second, SQSRetryMax: 4 * time.Second, SQSDLQMonitorInterval: 500 * time.Millisecond,

		OutboxEnabled: true, OutboxPollInterval: 100 * time.Millisecond, OutboxBatchSize: 50,
		OutboxLease: 3 * time.Second, OutboxRetryBase: 200 * time.Millisecond, OutboxRetryMax: 2 * time.Second,

		PendingEnabled: true, PendingPollInterval: 200 * time.Millisecond, PendingBatchSize: 50,
		PendingLease: 3 * time.Second, PendingTTL: time.Minute, PendingMaxAttempts: 100,
		PendingBackoffBase: 200 * time.Millisecond, PendingBackoffMax: time.Second,
	}
}

// instance é uma aplicação Fx completa rodando dentro do processo de teste.
type instance struct {
	app      *fx.App
	srv      *httpapi.Server
	relay    *worker.Relay
	pending  *worker.PendingResolver
	consumer *sqsadapter.Consumer
	pool     *pgxpool.Pool
	base     string
	stopped  bool
}

// startApp sobe uma aplicação Fx completa no próprio processo do teste, numa
// porta livre, e registra o desligamento no fim do teste.
func startApp(t testing.TB, cfg config.Config) *instance {
	t.Helper()
	i := &instance{}
	i.app = fx.New(
		bootstrap.Options(bootstrap.ConfigValue(cfg),
			fx.Populate(&i.srv, &i.relay, &i.pending, &i.consumer, &i.pool)),
		fx.StopTimeout(cfg.ShutdownTimeout),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := i.app.Start(ctx); err != nil {
		t.Fatalf("start app: %v", err)
	}
	i.base = "http://" + i.srv.Addr()
	t.Cleanup(func() { i.stop(t) })
	return i
}

func (i *instance) stop(t testing.TB) {
	if i.stopped {
		return
	}
	i.stopped = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := i.app.Stop(ctx); err != nil {
		t.Errorf("stop app: %v", err)
	}
}
