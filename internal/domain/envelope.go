package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Envelope é o formato publicado. É montado uma vez, no commit, e gravado na
// outbox: republicar envia os mesmos bytes e o mesmo eventId.
type Envelope struct {
	EventID       uuid.UUID   `json:"eventId"`               // id único e estável; consumidores deduplicam por ele
	EventType     string      `json:"eventType"`             // ex.: "WalletBalanceChanged"
	AggregateID   uuid.UUID   `json:"aggregateId"`           // walletId: agrupa e ordena eventos da mesma carteira
	CorrelationID string      `json:"correlationId"`         // liga o evento à requisição/mensagem de origem
	CausationID   string      `json:"causationId,omitempty"` // o que causou diretamente (messageId ou transação)
	OccurredAt    time.Time   `json:"occurredAt"`            // UTC, RFC 3339
	Version       int         `json:"version"`               // versão do formato de Data
	Data          DomainEvent `json:"data"`                  // conteúdo tipado do evento
}

// NewEnvelope embrulha o evento. occurredAt vai em UTC, com precisão de
// microssegundos (a mesma do Postgres).
func NewEnvelope(eventID uuid.UUID, ev DomainEvent, correlationID, causationID string, occurredAt time.Time) (Envelope, error) {
	if eventID == uuid.Nil || ev == nil || correlationID == "" || occurredAt.IsZero() {
		return Envelope{}, fmt.Errorf("%w: eventId, data, correlationId and occurredAt are required", ErrInvalidEvent)
	}
	return Envelope{
		EventID: eventID, EventType: ev.EventType(), AggregateID: ev.AggregateID(),
		CorrelationID: correlationID, CausationID: causationID,
		OccurredAt: occurredAt.UTC().Truncate(time.Microsecond), Version: ev.EventVersion(), Data: ev,
	}, nil
}
