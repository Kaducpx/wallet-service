//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"go.uber.org/fx"

	"wallet-service/internal/bootstrap"
	"wallet-service/internal/platform/config"
)

// Composição Fx real: inicia com todas as dependências, para na ordem certa
// e libera os recursos dos workers (goroutines terminadas, pool fechado,
// porta HTTP liberada).
func TestFxLifecycle(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))

	r := call(t, http.MethodGet, app.base+"/health/ready", "", nil, nil)
	if r.Status != http.StatusOK || r.str("checks", "postgres") != "UP" || r.str("checks", "sqs") != "UP" {
		t.Fatalf("ready: %d %s", r.Status, r.Raw)
	}
	for name, done := range map[string]<-chan struct{}{
		"relay": app.relay.Done(), "pending": app.pending.Done(), "consumer": app.consumer.Done(),
	} {
		select {
		case <-done:
			t.Fatalf("%s finished before stop", name)
		default:
		}
	}

	start := time.Now()
	app.stop(t)
	t.Logf("graceful stop took %s", time.Since(start))

	for name, done := range map[string]<-chan struct{}{
		"relay": app.relay.Done(), "pending": app.pending.Done(), "consumer": app.consumer.Done(),
	} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s goroutines still running after stop", name)
		}
	}
	if err := app.pool.Ping(context.Background()); err == nil {
		t.Fatal("postgres pool must be closed after stop")
	}
	if _, err := tryCall(http.MethodGet, app.base+"/health/live", "", nil, nil); err == nil {
		t.Fatal("http server must not accept connections after stop")
	}
}

// Uma configuração inválida impede a inicialização.
func TestFxRejectsInvalidConfig(t *testing.T) {
	cfg := testConfig(newQueues(t, 5, 5))
	cfg.DatabaseURL = ""
	if err := startAppErr(cfg); err == nil {
		t.Fatal("app must not start without DATABASE_URL")
	}
}

// startAppErr tenta montar a aplicação e devolve o erro de inicialização.
func startAppErr(cfg config.Config) error {
	return fx.New(bootstrap.Options(bootstrap.ConfigValue(cfg)), fx.NopLogger).Err()
}
