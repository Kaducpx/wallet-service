package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"wallet-service/internal/app"
	"wallet-service/internal/domain/money"
)

// Erros saem sempre como {"error":{"code","message","field"}}:
//
//	400 INVALID_* / MISSING_*        entrada inválida, nada gravado
//	401 UNAUTHENTICATED              token ausente, inválido ou expirado
//	403 FORBIDDEN / PROVIDER_MISMATCH sem permissão
//	404 WALLET_NOT_FOUND / TRANSACTION_NOT_FOUND
//	409 IDEMPOTENCY_KEY_CONFLICT     mesma chave, conteúdo diferente
//	409 EXTERNAL_TRANSACTION_CONFLICT mesmo id externo, outra chave
//	409 WALLET_ALREADY_EXISTS
//	503 SERVICE_UNAVAILABLE          dependência fora do ar (com Retry-After)
//	500 INTERNAL_ERROR
//
// Resultado de negócio não usa este formato: REJECTED (422),
// PENDING_REFERENCE (202) e PROCESSED (200/201) devolvem a transação.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status, code, field := classify(err)
	msg := http.StatusText(status)
	var ve *app.ValidationError
	if errors.As(err, &ve) {
		msg = ve.Message
	}
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	if status >= 500 {
		log.ErrorContext(r.Context(), "request failed", slog.String("code", code), slog.String("error", err.Error()))
	}
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg, Field: field}})
}

// classify devolve (status HTTP, código, campo). Os casos mais específicos
// vêm primeiro.
func classify(err error) (int, string, string) {
	var ve *app.ValidationError
	switch {
	case errors.As(err, &ve):
		return http.StatusBadRequest, ve.Code, ve.Field
	case errors.Is(err, errUnauthenticated):
		return http.StatusUnauthorized, "UNAUTHENTICATED", ""
	case errors.Is(err, errProviderMismatch):
		return http.StatusForbidden, "PROVIDER_MISMATCH", "providerId"
	case errors.Is(err, errForbidden):
		return http.StatusForbidden, "FORBIDDEN", ""
	case errors.Is(err, app.ErrWalletNotFound):
		return http.StatusNotFound, "WALLET_NOT_FOUND", ""
	case errors.Is(err, app.ErrTransactionNotFound):
		return http.StatusNotFound, "TRANSACTION_NOT_FOUND", ""
	case errors.Is(err, app.ErrIdempotencyConflict):
		return http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", "Idempotency-Key"
	case errors.Is(err, app.ErrExternalIDConflict):
		return http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT", "externalTransactionId"
	case errors.Is(err, app.ErrWalletAlreadyExists):
		return http.StatusConflict, "WALLET_ALREADY_EXISTS", ""
	case errors.Is(err, money.ErrUnsupportedCurrency):
		return http.StatusBadRequest, app.CodeUnsupportedCurrency, ""
	case errors.Is(err, money.ErrInvalidAmount), errors.Is(err, money.ErrNegativeAmount),
		errors.Is(err, money.ErrAmountOverflow), errors.Is(err, money.ErrUninitialized):
		return http.StatusBadRequest, app.CodeInvalidMoney, ""
	case app.IsTransient(err), errors.Is(err, app.ErrUniqueViolation),
		errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", ""
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR", ""
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

var (
	errUnauthenticated  = errors.New("unauthenticated")
	errForbidden        = errors.New("forbidden")
	errProviderMismatch = errors.New("providerId does not match the authenticated provider")
)
