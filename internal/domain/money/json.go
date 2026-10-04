package money

import (
	"encoding/json"
	"fmt"
)

// moneyJSON é a forma serializada: valor como string decimal, nunca número.
type moneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON produz {"amount":"25.00","currency":"BRL"}.
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsInitialized() {
		return nil, ErrUninitialized
	}
	return json.Marshal(moneyJSON{Amount: m.Amount(), Currency: m.currency.String()})
}

// UnmarshalJSON aceita valores com sinal porque também é usado para ler
// payloads internos (eventos, diferenças). Entradas externas passam por
// Parse, que rejeita negativos. Um amount numérico (25.00 sem aspas) é
// rejeitado: o contrato só aceita string.
func (m *Money) UnmarshalJSON(b []byte) error {
	var raw moneyJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAmount, err)
	}
	parsed, err := ParseSigned(raw.Amount, raw.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}
