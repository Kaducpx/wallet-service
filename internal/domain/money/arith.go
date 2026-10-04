package money

import (
	"fmt"
	"math"
)

// Todas as operações exigem a mesma moeda (somar BRL com USD é erro, não
// conversão) e detectam overflow. Resultado negativo é permitido aqui; quem
// impede saldo negativo é a Wallet e o CHECK do banco.

func (m Money) sameCurrency(o Money) error {
	if !m.IsInitialized() || !o.IsInitialized() {
		return ErrUninitialized
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}

// Add soma, checando o estouro antes da conta. O piso é -MaxInt64 (e não
// MinInt64) para que qualquer valor possa ser negado depois.
func (m Money) Add(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	if (o.units > 0 && m.units > math.MaxInt64-o.units) ||
		(o.units < 0 && m.units < -math.MaxInt64-o.units) {
		return Money{}, ErrAmountOverflow
	}
	return Money{units: m.units + o.units, currency: m.currency}, nil
}

// Sub faz m - o, reaproveitando a checagem de estouro do Add.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	neg, err := o.Neg()
	if err != nil {
		return Money{}, err
	}
	return m.Add(neg)
}

// Neg inverte o sinal. A checagem de MinInt64 é só defesa: FromUnits já
// impede esse valor.
func (m Money) Neg() (Money, error) {
	if !m.IsInitialized() {
		return Money{}, ErrUninitialized
	}
	if m.units == math.MinInt64 {
		return Money{}, ErrAmountOverflow
	}
	return Money{units: -m.units, currency: m.currency}, nil
}

// Cmp devolve -1, 0 ou 1. Moedas diferentes não são comparáveis: dá erro.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.sameCurrency(o); err != nil {
		return 0, err
	}
	switch {
	case m.units < o.units:
		return -1, nil
	case m.units > o.units:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal compara valor e moeda (10 BRL != 10 USD), sem erro.
func (m Money) Equal(o Money) bool { return m.units == o.units && m.currency == o.currency }
