package app

import (
	"encoding/json"
	"fmt"
	"time"

	"wallet-service/internal/domain"
)

// buildOutbox transforma os eventos em envelopes JSON prontos para publicar.
// O JSON fica gravado, então uma republicação manda os mesmos bytes e o
// mesmo eventId, e o consumidor consegue descartar a duplicata.
func buildOutbox(events []domain.DomainEvent, correlationID, causationID string, now time.Time) ([]OutboxMessage, error) {
	msgs := make([]OutboxMessage, 0, len(events))
	for _, ev := range events {
		env, err := domain.NewEnvelope(newID(), ev, correlationID, causationID, now)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(env)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", env.EventType, err)
		}
		msgs = append(msgs, OutboxMessage{
			ID: env.EventID, AggregateType: "wallet", AggregateID: env.AggregateID,
			EventType: env.EventType, EventVersion: env.Version, Payload: payload,
			CorrelationID: correlationID, OccurredAt: env.OccurredAt,
		})
	}
	return msgs, nil
}
