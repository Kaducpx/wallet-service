package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"wallet-service/internal/app"
)

// decodeStrict lê até 64 KiB e recusa campo desconhecido ou lixo depois do JSON.
func decodeStrict(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return &app.ValidationError{Code: app.CodeInvalidRequest, Message: "could not read body"}
	}
	if len(body) > maxBodyBytes {
		return &app.ValidationError{Code: app.CodeInvalidRequest, Message: "body too large"}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &app.ValidationError{Code: app.CodeInvalidRequest, Message: "invalid JSON body: " + err.Error()}
	}
	if dec.More() {
		return &app.ValidationError{Code: app.CodeInvalidRequest, Message: "unexpected data after JSON body"}
	}
	return nil
}

// pathUUID lê um UUID da rota. Se for inválido, já responde 400 e devolve false.
func pathUUID(w http.ResponseWriter, r *http.Request, log *slog.Logger, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeError(w, r, log, &app.ValidationError{Code: app.CodeInvalidField, Field: name, Message: name + " must be a UUID"})
		return uuid.Nil, false
	}
	return id, true
}
