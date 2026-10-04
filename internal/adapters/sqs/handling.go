package sqs

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"wallet-service/internal/app"
)

// Ponto de falha dos testes: commitou, mas ainda não removeu da fila.
const FaultConsumerAfterCommit = "consumer.after_commit"

// handle decide o que fazer com uma mensagem. Devolve false se ela ficou
// para nova tentativa.
//
//	processada, rejeitada, pendente ou repetida -> remove da fila
//	formato inválido                            -> DLQ
//	conflito de chave ou de messageId           -> DLQ
//	carteira inexistente                        -> DLQ
//	desligando no meio                          -> devolve para a fila
//	erro passageiro ou desconhecido             -> tenta de novo (backoff)
//
// Rejeição de negócio (ex.: sem saldo) não é erro: foi gravada e a
// mensagem sai da fila normalmente.
func (c *Consumer) handle(m types.Message) bool {
	// Tem que terminar antes do visibility timeout, senão o SQS reentrega no
	// meio. Com o banco travado, o prazo vence, desfaz e tenta depois.
	ctx, cancel := context.WithTimeout(c.workCtx, time.Duration(c.cfg.SQSVisibilityTimeout)*time.Second*2/3)
	defer cancel()
	// Quantas vezes o SQS já entregou esta mensagem (usado no backoff).
	receiveCount, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	receivedAt := time.Now()

	// Formato inválido não melhora tentando de novo: direto para a DLQ.
	env, cmd, err := c.decode(m, receivedAt)
	log := c.log.With(slog.String("sqsMessageId", aws.ToString(m.MessageId)), slog.String("messageId", env.MessageID))
	if err != nil {
		log.Warn("invalid message", slog.String("error", err.Error()))
		return c.toDLQ(ctx, m, "invalid_message", err)
	}
	log = log.With(slog.String("correlationId", cmd.CorrelationID), slog.String("providerId", cmd.Op.ProviderID),
		slog.String("walletId", cmd.Op.WalletID.String()))

	// Mesmo caso de uso do HTTP.
	res, err := c.wager.Submit(ctx, cmd)

	switch {
	case err == nil:
		// Commitou. Os testes podem derrubar o processo aqui, antes do ack.
		_ = c.faults.Hit(FaultConsumerAfterCommit)
		outcome := "processed"
		if res.InboxDuplicate {
			outcome = "duplicate"
		}
		c.metrics.SQSMessages.WithLabelValues(outcome).Inc()
		log.Info("message handled", slog.String("transactionId", res.Tx.ID().String()),
			slog.String("status", string(res.Tx.Status())), slog.Bool("duplicate", res.InboxDuplicate))
		c.delete(m)
		return true

	case errors.Is(err, app.ErrIdempotencyConflict), errors.Is(err, app.ErrExternalIDConflict),
		errors.Is(err, app.ErrInboxPayloadMismatch):
		log.Warn("conflicting message", slog.String("error", err.Error()))
		return c.toDLQ(ctx, m, "conflict", err)

	case errors.Is(err, app.ErrWalletNotFound):
		log.Warn("wallet not found", slog.String("error", err.Error()))
		return c.toDLQ(ctx, m, "wallet_not_found", err)

	case c.workCtx.Err() != nil:
		// Desligamento forçado no meio: a transação foi desfeita; devolve a
		// mensagem para a fila.
		log.Warn("processing interrupted by shutdown, releasing message")
		c.release([]types.Message{m})
		return false

	default:
		// Erro passageiro: esconde a mensagem pelo backoff e tenta de novo.
		// Passando do maxReceiveCount, o SQS manda para a DLQ sozinho.
		delay := c.backoff(receiveCount)
		c.metrics.SQSRetries.Inc()
		c.metrics.SQSMessages.WithLabelValues("retry").Inc()
		log.Warn("message processing failed, will retry",
			slog.Int("receiveCount", receiveCount), slog.Duration("retryIn", delay), slog.String("error", err.Error()))
		c.changeVisibility(m, int32(delay.Seconds()))
		return false
	}
}

// backoff: base, 2x, 4x... até o teto (2s, 4s, 8s... 60s). Dá tempo para a
// dependência voltar sem ser bombardeada.
func (c *Consumer) backoff(receiveCount int) time.Duration {
	d := c.cfg.SQSRetryBase
	for i := 1; i < receiveCount && d < c.cfg.SQSRetryMax; i++ {
		d *= 2
	}
	if d > c.cfg.SQSRetryMax {
		d = c.cfg.SQSRetryMax
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}
