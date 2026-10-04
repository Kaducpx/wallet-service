package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"wallet-service/internal/adapters/postgres"
	sqsadapter "wallet-service/internal/adapters/sqs"
	"wallet-service/internal/auth"
	"wallet-service/internal/platform/config"
	"wallet-service/internal/platform/metrics"
)

var Module = fx.Module("http",
	fx.Provide(NewHandlers, NewServer),
)

type Server struct {
	srv      *http.Server
	addr     atomic.Value // endereço real após o Listen (útil com :0 nos testes)
	draining atomic.Bool  // true durante o shutdown: readiness passa a falhar
}

// ServerParams: dependências do servidor (o Fx preenche).
type ServerParams struct {
	fx.In
	Lifecycle fx.Lifecycle
	Config    config.Config
	Log       *slog.Logger
	Handlers  *Handlers
	Verifier  *auth.Verifier
	Metrics   *metrics.Metrics
	Store     *postgres.Store
	SQS       *sqs.Client
	Queues    *sqsadapter.Queues
}

// NewServer monta rotas e middlewares e registra start/stop no Fx.
func NewServer(p ServerParams) *Server {
	s := &Server{}
	mux := http.NewServeMux()
	health := healthChecks{store: p.Store, sqs: p.SQS, queues: p.Queues, draining: &s.draining}
	authn := authenticator{verifier: p.Verifier, metrics: p.Metrics, log: p.Log}
	registerRoutes(mux, p.Handlers, authn, health, p.Metrics)

	// De fora para dentro: correlação, métricas/log, prazo, rota.
	handler := withCorrelation(withObservability(withTimeout(mux, p.Config.HTTPRequestTimeout), p.Metrics, p.Log))

	s.srv = &http.Server{
		Addr:              p.Config.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      p.Config.HTTPRequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	p.Lifecycle.Append(fx.Hook{
		// Listen antes de subir: porta ocupada falha a inicialização na hora.
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", p.Config.HTTPAddr)
			if err != nil {
				return err
			}
			s.addr.Store(ln.Addr().String())
			go func() {
				if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					p.Log.Error("http server failed", slog.String("error", err.Error()))
				}
			}()
			p.Log.Info("http server listening", slog.String("addr", ln.Addr().String()))
			return nil
		},
		// No shutdown: readiness passa a falhar, conexões novas são recusadas e as
		// requisições em andamento terminam dentro do prazo.
		OnStop: func(ctx context.Context) error {
			s.draining.Store(true)
			err := s.srv.Shutdown(ctx)
			p.Log.Info("http server stopped")
			return err
		},
	})
	return s
}

// Addr é o endereço real (útil quando a porta é :0 nos testes).
func (s *Server) Addr() string {
	v, _ := s.addr.Load().(string)
	return v
}
