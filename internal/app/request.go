package app

import (
	"errors"

	"github.com/google/uuid"

	"wallet-service/internal/domain"
	"wallet-service/internal/domain/money"
)

// Formato de entrada, igual no HTTP e no SQS (lá vem dentro de data). Tudo
// é string: dinheiro nunca passa por número no decode.
type RawMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// RawWagerRequest é o corpo como chegou, antes de validar.
type RawWagerRequest struct {
	ProviderID                     string    `json:"providerId"`
	ExternalTransactionID          string    `json:"externalTransactionId"`
	PlayerID                       string    `json:"playerId"`
	WalletID                       string    `json:"walletId"`
	RoundID                        string    `json:"roundId"`
	GameID                         string    `json:"gameId"`
	Kind                           string    `json:"kind"`
	Money                          *RawMoney `json:"money"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId,omitempty"`
}

// WagerOperation é a requisição já validada.
type WagerOperation struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           domain.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	// PayloadHash é o SHA-256 do JSON canônico dos campos de negócio.
	PayloadHash string
}

// Tamanho máximo dos ids textuais.
const maxIDLength = 128

// ParseWagerRequest valida a entrada e calcula o hash. HTTP e SQS usam esta
// mesma função, então a mesma operação gera o mesmo hash nos dois.
//
// Erro aqui é entrada inválida (400 / DLQ): nada é gravado.
func ParseWagerRequest(r RawWagerRequest) (WagerOperation, error) {
	var op WagerOperation
	var err error

	for _, f := range []struct{ name, value string }{
		{"providerId", r.ProviderID}, {"externalTransactionId", r.ExternalTransactionID},
		{"roundId", r.RoundID}, {"gameId", r.GameID},
	} {
		if err := validateID(f.name, f.value, true); err != nil {
			return op, err
		}
	}
	if err := validateID("referenceExternalTransactionId", r.ReferenceExternalTransactionID, false); err != nil {
		return op, err
	}

	if op.PlayerID, err = parseUUID("playerId", r.PlayerID); err != nil {
		return op, err
	}
	if op.WalletID, err = parseUUID("walletId", r.WalletID); err != nil {
		return op, err
	}

	// OPENING tem erro próprio: é só para uso interno.
	op.Kind, err = domain.ParseExternalKind(r.Kind)
	switch {
	case errors.Is(err, domain.ErrOpeningNotAllowed):
		return op, &ValidationError{Code: CodeOpeningNotAllowed, Field: "kind", Message: "OPENING is reserved for internal wallet opening"}
	case err != nil:
		return op, &ValidationError{Code: CodeInvalidKind, Field: "kind", Message: "kind must be one of BET, WIN, LOSS, REFUND, ROLLBACK"}
	}

	if r.Money == nil {
		return op, &ValidationError{Code: CodeInvalidMoney, Field: "money", Message: "money is required"}
	}
	op.Money, err = money.Parse(r.Money.Amount, r.Money.Currency)
	switch {
	case errors.Is(err, money.ErrUnsupportedCurrency):
		return op, &ValidationError{Code: CodeUnsupportedCurrency, Field: "money.currency", Message: "currency must be an uppercase supported ISO 4217 code"}
	case err != nil:
		return op, &ValidationError{Code: CodeInvalidMoney, Field: "money.amount", Message: "amount must be a non-negative decimal string with exactly 2 decimal places"}
	}

	// Valor e referência conforme o tipo.
	if op.Kind == domain.KindLoss && !op.Money.IsZero() {
		return op, &ValidationError{Code: CodeInvalidAmountForKind, Field: "money.amount", Message: "LOSS requires amount 0.00"}
	}
	if op.Kind != domain.KindLoss && !op.Money.IsPositive() {
		return op, &ValidationError{Code: CodeInvalidAmountForKind, Field: "money.amount", Message: string(op.Kind) + " requires amount greater than zero"}
	}
	if op.Kind.RequiresReference() && r.ReferenceExternalTransactionID == "" {
		return op, &ValidationError{Code: CodeMissingReference, Field: "referenceExternalTransactionId", Message: string(op.Kind) + " requires referenceExternalTransactionId"}
	}
	if (op.Kind == domain.KindBet || op.Kind == domain.KindLoss) && r.ReferenceExternalTransactionID != "" {
		return op, &ValidationError{Code: CodeInvalidField, Field: "referenceExternalTransactionId", Message: string(op.Kind) + " does not accept a reference"}
	}
	if r.ReferenceExternalTransactionID == r.ExternalTransactionID {
		return op, &ValidationError{Code: CodeInvalidField, Field: "referenceExternalTransactionId", Message: "a transaction cannot reference itself"}
	}

	op.ProviderID = r.ProviderID
	op.ExternalTransactionID = r.ExternalTransactionID
	op.RoundID = r.RoundID
	op.GameID = r.GameID
	op.ReferenceExternalTransactionID = r.ReferenceExternalTransactionID
	op.PayloadHash = PayloadHash(op)
	return op, nil
}

// ValidateIdempotencyKey usa as mesmas regras dos ids. A chave é usada como
// veio; o servidor nunca troca por uma calculada.
func ValidateIdempotencyKey(key string) error {
	if key == "" {
		return &ValidationError{Code: CodeMissingIdempotency, Field: "Idempotency-Key", Message: "Idempotency-Key is required"}
	}
	return validateID("idempotencyKey", key, true)
}
