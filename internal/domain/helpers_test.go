package domain

import (
	"testing"
	"time"

	"wallet-service/internal/domain/money"
)

// Utilitários compartilhados pelos testes do domínio.

var (
	brl = money.MustCurrency("BRL")
	t0  = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
)

// mustMoney cria um Money a partir de string e falha o teste se for inválido.
func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("money.Parse(%q,%q): %v", amount, currency, err)
	}
	return m
}
