package httpapi

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"wallet-service/internal/adapters/postgres"
	sqsadapter "wallet-service/internal/adapters/sqs"
)

type healthChecks struct {
	store    *postgres.Store
	sqs      *sqs.Client
	queues   *sqsadapter.Queues
	draining *atomic.Bool
}

// live: o processo está de pé. Não checa dependências.
func (h healthChecks) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
}

// ready checa Postgres e SQS. Durante o shutdown responde 503, para o
// balanceador parar de mandar tráfego.
func (h healthChecks) ready(w http.ResponseWriter, r *http.Request) {
	// As duas checagens rodam ao mesmo tempo, cada uma com seu prazo. Assim
	// um banco travado não faz o SQS parecer fora do ar (e vice-versa).
	var pgErr, sqsErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		pgErr = h.store.Ping(ctx)
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		sqsErr = sqsadapter.Ping(ctx, h.sqs, h.queues)
	}()
	wg.Wait()

	checks := map[string]string{"postgres": "UP", "sqs": "UP"}
	status := http.StatusOK
	if h.draining.Load() {
		checks["shutdown"] = "DRAINING"
		status = http.StatusServiceUnavailable
	}
	if pgErr != nil {
		checks["postgres"], status = "DOWN", http.StatusServiceUnavailable
	}
	if sqsErr != nil {
		checks["sqs"], status = "DOWN", http.StatusServiceUnavailable
	}
	overall := "UP"
	if status != http.StatusOK {
		overall = "DOWN"
	}
	writeJSON(w, status, map[string]any{"status": overall, "checks": checks})
}
