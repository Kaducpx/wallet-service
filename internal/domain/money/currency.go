package money

import "fmt"

// Moedas aceitas. Todas têm 2 casas decimais, então a escala fixa funciona.
var supportedCurrencies = map[string]struct{}{
	"BRL": {},
	"USD": {},
	"EUR": {},
}

// Currency é um código ISO 4217 já validado. Currency{} = não inicializada.
type Currency struct{ code string }

// ParseCurrency só aceita o código em maiúsculas. "brl" é recusado em vez de
// convertido, para cada operação ter uma única forma (e um único hash).
func ParseCurrency(code string) (Currency, error) {
	if _, ok := supportedCurrencies[code]; !ok {
		return Currency{}, fmt.Errorf("%w: %q", ErrUnsupportedCurrency, code)
	}
	return Currency{code: code}, nil
}

// MustCurrency é para testes e constantes. Nunca use com entrada externa.
func MustCurrency(code string) Currency {
	c, err := ParseCurrency(code)
	if err != nil {
		panic(err)
	}
	return c
}

func (c Currency) String() string { return c.code }
func (c Currency) IsZero() bool   { return c.code == "" }
