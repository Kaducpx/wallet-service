package money

import (
	"fmt"
	"math"
)

// Scale é o número de casas decimais aceito: "25.00" sim, "25.0" não.
const Scale = 2

const unitsPerWhole = 100 // centavos por unidade

// Money é imutável: as operações sempre devolvem um valor novo.
//
// Money{} (sem moeda) conta como "não inicializado" e é recusado pelas
// operações. Assim um campo esquecido nunca vira R$ 0,00 sem ninguém notar.
type Money struct {
	units    int64 // centavos: 2500 = 25,00
	currency Currency
}

// FromUnits monta um Money a partir de centavos (ex.: valor lido do banco).
// MinInt64 é recusado porque não tem negativo correspondente em int64.
func FromUnits(units int64, currency Currency) (Money, error) {
	if currency.IsZero() {
		return Money{}, ErrUninitialized
	}
	if units == math.MinInt64 {
		return Money{}, ErrAmountOverflow
	}
	return Money{units: units, currency: currency}, nil
}

// Zero devolve 0,00 na moeda informada.
func Zero(currency Currency) Money { return Money{currency: currency} }

func (m Money) Units() int64        { return m.units }
func (m Money) Currency() Currency  { return m.currency }
func (m Money) IsZero() bool        { return m.units == 0 }
func (m Money) IsPositive() bool    { return m.units > 0 }
func (m Money) IsNegative() bool    { return m.units < 0 }
func (m Money) IsInitialized() bool { return !m.currency.IsZero() }

// Amount formata no padrão do contrato: 2510 -> "25.10", -310 -> "-3.10".
func (m Money) Amount() string {
	u := m.units
	sign := ""
	if u < 0 {
		sign = "-"
		u = -u // seguro: units nunca é MinInt64
	}
	return fmt.Sprintf("%s%d.%02d", sign, u/unitsPerWhole, u%unitsPerWhole)
}

func (m Money) String() string { return m.Amount() + " " + m.currency.String() }
