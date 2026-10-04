package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

var brl = MustCurrency("BRL")

// mustMoney cria um Money a partir de string e falha o teste se for inválido.
func mustMoney(t *testing.T, amount, currency string) Money {
	t.Helper()
	m, err := Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q,%q): %v", amount, currency, err)
	}
	return m
}

// Valores válidos viram os centavos corretos e voltam à mesma string,
// inclusive o maior valor representável em int64.
func TestParseMoneyValid(t *testing.T) {
	cases := map[string]int64{
		"0.00":                 0,
		"0.01":                 1,
		"25.00":                2500,
		"1000.99":              100099,
		"92233720368547758.07": math.MaxInt64, // limite exato do int64
	}
	for in, want := range cases {
		m, err := Parse(in, "BRL")
		if err != nil {
			t.Fatalf("%q: unexpected error %v", in, err)
		}
		if m.Units() != want {
			t.Fatalf("%q: units = %d, want %d", in, m.Units(), want)
		}
		if m.Amount() != in {
			t.Fatalf("%q: round-trip produced %q", in, m.Amount())
		}
	}
}

// Escala errada, notação científica, NaN, Infinity, sinais, espaços e zeros
// à esquerda são rejeitados com ErrInvalidAmount (nada é arredondado).
func TestParseMoneyRejectsInvalidInput(t *testing.T) {
	invalid := []string{
		"", " ", "25", "25.", ".50", "25.0", "25.000", // escala
		"1e3", "1E3", "2.5e1", // notação científica
		"NaN", "nan", "Infinity", "-Infinity", "+Inf", // não numéricos
		"+25.00", " 25.00", "25.00 ", "25,00", "2_5.00", "0x10.00", // formato
		"007.00", "00.00", // zeros à esquerda (forma não canônica)
	}
	for _, in := range invalid {
		if _, err := Parse(in, "BRL"); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("%q: expected ErrInvalidAmount, got %v", in, err)
		}
	}
}

// Entradas externas negativas são rejeitadas com erro próprio.
func TestParseMoneyRejectsNegativeExternalInput(t *testing.T) {
	if _, err := Parse("-1.00", "BRL"); !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("expected ErrNegativeAmount, got %v", err)
	}
}

// Valores acima do limite do int64 são recusados, nunca "dão a volta".
func TestParseMoneyOverflow(t *testing.T) {
	for _, in := range []string{"92233720368547758.08", "99999999999999999999.00", "92233720368547759.00"} {
		if _, err := Parse(in, "BRL"); !errors.Is(err, ErrAmountOverflow) {
			t.Errorf("%q: expected ErrAmountOverflow, got %v", in, err)
		}
	}
}

// Só códigos ISO 4217 suportados e em maiúsculas são aceitos.
func TestParseCurrency(t *testing.T) {
	for _, bad := range []string{"", "brl", "BR", "BRLL", "XXX", "BTC"} {
		if _, err := Parse("1.00", bad); !errors.Is(err, ErrUnsupportedCurrency) {
			t.Errorf("%q: expected ErrUnsupportedCurrency, got %v", bad, err)
		}
	}
}

// Soma, subtração (inclusive com resultado negativo), negação e comparação.
func TestMoneyArithmetic(t *testing.T) {
	a := mustMoney(t, "100.00", "BRL")
	b := mustMoney(t, "80.50", "BRL")

	sum, err := a.Add(b)
	if err != nil || sum.Amount() != "180.50" {
		t.Fatalf("Add = %v, %v", sum, err)
	}
	diff, err := b.Sub(a)
	if err != nil || diff.Amount() != "-19.50" || !diff.IsNegative() {
		t.Fatalf("Sub = %v, %v (diferenças internas podem ser negativas)", diff, err)
	}
	neg, err := a.Neg()
	if err != nil || neg.Amount() != "-100.00" {
		t.Fatalf("Neg = %v, %v", neg, err)
	}
	if c, _ := a.Cmp(b); c != 1 {
		t.Fatalf("Cmp(a,b) = %d", c)
	}
	if c, _ := b.Cmp(a); c != -1 {
		t.Fatalf("Cmp(b,a) = %d", c)
	}
	if c, _ := a.Cmp(a); c != 0 {
		t.Fatalf("Cmp(a,a) = %d", c)
	}
	if !Zero(brl).IsZero() || Zero(brl).Amount() != "0.00" {
		t.Fatal("Zero(BRL) must be 0.00")
	}
}

// Operar moedas diferentes é erro, nunca conversão silenciosa.
func TestMoneyCurrencyMismatch(t *testing.T) {
	a := mustMoney(t, "1.00", "BRL")
	b := mustMoney(t, "1.00", "USD")
	if _, err := a.Add(b); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Add: %v", err)
	}
	if _, err := a.Sub(b); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Sub: %v", err)
	}
	if _, err := a.Cmp(b); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Cmp: %v", err)
	}
	if a.Equal(b) {
		t.Error("Equal must consider the currency")
	}
}

// Overflow em soma e subtração é detectado; MinInt64 é recusado para que a
// negação sempre funcione.
func TestMoneyOverflowInArithmetic(t *testing.T) {
	max, _ := FromUnits(math.MaxInt64, brl)
	one, _ := FromUnits(1, brl)
	if _, err := max.Add(one); !errors.Is(err, ErrAmountOverflow) {
		t.Errorf("Add overflow: %v", err)
	}
	min, _ := FromUnits(-math.MaxInt64, brl)
	if _, err := min.Sub(one); !errors.Is(err, ErrAmountOverflow) {
		t.Errorf("Sub overflow: %v", err)
	}
	if _, err := FromUnits(math.MinInt64, brl); !errors.Is(err, ErrAmountOverflow) {
		t.Errorf("MinInt64 must be rejected so that Neg never overflows: %v", err)
	}
	neg, err := min.Neg()
	if err != nil || neg.Units() != math.MaxInt64 {
		t.Errorf("Neg(-MaxInt64) = %v, %v", neg, err)
	}
}

// Money{} (sem moeda) é recusado em todas as operações e na serialização.
func TestUninitializedMoneyIsRejected(t *testing.T) {
	var zero Money
	if _, err := zero.Add(mustMoney(t, "1.00", "BRL")); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Add: %v", err)
	}
	if _, err := zero.Neg(); !errors.Is(err, ErrUninitialized) {
		t.Errorf("Neg: %v", err)
	}
	if _, err := json.Marshal(zero); err == nil {
		t.Error("Marshal of uninitialized money must fail")
	}
	if _, err := FromUnits(10, Currency{}); !errors.Is(err, ErrUninitialized) {
		t.Errorf("FromUnits: %v", err)
	}
}

// O JSON segue o contrato {"amount":"25.00","currency":"BRL"} e recusa
// amount numérico.
func TestMoneyJSONRoundTrip(t *testing.T) {
	m := mustMoney(t, "25.00", "BRL")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("unexpected JSON %s", b)
	}
	var back Money
	if err := json.Unmarshal(b, &back); err != nil || !back.Equal(m) {
		t.Fatalf("round trip: %v %v", back, err)
	}
	// Valor numérico (não string) é rejeitado: o contrato só aceita string.
	if err := json.Unmarshal([]byte(`{"amount":25.00,"currency":"BRL"}`), &back); err == nil {
		t.Fatal("numeric amount must be rejected")
	}
}
