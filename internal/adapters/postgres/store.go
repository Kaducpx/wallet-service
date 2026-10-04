package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-service/internal/app"
)

// Store abre as transações. Tudo que o caso de uso faz dentro de InTx usa a
// mesma transação e é confirmado num único COMMIT.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// InTx usa READ COMMITTED. Quem organiza a fila por carteira é o
// FOR UPDATE, não o nível de isolamento; assim carteiras diferentes não
// abortam umas às outras.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) error {
	return s.run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn)
}

// ReadOnly usa REPEATABLE READ: todas as leituras veem o mesmo momento.
func (s *Store) ReadOnly(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) error {
	return s.run(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, fn)
}

// run: BEGIN, fn, COMMIT. Qualquer erro faz ROLLBACK.
func (s *Store) run(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context, tx app.Tx) error) (err error) {
	tx, err := s.pool.BeginTx(ctx, opts)
	if err != nil {
		return mapError(err)
	}
	defer func() {
		if err != nil {
			// Contexto próprio: mesmo com ctx cancelado, libera conexão e locks já.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(ctx, &txRepo{tx: tx}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		// As constraint triggers rodam aqui, no COMMIT.
		return mapError(err)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// wrap traduz o erro e põe o nome da operação na mensagem.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	mapped := mapError(err)
	if errors.Is(mapped, app.ErrNotFound) {
		return mapped
	}
	return fmt.Errorf("%s: %w", op, mapped)
}
