package money

import "errors"

// Erros sentinela. Quem chama classifica com errors.Is, nunca comparando
// mensagens.
var (
	ErrInvalidAmount       = errors.New("invalid monetary amount")
	ErrUnsupportedCurrency = errors.New("unsupported currency")
	ErrCurrencyMismatch    = errors.New("currency mismatch")
	ErrAmountOverflow      = errors.New("monetary amount overflow")
	ErrUninitialized       = errors.New("uninitialized money value")
	ErrNegativeAmount      = errors.New("negative amount not allowed")
)
