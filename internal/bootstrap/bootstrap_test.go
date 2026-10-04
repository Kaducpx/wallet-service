package bootstrap

import (
	"testing"
	"time"

	"go.uber.org/fx"

	"wallet-service/internal/platform/config"
)

// TestGraphIsValid verifica a composição do Fx sem iniciar nada: todos os
// construtores têm suas dependências satisfeitas e não há ciclos. A
// verificação de início/encerramento real fica nos testes de integração.
func TestGraphIsValid(t *testing.T) {
	cfg := config.Config{
		InstanceID: "test", LogLevel: "error", HTTPAddr: "127.0.0.1:0", HTTPRequestTimeout: time.Second, ShutdownTimeout: time.Second,
		DatabaseURL: "postgres://u:p@localhost:1/db", DBMaxConns: 1, DBLockTimeout: time.Second,
		OIDCIssuer: "http://issuer", OIDCJWKSURL: "http://issuer/certs", OIDCAudience: "wallet-service",
		AWSRegion: "us-east-1", SQSQueueName: "q.fifo", SQSDLQName: "dlq.fifo", SQSEventsQueueName: "ev.fifo",
		SQSConsumerName: "c", SQSConsumerWorkers: 1, SQSDLQMonitorInterval: time.Second, SQSWaitSeconds: 1, SQSMaxMessages: 1, SQSVisibilityTimeout: 5,
		OutboxBatchSize: 1, OutboxLease: time.Second, OutboxRetryBase: time.Second, OutboxRetryMax: time.Second,
		PendingTTL: time.Minute, PendingMaxAttempts: 1, PendingBackoffBase: time.Second, PendingBackoffMax: time.Second,
		PendingLease: time.Second,
	}
	if err := fx.ValidateApp(Options(ConfigValue(cfg))); err != nil {
		t.Fatal(err)
	}
}

// Uma configuração vazia impede a aplicação de iniciar.
func TestInvalidConfigIsRejected(t *testing.T) {
	err := fx.ValidateApp(Options(ConfigValue(config.Config{})))
	if err == nil {
		// ValidateApp não executa construtores; a validação ocorre no New.
		app := fx.New(Options(ConfigValue(config.Config{})), fx.NopLogger)
		if app.Err() == nil {
			t.Fatal("empty configuration must fail during initialization")
		}
	}
}
