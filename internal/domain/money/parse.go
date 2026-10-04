package money

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Parse lê valores vindos de fora ({"amount":"25.00","currency":"BRL"}).
//
// Aceita só o formato exato: dígitos, ponto e duas casas. Recusa sinal,
// espaço, notação científica, NaN, Infinity, zero à esquerda ("007.00") e
// valores negativos. Nada é arredondado: fora do formato, é erro.
//
//	"25.00" -> 2500     "25"    -> erro
//	"0.01"  -> 1        "1e3"   -> erro
//	"-5.00" -> erro (negativo)
func Parse(amount, currency string) (Money, error) {
	cur, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	if rest, neg := strings.CutPrefix(amount, "-"); neg {
		// Valida o formato antes, para "-NaN" dar erro de formato e não de sinal.
		if _, err := parseUnsignedDecimal(rest); err != nil {
			return Money{}, err
		}
		return Money{}, fmt.Errorf("%w: %q", ErrNegativeAmount, amount)
	}
	units, err := parseUnsignedDecimal(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{units: units, currency: cur}, nil
}

// ParseSigned também aceita negativos. Uso interno apenas (diferenças da
// reconciliação, leitura de eventos).
func ParseSigned(amount, currency string) (Money, error) {
	cur, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	rest, neg := strings.CutPrefix(amount, "-")
	units, err := parseUnsignedDecimal(rest)
	if err != nil {
		return Money{}, err
	}
	if neg {
		units = -units
	}
	return Money{units: units, currency: cur}, nil
}

// parseUnsignedDecimal converte "123.45" em 12345 separando parte inteira e
// decimal. Sem ParseFloat.
func parseUnsignedDecimal(s string) (int64, error) {
	invalid := func() (int64, error) { return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, s) }

	// Precisa ter algo antes do ponto e exatamente 2 dígitos depois.
	dot := strings.IndexByte(s, '.')
	if dot <= 0 || len(s)-dot-1 != Scale {
		return invalid()
	}
	intPart, fracPart := s[:dot], s[dot+1:]

	// Uma forma só por valor ("0.50" sim, "00.50" não): o hash de
	// idempotência não precisa normalizar nada.
	if len(intPart) > 1 && intPart[0] == '0' {
		return invalid()
	}
	if !allDigits(intPart) || !allDigits(fracPart) {
		return invalid()
	}

	whole, err := strconv.ParseUint(intPart, 10, 63) // 63 bits = até MaxInt64
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrAmountOverflow, s)
	}
	frac, _ := strconv.ParseUint(fracPart, 10, 8) // já validado: 2 dígitos

	// Checa o estouro antes de multiplicar:
	// whole*100 + frac <= MaxInt64  <=>  whole <= (MaxInt64 - frac) / 100
	if whole > (math.MaxInt64-frac)/unitsPerWhole {
		return 0, fmt.Errorf("%w: %q", ErrAmountOverflow, s)
	}
	return int64(whole*unitsPerWhole + frac), nil
}

// allDigits aceita só 0-9 ASCII (unicode.IsDigit deixaria passar dígitos de
// outros alfabetos).
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
