package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/auth"
	"wallet-service/internal/platform/metrics"
)

type authenticator struct {
	verifier *auth.Verifier
	metrics  *metrics.Metrics
	log      *slog.Logger
}

// require valida o token e a permissão da rota: 401 se o token for ausente,
// inválido ou expirado; 403 se faltar permissão. Se passar, o Principal vai
// para o contexto (o handler lê com auth.FromContext).
func (a authenticator) require(allowed func(auth.Principal) bool) func(http.HandlerFunc) http.Handler {
	return func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := a.verifier.Authenticate(r.Context(), r.Header.Get("Authorization"))
			if err != nil {
				reason := "invalid_token"
				if errors.Is(err, auth.ErrMissingToken) {
					reason = "missing_token"
				}
				a.metrics.AuthFailures.WithLabelValues(reason).Inc()
				// O motivo detalhado vai para o log (nunca o token); o cliente só vê 401.
				a.log.InfoContext(r.Context(), "authentication failed",
					slog.String("reason", reason), slog.String("error", err.Error()),
					slog.String("correlationId", correlationID(r)))
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				writeError(w, r, a.log, errUnauthenticated)
				return
			}
			if !allowed(p) {
				a.metrics.AuthFailures.WithLabelValues("forbidden").Inc()
				a.log.InfoContext(r.Context(), "authorization denied",
					slog.String("clientId", p.ClientID), slog.String("correlationId", correlationID(r)))
				writeError(w, r, a.log, errForbidden)
				return
			}
			next(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

type correlationKey struct{}

// withCorrelation usa o X-Correlation-Id recebido (ou gera um). Esse id
// aparece nos logs e nos eventos, então dá para seguir uma requisição de
// ponta a ponta.
func withCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-Id")
		if id == "" || len(id) > 128 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Correlation-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey{}, id)))
	})
}

func correlationID(r *http.Request) string {
	id, _ := r.Context().Value(correlationKey{}).(string)
	return id
}

// statusRecorder guarda o status escrito pelo handler para o log e as métricas.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// withObservability registra métrica e log de cada requisição.
func withObservability(next http.Handler, m *metrics.Metrics, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		elapsed := time.Since(start)
		m.HTTPRequests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		m.HTTPDuration.WithLabelValues(route).Observe(elapsed.Seconds())
		if route == "GET /health/live" || route == "GET /health/ready" || route == "GET /metrics" {
			return
		}
		log.InfoContext(r.Context(), "http request",
			slog.String("route", route), slog.Int("status", rec.status),
			slog.Duration("duration", elapsed), slog.String("correlationId", correlationID(r)))
	})
}

// withTimeout dá um prazo para cada requisição. Se o banco travar, o prazo
// vence, a transação é desfeita e o cliente recebe 503 em vez de ficar
// esperando.
func withTimeout(next http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
