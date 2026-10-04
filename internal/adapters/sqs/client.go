package sqs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"wallet-service/internal/platform/config"
)

// Queues guarda as URLs resolvidas no início da aplicação.
type Queues struct {
	Requests string // wager-transactions.fifo
	DLQ      string // wager-transactions-dlq.fifo
	Events   string // wallet-events.fifo (destino da outbox)
}

// NewClient usa as credenciais padrão da AWS (variáveis de ambiente, perfil).
// Com AWS_ENDPOINT_URL, aponta para o LocalStack.
func NewClient(cfg config.Config) (*sqs.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.AWSEndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.AWSEndpointURL)
		}
	}), nil
}

// NewQueues busca as URLs das filas no start. Fila faltando = não sobe.
func NewQueues(lc fx.Lifecycle, client *sqs.Client, cfg config.Config, log *slog.Logger) *Queues {
	q := &Queues{}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		resolve := func(name string) (string, error) {
			var lastErr error
			for {
				out, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
				if err == nil {
					return aws.ToString(out.QueueUrl), nil
				}
				lastErr = err
				log.Warn("sqs queue not available yet", slog.String("queue", name), slog.String("error", err.Error()))
				select {
				case <-ctx.Done():
					return "", fmt.Errorf("resolve queue %s: %w", name, lastErr)
				case <-time.After(time.Second):
				}
			}
		}
		var err error
		if q.Requests, err = resolve(cfg.SQSQueueName); err != nil {
			return err
		}
		if q.DLQ, err = resolve(cfg.SQSDLQName); err != nil {
			return err
		}
		if q.Events, err = resolve(cfg.SQSEventsQueueName); err != nil {
			return err
		}
		log.Info("sqs queues resolved")
		return nil
	}})
	return q
}

func Ping(ctx context.Context, client *sqs.Client, q *Queues) error {
	_, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(q.Requests), AttributeNames: nil,
	})
	return err
}
