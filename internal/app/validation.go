package app

import (
	"strings"

	"github.com/google/uuid"
)

// validateID: até 128 caracteres, ASCII visível, sem espaço nas pontas.
// Evita ids que parecem iguais mas não são (espaço no fim, Unicode
// parecido) e quebrariam a idempotência.
func validateID(field, v string, required bool) error {
	if v == "" {
		if required {
			return &ValidationError{Code: CodeInvalidField, Field: field, Message: field + " is required"}
		}
		return nil
	}
	if len(v) > maxIDLength || strings.TrimSpace(v) != v {
		return &ValidationError{Code: CodeInvalidField, Field: field, Message: field + " must have at most 128 chars and no surrounding spaces"}
	}
	for _, c := range v {
		if c < 0x21 || c > 0x7e { // apenas ASCII imprimível, sem espaços
			return &ValidationError{Code: CodeInvalidField, Field: field, Message: field + " must contain printable ASCII characters only"}
		}
	}
	return nil
}

// parseUUID aceita só o formato 8-4-4-4-12.
func parseUUID(field, v string) (uuid.UUID, error) {
	if len(v) != 36 {
		return uuid.Nil, &ValidationError{Code: CodeInvalidField, Field: field, Message: field + " must be a UUID"}
	}
	id, err := uuid.Parse(v)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, &ValidationError{Code: CodeInvalidField, Field: field, Message: field + " must be a non-nil UUID"}
	}
	return id, nil
}
