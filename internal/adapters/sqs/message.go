package sqs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"wallet-service/internal/app"
)

// Único tipo de mensagem aceito na fila de entrada.
const MessageTypeWagerRequested = "WagerTransactionRequested"

// requestEnvelope é o corpo da mensagem:
//
//	{"messageId":"msg-123","type":"WagerTransactionRequested",
//	 "occurredAt":"...","data":{...mesmo corpo do HTTP + idempotencyKey}}
//
// data fica cru (RawMessage) para ser lido depois, de forma estrita.
type requestEnvelope struct {
	MessageID  string          `json:"messageId"` // identidade da mensagem para a inbox
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurredAt"`
	Data       json.RawMessage `json:"data"`
}

// requestData é o corpo do HTTP mais a chave de idempotência.
type requestData struct {
	app.RawWagerRequest
	IdempotencyKey string `json:"idempotencyKey"`
}

// decode transforma a mensagem no mesmo comando que o HTTP gera, mais o
// registro de inbox. Erro aqui é formato inválido (vai para a DLQ).
func (c *Consumer) decode(m types.Message, receivedAt time.Time) (requestEnvelope, app.SubmitCommand, error) {
	var env requestEnvelope
	if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &env); err != nil {
		return env, app.SubmitCommand{}, fmt.Errorf("decode envelope: %w", err)
	}
	if env.MessageID == "" || len(env.MessageID) > 128 {
		return env, app.SubmitCommand{}, errors.New("messageId is required (max 128 chars)")
	}
	if env.Type != MessageTypeWagerRequested {
		return env, app.SubmitCommand{}, fmt.Errorf("unsupported message type %q", env.Type)
	}
	// Campo desconhecido é erro. As validações são as mesmas do HTTP.
	var data requestData
	dec := json.NewDecoder(bytes.NewReader(env.Data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&data); err != nil {
		return env, app.SubmitCommand{}, fmt.Errorf("decode data: %w", err)
	}
	if err := app.ValidateIdempotencyKey(data.IdempotencyKey); err != nil {
		return env, app.SubmitCommand{}, err
	}
	op, err := app.ParseWagerRequest(data.RawWagerRequest)
	if err != nil {
		return env, app.SubmitCommand{}, err
	}
	// Usa o correlationId do produtor, se veio; senão, o messageId.
	correlationID := env.MessageID
	if a, ok := m.MessageAttributes["correlationId"]; ok && aws.ToString(a.StringValue) != "" {
		correlationID = aws.ToString(a.StringValue)
	}
	return env, app.SubmitCommand{
		Op: op, IdempotencyKey: data.IdempotencyKey, CorrelationID: correlationID,
		CausationID: env.MessageID, Channel: app.ChannelSQS,
		Inbox: &app.InboxRecord{
			Consumer: c.cfg.SQSConsumerName, MessageID: env.MessageID,
			// Mesmo hash dos campos de negócio: detecta messageId repetido com
			// conteúdo diferente.
			PayloadHash: op.PayloadHash, ReceivedAt: receivedAt,
		},
	}, nil
}
