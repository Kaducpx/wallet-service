package app

import (
	"context"
	"errors"
	"time"
)

// isConflict: erros que se resolvem refazendo a transação (chave gravada
// por outra instância, deadlock, lock timeout, versão mudou). Refazer é
// seguro porque nada da tentativa anterior foi gravado.
func isConflict(err error) bool {
	return errors.Is(err, ErrUniqueViolation) || errors.Is(err, ErrRetryable) || errors.Is(err, ErrConcurrentUpdate)
}

// sleepCtx espera d, mas para na hora se o contexto for cancelado.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
