package sqs

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"wallet-service/internal/platform/config"
	"wallet-service/internal/platform/metrics"
	"wallet-service/internal/worker"
)

// DLQMonitor publica o tamanho da DLQ na métrica wallet_sqs_dlq_depth.
//
// O contador wallet_sqs_dlq_total só vê o que o consumidor manda para a DLQ.
// Mensagens que esgotam as tentativas são movidas pelo próprio SQS; o
// tamanho da fila pega os dois casos.
type DLQMonitor struct {
	client  *sqs.Client
	queues  *Queues
	metrics *metrics.Metrics
	log     *slog.Logger
	loop    *worker.Loop
}

func NewDLQMonitor(client *sqs.Client, queues *Queues, m *metrics.Metrics, log *slog.Logger, cfg config.Config) *DLQMonitor {
	d := &DLQMonitor{client: client, queues: queues, metrics: m, log: log}
	d.loop = worker.NewLoop("dlq-monitor", cfg.SQSDLQMonitorInterval, log, d.tick)
	return d
}

func (d *DLQMonitor) Start()                         { d.loop.Start() }
func (d *DLQMonitor) Stop(ctx context.Context) error { return d.loop.Stop(ctx) }
func (d *DLQMonitor) Done() <-chan struct{}          { return d.loop.Done() }

// tick lê o tamanho da DLQ (visíveis + em processamento).
func (d *DLQMonitor) tick(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := d.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(d.queues.DLQ),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		d.log.Warn("dlq depth check failed", slog.String("error", err.Error()))
		return false
	}
	visible, _ := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)])
	inFlight, _ := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessagesNotVisible)])
	d.metrics.SQSDLQDepth.Set(float64(visible + inFlight))
	return false
}
