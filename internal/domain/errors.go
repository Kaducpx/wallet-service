package domain

import (
	"errors"
	"fmt"
)

// Erros do domínio. Compare com errors.Is/errors.As, nunca pela mensagem.
// Regra de negócio não usa panic. Os erros de dinheiro ficam no pacote money.
var (
	// Carteira.
	ErrInsufficientFunds  = errors.New("insufficient funds")
	ErrNonPositiveAmount  = errors.New("amount must be greater than zero")
	ErrInvalidWallet      = errors.New("invalid wallet")
	ErrInvalidLedgerEntry = errors.New("invalid ledger entry")

	// Transações de aposta.
	ErrInvalidTransaction     = errors.New("invalid wager transaction")
	ErrInvalidKind            = errors.New("invalid transaction kind")
	ErrOpeningNotAllowed      = errors.New("OPENING is reserved for internal wallet opening")
	ErrInvalidAmountForKind   = errors.New("amount not allowed for transaction kind")
	ErrMissingReference       = errors.New("reference external transaction id is required")
	ErrInvalidStateTransition = errors.New("invalid transaction state transition")

	// Eventos.
	ErrInvalidEvent = errors.New("invalid event")
)

// FieldError diz qual campo está errado sem perder o erro original
// (errors.Is continua funcionando).
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return fmt.Sprintf("%s: %v", e.Field, e.Err) }
func (e *FieldError) Unwrap() error { return e.Err }

func fieldErr(field string, err error) error { return &FieldError{Field: field, Err: err} }
