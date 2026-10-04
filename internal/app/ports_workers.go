package app

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OutboxStore é usado pelo publicador.
type OutboxStore interface {
	// Claim reserva até limit eventos por um tempo (lease). Se o publicador
	// morrer, o lease vence e outro pega.
	Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]OutboxMessage, error)
	MarkPublished(ctx context.Context, id uuid.UUID, owner string) error
	MarkRetry(ctx context.Context, id uuid.UUID, owner string, nextAttemptAt time.Time, lastErr string) error
	Stats(ctx context.Context) (OutboxStats, error)
}

type OutboxStats struct {
	Pending      int64
	OldestAgeSec float64
}

// PendingStore é usado pelo worker de pendências.
type PendingStore interface {
	// ClaimDue reserva as pendências vencidas para esta instância. A decisão
	// mesmo sempre acontece com a carteira travada.
	ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]PendingRef, error)
}

type PendingRef struct {
	TransactionID uuid.UUID
	WalletID      uuid.UUID
}

// EventPublisher manda o evento para a fila de eventos.
type EventPublisher interface {
	Publish(ctx context.Context, msg OutboxMessage) error
}
