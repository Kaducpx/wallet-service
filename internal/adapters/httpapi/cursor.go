package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

// O cursor é base64url de {"v": walletVersion}. Para o cliente é só um texto.
type cursor struct {
	V int64 `json:"v"`
}

func encodeCursor(v int64) string {
	b, _ := json.Marshal(cursor{V: v})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor recusa cursor inválido. Vazio = primeira página.
func decodeCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, errors.New("invalid cursor")
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil || c.V < 1 {
		return 0, errors.New("invalid cursor")
	}
	return c.V, nil
}
