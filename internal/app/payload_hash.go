package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PayloadHash resume a operação num SHA-256. Serve para saber se uma chave
// de idempotência repetida é a mesma operação (replay) ou outra (conflito).
//
// JSON com chaves em ordem alfabética, sem espaços, só com campos de negócio:
// providerId, externalTransactionId, playerId, walletId, roundId, gameId,
// kind, money.amount, money.currency e referenceExternalTransactionId (se
// houver). Ficam de fora a chave de idempotência, messageId, occurredAt e
// outros dados de transporte.
//
// UUIDs vão em minúsculas. O valor já chega numa forma única (o parser só
// aceita "25.00"), então não precisa normalizar.
func PayloadHash(op WagerOperation) string {
	canonical := map[string]any{
		"providerId":            op.ProviderID,
		"externalTransactionId": op.ExternalTransactionID,
		"playerId":              op.PlayerID.String(),
		"walletId":              op.WalletID.String(),
		"roundId":               op.RoundID,
		"gameId":                op.GameID,
		"kind":                  string(op.Kind),
		"money": map[string]any{
			"amount":   op.Money.Amount(),
			"currency": op.Money.Currency().String(),
		},
	}
	if op.ReferenceExternalTransactionID != "" {
		canonical["referenceExternalTransactionId"] = op.ReferenceExternalTransactionID
	}
	// encoding/json já ordena as chaves de mapas.
	b, _ := json.Marshal(canonical)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
