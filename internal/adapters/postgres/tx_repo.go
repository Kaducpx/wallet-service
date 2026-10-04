package postgres

import (
	"github.com/jackc/pgx/v5"

	"wallet-service/internal/domain"
)

// txRepo implementa app.Tx. Os métodos estão espalhados em *_repo.go, mas
// todos usam o mesmo r.tx, ou seja, a mesma transação.
type txRepo struct {
	tx pgx.Tx
}

// deref: NULL vira "".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nullIfEmpty: "" vira NULL.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resultBalance separa o saldo em centavos e moeda (nil vira NULL).
func resultBalance(t *domain.WagerTransaction) (*int64, *string) {
	b := t.ResultBalance()
	if b == nil {
		return nil, nil
	}
	u, c := b.Units(), b.Currency().String()
	return &u, &c
}
