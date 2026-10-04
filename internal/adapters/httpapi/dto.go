package httpapi

import (
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/app"
	"wallet-service/internal/domain"
	"wallet-service/internal/domain/money"
)

// Corpo de POST /wallets.
type openWalletRequest struct {
	PlayerID       string        `json:"playerId"`
	InitialBalance *app.RawMoney `json:"initialBalance"`
}

type walletResponse struct {
	ID        uuid.UUID   `json:"id"`
	PlayerID  uuid.UUID   `json:"playerId"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

func toWallet(w *domain.Wallet) walletResponse {
	return walletResponse{ID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version(),
		CreatedAt: w.CreatedAt(), UpdatedAt: w.UpdatedAt()}
}

type ledgerEntryResponse struct {
	ID            uuid.UUID        `json:"id"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     domain.Direction `json:"direction"`
	Amount        money.Money      `json:"amount"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
	CreatedAt     time.Time        `json:"createdAt"`
}

// nextCursor só vem se houver mais páginas.
type ledgerPageResponse struct {
	WalletID   uuid.UUID             `json:"walletId"`
	Entries    []ledgerEntryResponse `json:"entries"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func toLedgerPage(walletID uuid.UUID, p app.LedgerPage) ledgerPageResponse {
	out := ledgerPageResponse{WalletID: walletID, Entries: make([]ledgerEntryResponse, 0, len(p.Entries))}
	for _, e := range p.Entries {
		out.Entries = append(out.Entries, ledgerEntryResponse{
			ID: e.ID(), TransactionID: e.TransactionID(), Direction: e.Direction(), Amount: e.Amount(),
			BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(),
			WalletVersion: e.WalletVersion(), CreatedAt: e.CreatedAt(),
		})
	}
	if p.NextAfterVersion > 0 {
		out.NextCursor = encodeCursor(p.NextAfterVersion)
	}
	return out
}

// Resposta de POST /wagering/transactions.
type submitResponse struct {
	TransactionID    uuid.UUID     `json:"transactionId"`
	Status           domain.Status `json:"status"`
	Balance          *money.Money  `json:"balance,omitempty"`
	FailureCode      string        `json:"failureCode,omitempty"`
	IdempotentReplay bool          `json:"idempotentReplay"`
}

// Resposta das consultas de transação.
type transactionResponse struct {
	TransactionID                  uuid.UUID     `json:"transactionId"`
	Origin                         domain.Origin `json:"origin"`
	Kind                           domain.Kind   `json:"kind"`
	Status                         domain.Status `json:"status"`
	WalletID                       uuid.UUID     `json:"walletId"`
	PlayerID                       uuid.UUID     `json:"playerId"`
	Money                          money.Money   `json:"money"`
	ProviderID                     string        `json:"providerId,omitempty"`
	ExternalTransactionID          string        `json:"externalTransactionId,omitempty"`
	RoundID                        string        `json:"roundId,omitempty"`
	GameID                         string        `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         *uuid.UUID    `json:"referenceTransactionId,omitempty"`
	FailureCode                    string        `json:"failureCode,omitempty"`
	Balance                        *money.Money  `json:"balance,omitempty"`
	Attempts                       int           `json:"attempts,omitempty"`
	NextRetryAt                    *time.Time    `json:"nextRetryAt,omitempty"`
	ExpiresAt                      *time.Time    `json:"expiresAt,omitempty"`
	CreatedAt                      time.Time     `json:"createdAt"`
	UpdatedAt                      time.Time     `json:"updatedAt"`
}

// Os campos de espera (attempts, nextRetryAt, expiresAt) só aparecem
// enquanto a transação está pendente.
func toTransaction(t *domain.WagerTransaction) transactionResponse {
	r := transactionResponse{
		TransactionID: t.ID(), Origin: t.Origin(), Kind: t.Kind(), Status: t.Status(),
		WalletID: t.WalletID(), PlayerID: t.PlayerID(), Money: t.Money(),
		ReferenceTransactionID: t.ReferenceTransactionID(), FailureCode: string(t.FailureCode()),
		Balance: t.ResultBalance(), CreatedAt: t.CreatedAt(), UpdatedAt: t.UpdatedAt(),
	}
	if e := t.External(); e != nil {
		r.ProviderID, r.ExternalTransactionID = e.ProviderID, e.ExternalTransactionID
		r.RoundID, r.GameID = e.RoundID, e.GameID
		r.ReferenceExternalTransactionID = e.ReferenceExternalTransactionID
	}
	if t.Status() == domain.StatusPendingReference {
		r.Attempts, r.NextRetryAt, r.ExpiresAt = t.Attempts(), t.NextRetryAt(), t.ExpiresAt()
	}
	return r
}

type reconciliationResponse struct {
	WalletID          uuid.UUID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}

// Todo erro sai neste formato.
type errorBody struct {
	Error errorDetail `json:"error"`
}

// code é estável (para código); message é para gente ler.
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}
