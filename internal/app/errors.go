package app

import (
	"errors"
	"fmt"
)

// Erros da aplicação. HTTP e SQS traduzem cada um num código HTTP ou numa
// decisão (ack, retry, DLQ). Compare sempre com errors.Is.
//
//	ErrWalletNotFound       404   SQS: DLQ
//	ErrIdempotencyConflict  409   SQS: DLQ
//	ErrUnavailable          503   SQS: retry com backoff
//	*ValidationError        400   SQS: DLQ
var (
	// Persistência.
	ErrNotFound         = errors.New("not found")
	ErrUniqueViolation  = errors.New("unique constraint violation")
	ErrConcurrentUpdate = errors.New("concurrent update detected")
	// Deadlock ou lock timeout: pode refazer a transação.
	ErrRetryable = errors.New("retryable database conflict")
	// Dependência fora do ar: tente de novo mais tarde.
	ErrUnavailable = errors.New("dependency temporarily unavailable")

	// Negócio / contrato.
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrWalletAlreadyExists = errors.New("wallet already exists for player and currency")
	ErrTransactionNotFound = errors.New("transaction not found")
	// Mesma Idempotency-Key com conteúdo diferente.
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different payload")
	// (providerId, externalTransactionId) já usado com outra chave.
	ErrExternalIDConflict = errors.New("external transaction already registered with another idempotency key")
	// Mesmo messageId com conteúdo diferente (reentrega adulterada).
	ErrInboxPayloadMismatch = errors.New("message id reused with a different payload")
)

// ValidationError é entrada inválida. Nada é gravado; o cliente corrige e
// reenvia.
type ValidationError struct {
	Code    string // código estável, ex.: INVALID_MONEY
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s (%s): %s", e.Code, e.Field, e.Message)
	}
	return e.Code + ": " + e.Message
}

// Códigos de validação (entradas corrigíveis, não persistidas).
const (
	CodeInvalidRequest       = "INVALID_REQUEST"
	CodeInvalidField         = "INVALID_FIELD"
	CodeInvalidMoney         = "INVALID_MONEY"
	CodeUnsupportedCurrency  = "UNSUPPORTED_CURRENCY"
	CodeInvalidKind          = "INVALID_KIND"
	CodeOpeningNotAllowed    = "OPENING_NOT_ALLOWED"
	CodeInvalidAmountForKind = "INVALID_AMOUNT_FOR_KIND"
	CodeMissingReference     = "MISSING_REFERENCE"
	CodeMissingIdempotency   = "MISSING_IDEMPOTENCY_KEY"
)

// IsTransient diz se vale tentar de novo mais tarde.
func IsTransient(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRetryable) || errors.Is(err, ErrConcurrentUpdate)
}
