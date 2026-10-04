package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"wallet-service/internal/app"
)

// mapError traduz erros do Postgres para o que a aplicação entende:
//
//	23505 (unique)            -> ErrUniqueViolation (refaz)
//	40001, 40P01, 55P03       -> ErrRetryable (refaz)
//	08xxx, 57P0x, timeout     -> ErrUnavailable (tenta mais tarde)
//	sem linhas                -> ErrNotFound
//
// Assim a aplicação não precisa conhecer códigos do Postgres. CHECK ou
// trigger violados sobem como estão: é bug ou tentativa de quebrar regra.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505":
			return fmt.Errorf("%w: %s", app.ErrUniqueViolation, pgErr.ConstraintName)
		case pgErr.Code == "40001" || pgErr.Code == "40P01" || pgErr.Code == "55P03": // 55P03 = lock_timeout
			return fmt.Errorf("%w: %s", app.ErrRetryable, pgErr.Message)
		case len(pgErr.Code) >= 2 && pgErr.Code[:2] == "08",
			pgErr.Code == "57P01", pgErr.Code == "57P02", pgErr.Code == "57P03",
			pgErr.Code == "53300": // too_many_connections
			return fmt.Errorf("%w: %s", app.ErrUnavailable, pgErr.Message)
		}
		return err
	}
	// Falha de rede ou conexão (banco fora do ar).
	var netErr net.Error
	var connErr *pgconn.ConnectError
	if errors.As(err, &netErr) || errors.As(err, &connErr) || pgconn.Timeout(err) || pgconn.SafeToRetry(err) {
		return fmt.Errorf("%w: %v", app.ErrUnavailable, err)
	}
	if errors.Is(err, pgx.ErrTxClosed) {
		return fmt.Errorf("%w: %v", app.ErrUnavailable, err)
	}
	return err
}
