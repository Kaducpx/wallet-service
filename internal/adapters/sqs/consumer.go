package sqs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"wallet-service/internal/app"
	"wallet-service/internal/platform/config"
	"wallet-service/internal/platform/metrics"
)

// Consumer lê wager-transactions.fifo. Para cada mensagem: valida, processa
// (inbox + transação + saldo + ledger + outbox no mesmo commit) e só depois
// remove da fila.
//
// Se cair entre o commit e a remoção, o SQS reentrega; a inbox reconhece o
// messageId e a mensagem é só removida, sem efeito financeiro.
type Consumer struct {
	client  *sqs.Client
	queues  *Queues
	wager   *app.WagerService
	faults  app.FaultInjector
	metrics *metrics.Metrics
	log     *slog.Logger
	cfg     config.Config

	// Desligamento em duas fases: primeiro para de buscar (fetchCtx); só se o
	// prazo acabar interrompe o que está em andamento (workCtx).
	fetchCtx   context.Context // cancelado no Stop: para de buscar mensagens
	stopFetch  context.CancelFunc
	workCtx    context.Context // cancelado só quando o prazo do Stop vence
	cancelWork context.CancelFunc
	wg         sync.WaitGroup // conta os loops de recebimento em execução
	done       chan struct{}  // fechado quando todos os loops terminaram
}

// O consumidor só começa a trabalhar no Start.
func NewConsumer(client *sqs.Client, queues *Queues, wager *app.WagerService, faults app.FaultInjector,
	m *metrics.Metrics, log *slog.Logger, cfg config.Config) *Consumer {
	return &Consumer{
		client: client, queues: queues, wager: wager, faults: faults, metrics: m, cfg: cfg,
		log: log.With(slog.String("worker", "sqs-consumer")), done: make(chan struct{}),
	}
}

// Start sobe SQSConsumerWorkers laços de recebimento. O FIFO não entrega
// mensagens da mesma carteira para dois laços ao mesmo tempo.
func (c *Consumer) Start() {
	c.fetchCtx, c.stopFetch = context.WithCancel(context.Background())
	c.workCtx, c.cancelWork = context.WithCancel(context.Background())
	for i := 0; i < c.cfg.SQSConsumerWorkers; i++ {
		c.wg.Add(1)
		go c.receiveLoop()
	}
	go func() { c.wg.Wait(); close(c.done) }()
	c.log.Info("sqs consumer started", slog.Int("workers", c.cfg.SQSConsumerWorkers))
}

// Stop (SIGTERM): para de buscar na hora, deixa o que está em andamento
// terminar dentro do prazo e, se o prazo acabar, cancela e devolve as
// mensagens para a fila.
func (c *Consumer) Stop(ctx context.Context) error {
	if c.stopFetch == nil {
		return nil
	}
	c.stopFetch()
	select {
	case <-c.done:
		c.cancelWork()
		c.log.Info("sqs consumer stopped")
		return nil
	case <-ctx.Done():
		c.log.Warn("sqs consumer stop deadline reached, releasing in-flight messages")
		c.cancelWork()
		<-c.done
		return ctx.Err()
	}
}

func (c *Consumer) Done() <-chan struct{} { return c.done }

// receiveLoop busca lotes até o Stop. Usa long polling: com a fila vazia, o
// SQS segura a chamada alguns segundos em vez de responder vazio na hora.
func (c *Consumer) receiveLoop() {
	defer c.wg.Done()
	for c.fetchCtx.Err() == nil {
		// Prazo por chamada, para não ficar preso se o SQS travar.
		ctx, cancel := context.WithTimeout(c.fetchCtx, time.Duration(c.cfg.SQSWaitSeconds)*time.Second+10*time.Second)
		out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.queues.Requests),
			MaxNumberOfMessages: c.cfg.SQSMaxMessages,
			WaitTimeSeconds:     c.cfg.SQSWaitSeconds,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameApproximateReceiveCount,
				types.MessageSystemAttributeNameMessageGroupId,
			},
			MessageAttributeNames: []string{"All"},
		})
		cancel()
		if err != nil {
			if c.fetchCtx.Err() != nil {
				return
			}
			// SQS fora do ar: espera um pouco e tenta de novo.
			c.log.Warn("sqs receive failed", slog.String("error", err.Error()))
			select {
			case <-c.fetchCtx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		c.processBatch(out.Messages)
	}
}

// processBatch mantém a ordem por carteira: se uma mensagem falhar, as
// seguintes da mesma carteira voltam para a fila sem processar (para não
// passarem na frente). As de outras carteiras seguem normalmente.
func (c *Consumer) processBatch(msgs []types.Message) {
	blocked := map[string]bool{} // grupos com uma falha neste lote
	for i, m := range msgs {
		if c.workCtx.Err() != nil {
			c.release(msgs[i:])
			return
		}
		group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		if blocked[group] {
			c.release([]types.Message{m})
			continue
		}
		if !c.handle(m) {
			blocked[group] = true
		}
	}
}
