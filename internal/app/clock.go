package app

import (
	"time"

	"github.com/google/uuid"
)

// Now devolve a hora em UTC com precisão de microssegundos, igual ao
// Postgres. Assim a resposta original e o replay mostram o mesmo horário.
func Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// newID gera UUIDv7, que começa pelo timestamp: ordenado no tempo e melhor
// para índices do que o v4 aleatório.
func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }
