package sqs

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"wallet-service/internal/app"
)

// EventPublisher manda os eventos da outbox para wallet-events.fifo.
//
// MessageGroupId = walletId: eventos da mesma carteira saem em ordem.
// MessageDeduplicationId = eventId: o SQS descarta republicação em até
// 5 minutos; depois disso, o consumidor deduplica pelo eventId.
type EventPublisher struct {
	client *sqs.Client
	queues *Queues
}

func NewEventPublisher(client *sqs.Client, queues *Queues) *EventPublisher {
	return &EventPublisher{client: client, queues: queues}
}

func (p *EventPublisher) Publish(ctx context.Context, m app.OutboxMessage) error {
	// Prazo para não prender o publicador se o SQS travar.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queues.Events),
		MessageBody:            aws.String(string(m.Payload)),
		MessageGroupId:         aws.String(m.AggregateID.String()),
		MessageDeduplicationId: aws.String(m.ID.String()),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType":     {DataType: aws.String("String"), StringValue: aws.String(m.EventType)},
			"eventId":       {DataType: aws.String("String"), StringValue: aws.String(m.ID.String())},
			"correlationId": {DataType: aws.String("String"), StringValue: aws.String(m.CorrelationID)},
		},
	})
	return err
}
