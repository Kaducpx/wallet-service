package httpapi

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"wallet-service/internal/auth"
	"wallet-service/internal/platform/metrics"
)

// registerRoutes é o mapa da API: rota, quem pode acessar e handler.
// Regras mais finas (provedor só vê o que é dele) ficam nos handlers.
func registerRoutes(mux *http.ServeMux, h *Handlers, a authenticator, health healthChecks, m *metrics.Metrics) {
	operator := a.require(func(p auth.Principal) bool { return p.IsOperator() })
	provider := a.require(func(p auth.Principal) bool { return p.IsProvider() })
	providerOrOperator := a.require(func(p auth.Principal) bool { return p.IsProvider() || p.IsOperator() })

	// Públicos.
	mux.HandleFunc("GET /health/live", health.live)
	mux.HandleFunc("GET /health/ready", health.ready)
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))

	// Serviço interno (wallet-operator).
	mux.Handle("POST /wallets", operator(h.openWallet))
	mux.Handle("GET /wallets/{walletId}", operator(h.getWallet))
	mux.Handle("GET /wallets/{walletId}/ledger", operator(h.getLedger))
	mux.Handle("POST /wallets/{walletId}/reconciliation", operator(h.reconcile))

	// Provedores (wager-provider), sempre em nome próprio.
	mux.Handle("POST /wagering/transactions", provider(h.submitWager))

	// Consultas: provedor vê só as dele; operador vê todas.
	mux.Handle("GET /wagering/transactions/{transactionId}", providerOrOperator(h.getTransaction))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", providerOrOperator(h.getProviderTransaction))
}
