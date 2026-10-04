package sqs

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Ack, DLQ e visibilidade. Usam contexto próprio com prazo curto para o ack
// funcionar mesmo durante o shutdown.

// toDLQ manda erros permanentes direto para a DLQ e remove a original. Se o
// envio falhar, a redrive policy leva a mensagem para a DLQ depois.
func (c *Consumer) toDLQ(ctx context.Context, m types.Message, reason string, cause error) bool {
	group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = "invalid"
	}
	_, err := c.client.SendMessage(context.WithoutCancel(ctx), &sqs.SendMessageInput{
		QueueUrl:               aws.String(c.queues.DLQ),
		MessageBody:            m.Body,
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: m.MessageId,
		MessageAttributes: map[string]types.MessageAttributeValue{
			"failureReason": {DataType: aws.String("String"), StringValue: aws.String(reason)},
			"failureDetail": {DataType: aws.String("String"), StringValue: aws.String(truncate(cause.Error(), 512))},
		},
	})
	if err != nil {
		c.log.Error("failed to send message to DLQ", slog.String("error", err.Error()))
		return false
	}
	c.metrics.SQSDLQ.WithLabelValues(reason).Inc()
	c.metrics.SQSMessages.WithLabelValues("dlq").Inc()
	c.delete(m)
	return true
}

// delete é o ack. Só acontece depois do commit.
func (c *Consumer) delete(m types.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(c.queues.Requests), ReceiptHandle: m.ReceiptHandle,
	}); err != nil {
		// Sem ack a mensagem volta, e a inbox evita processar de novo.
		c.log.Warn("delete message failed", slog.String("error", err.Error()))
	}
}

// changeVisibility esconde a mensagem por alguns segundos (backoff); com 0
// devolve na hora.
func (c *Consumer) changeVisibility(m types.Message, seconds int32) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.queues.Requests), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: seconds,
	})
}

// release devolve as mensagens para a fila na hora.
func (c *Consumer) release(msgs []types.Message) {
	for _, m := range msgs {
		c.changeVisibility(m, 0)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
