//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

// SQS: filas exclusivas por teste (com redrive para DLQ) e envio/leitura de mensagens.

var (
	sqsOnce   sync.Once
	sqsClient *sqs.Client
)

// sqsc devolve um cliente SQS único (criado uma vez) apontando para o LocalStack.
func sqsc(t testing.TB) *sqs.Client {
	sqsOnce.Do(func() {
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"))
		if err != nil {
			panic(err)
		}
		sqsClient = sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(awsEndpoint) })
	})
	return sqsClient
}

type queueSet struct {
	Name, DLQName, EventsName string
	URL, DLQURL, EventsURL    string
}

// newQueues cria fila de entrada (com redrive), DLQ e fila de eventos.
func newQueues(t testing.TB, maxReceive, visibility int) queueSet {
	t.Helper()
	ctx := context.Background()
	c := sqsc(t)
	suffix := strings.ReplaceAll(uuid.NewString()[:13], "-", "")
	q := queueSet{
		Name: "it-req-" + suffix + ".fifo", DLQName: "it-dlq-" + suffix + ".fifo", EventsName: "it-ev-" + suffix + ".fifo",
	}
	mk := func(name string, attrs map[string]string) string {
		attrs["FifoQueue"] = "true"
		out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attrs})
		if err != nil {
			t.Fatalf("create queue %s: %v", name, err)
		}
		return aws.ToString(out.QueueUrl)
	}
	q.DLQURL = mk(q.DLQName, map[string]string{})
	arn, err := c.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(q.DLQURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatal(err)
	}
	q.URL = mk(q.Name, map[string]string{
		"VisibilityTimeout": strconv.Itoa(visibility),
		"RedrivePolicy": fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"%d"}`,
			arn.Attributes["QueueArn"], maxReceive),
	})
	q.EventsURL = mk(q.EventsName, map[string]string{})
	t.Cleanup(func() {
		for _, u := range []string{q.URL, q.DLQURL, q.EventsURL} {
			_, _ = c.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(u)})
		}
	})
	return q
}

// sendWagerMessage publica uma operação como um provedor faria via SQS.
// MessageGroupId = walletId; MessageDeduplicationId = dedupID.
func sendWagerMessage(t testing.TB, q queueSet, messageID string, data map[string]any, dedupID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339), "data": data,
	})
	sendRaw(t, q.URL, string(body), fmt.Sprint(data["walletId"]), dedupID)
}

// sendRaw envia um corpo qualquer (inclusive inválido) para a fila.
func sendRaw(t testing.TB, queueURL, body, group, dedup string) {
	t.Helper()
	_, err := sqsc(t).SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(body),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(dedup),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// drain lê mensagens da fila (removendo-as) até a fila ficar vazia por idle.
func drain(t testing.TB, queueURL string, idle time.Duration, until func([]types.Message) bool) []types.Message {
	t.Helper()
	var all []types.Message
	deadline := time.Now().Add(60 * time.Second)
	lastSeen := time.Now()
	for time.Now().Before(deadline) {
		out, err := sqsc(t).ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageAttributeNames: []string{"All"},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			all = append(all, m)
			_, _ = sqsc(t).DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
				QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle,
			})
		}
		if len(out.Messages) > 0 {
			lastSeen = time.Now()
		}
		if until != nil && until(all) {
			return all
		}
		if until == nil && time.Since(lastSeen) > idle {
			return all
		}
	}
	return all
}
