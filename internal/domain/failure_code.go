package domain

// FailureCode é o motivo da recusa, gravado e devolvido ao provedor. São
// resultados definitivos: um replay devolve sempre o mesmo código.
//
// Entrada mal formatada (JSON inválido, valor errado) não chega aqui: não é
// gravada e tem códigos próprios na API.
type FailureCode string

const (
	// BET sem saldo suficiente.
	FailureInsufficientFunds FailureCode = "INSUFFICIENT_FUNDS"
	// ROLLBACK sem saldo para desfazer. Diferente de INSUFFICIENT_FUNDS de
	// propósito.
	FailureReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	// A carteira não é do jogador informado.
	FailureWalletPlayerMismatch FailureCode = "WALLET_PLAYER_MISMATCH"
	// Moeda diferente da carteira.
	FailureWalletCurrencyMismatch FailureCode = "WALLET_CURRENCY_MISMATCH"
	// A referência não chegou dentro do prazo.
	FailureReferenceNotFound FailureCode = "REFERENCE_NOT_FOUND"
	// A referência existe, mas foi rejeitada ou falhou.
	FailureReferenceNotProcessed FailureCode = "REFERENCE_NOT_PROCESSED"
	// Referência de outro jogador, carteira, moeda ou rodada.
	FailureReferenceMismatch FailureCode = "REFERENCE_MISMATCH"
	// Tipo de referência que não combina (ex.: REFUND de um WIN).
	FailureReferenceInvalidKind FailureCode = "REFERENCE_INVALID_KIND"
	// Reversão com valor diferente do original.
	FailureReferenceAmountMismatch FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	// A referência já foi revertida (REFUND ou ROLLBACK).
	FailureReferenceAlreadyReversed FailureCode = "REFERENCE_ALREADY_REVERSED"
	// Crédito estouraria o limite do saldo.
	FailureBalanceLimitExceeded FailureCode = "BALANCE_LIMIT_EXCEEDED"
	// Falha permanente (status FAILED).
	FailurePermanentProcessingError FailureCode = "PERMANENT_PROCESSING_ERROR"
)
