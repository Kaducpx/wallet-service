// Package metrics expõe as métricas do Prometheus. Cada instância tem seu
// próprio Registry, então os testes conseguem subir várias no mesmo processo.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.uber.org/fx"

	"wallet-service/internal/app"
)

var Module = fx.Module("metrics",
	fx.Provide(
		New,
		func(m *Metrics) app.Metrics { return m },
	),
)

// Metrics tem todas as métricas. Os campos exportados são usados direto
// pelos adaptadores; os privados, pelos métodos abaixo.
type Metrics struct {
	Registry *prometheus.Registry

	wagerResults     *prometheus.CounterVec
	duplicates       *prometheus.CounterVec
	conflicts        *prometheus.CounterVec
	processing       *prometheus.HistogramVec
	reconDivergences prometheus.Counter
	SQSRetries       prometheus.Counter
	SQSDLQ           *prometheus.CounterVec
	SQSDLQDepth      prometheus.Gauge
	SQSMessages      *prometheus.CounterVec
	OutboxPublished  prometheus.Counter
	OutboxFailures   prometheus.Counter
	OutboxPending    prometheus.Gauge
	OutboxLagSeconds prometheus.Gauge
	PendingRetries   prometheus.Counter
	HTTPRequests     *prometheus.CounterVec
	HTTPDuration     *prometheus.HistogramVec
	AuthFailures     *prometheus.CounterVec
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	f := func(c prometheus.Collector) { reg.MustRegister(c) }
	m := &Metrics{
		Registry: reg,
		wagerResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_wager_results_total", Help: "Resultado das operações por canal e status.",
		}, []string{"channel", "status", "replay"}),
		duplicates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_duplicates_total", Help: "Recebimentos duplicados detectados (idempotency_key ou inbox).",
		}, []string{"channel", "kind"}),
		conflicts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_concurrency_conflicts_total", Help: "Conflitos de concorrência resolvidos com retry.",
		}, []string{"operation"}),
		processing: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "wallet_processing_duration_seconds", Help: "Latência de processamento das operações.",
			Buckets: prometheus.DefBuckets,
		}, []string{"channel"}),
		reconDivergences: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_reconciliation_divergences_total", Help: "Reconciliações com saldo divergente do ledger.",
		}),
		SQSRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_sqs_retries_total", Help: "Mensagens devolvidas à fila para nova tentativa.",
		}),
		SQSDLQ: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_sqs_dlq_total", Help: "Mensagens enviadas explicitamente à DLQ.",
		}, []string{"reason"}),
		SQSDLQDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wallet_sqs_dlq_depth", Help: "Mensagens na DLQ (inclui as movidas pela redrive policy).",
		}),
		SQSMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_sqs_messages_total", Help: "Mensagens SQS por desfecho.",
		}, []string{"outcome"}),
		OutboxPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_outbox_published_total", Help: "Eventos publicados.",
		}),
		OutboxFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_outbox_publish_failures_total", Help: "Falhas de publicação (serão reprocessadas).",
		}),
		OutboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wallet_outbox_pending", Help: "Eventos ainda não publicados.",
		}),
		OutboxLagSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wallet_outbox_lag_seconds", Help: "Idade do evento pendente mais antigo.",
		}),
		PendingRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_pending_reference_retries_total", Help: "Tentativas de resolver referências pendentes.",
		}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_http_requests_total", Help: "Requisições HTTP por rota e status.",
		}, []string{"route", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "wallet_http_request_duration_seconds", Help: "Latência HTTP por rota.", Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		AuthFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_auth_failures_total", Help: "Falhas de autenticação/autorização.",
		}, []string{"reason"}),
	}
	for _, c := range []prometheus.Collector{
		m.wagerResults, m.duplicates, m.conflicts, m.processing, m.reconDivergences,
		m.SQSRetries, m.SQSDLQ, m.SQSDLQDepth, m.SQSMessages, m.OutboxPublished, m.OutboxFailures,
		m.OutboxPending, m.OutboxLagSeconds, m.PendingRetries, m.HTTPRequests, m.HTTPDuration, m.AuthFailures,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	} {
		f(c)
	}
	return m
}

// Métodos de app.Metrics.

func (m *Metrics) WagerResult(channel, status string, replay bool) {
	r := "false"
	if replay {
		r = "true"
	}
	m.wagerResults.WithLabelValues(channel, status, r).Inc()
}

func (m *Metrics) DuplicateDetected(channel, kind string) {
	m.duplicates.WithLabelValues(channel, kind).Inc()
}

func (m *Metrics) ConcurrencyConflict(operation string) {
	m.conflicts.WithLabelValues(operation).Inc()
}

func (m *Metrics) ProcessingDuration(channel string, d time.Duration) {
	m.processing.WithLabelValues(channel).Observe(d.Seconds())
}

func (m *Metrics) ReconciliationDivergence() { m.reconDivergences.Inc() }
