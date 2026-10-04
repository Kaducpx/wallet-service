package domain

import (
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain/money"
)

// Eventos avisam outros sistemas do que aconteceu (aposta processada, saldo
// alterado...). O domínio só cria; a aplicação grava na outbox junto com o
// resto e um worker publica depois do commit.

// DomainEvent é a interface comum dos eventos. Tipo e versão são fixos em
// cada tipo.
type DomainEvent interface {
	EventType() string
	EventVersion() int
	// AggregateID é sempre o walletId (vira o MessageGroupId na fila).
	AggregateID() uuid.UUID
}

const (
	EventWagerTransactionProcessed        = "WagerTransactionProcessed"
	EventWagerTransactionRejected         = "WagerTransactionRejected"
	EventWalletBalanceChanged             = "WalletBalanceChanged"
	EventWagerTransactionPendingReference = "WagerTransactionPendingReference"
)

// WagerTransactionProcessed: operação concluída (inclusive LOSS e OPENING).
type WagerTransactionProcessed struct {
	TransactionID          uuid.UUID   `json:"transactionId"`
	WalletID               uuid.UUID   `json:"walletId"`
	PlayerID               uuid.UUID   `json:"playerId"`
	Origin                 Origin      `json:"origin"`
	Kind                   Kind        `json:"kind"`
	Money                  money.Money `json:"money"`
	BalanceAfter           money.Money `json:"balanceAfter"`
	ProviderID             string      `json:"providerId,omitempty"`
	ExternalTransactionID  string      `json:"externalTransactionId,omitempty"`
	RoundID                string      `json:"roundId,omitempty"`
	GameID                 string      `json:"gameId,omitempty"`
	ReferenceTransactionID *uuid.UUID  `json:"referenceTransactionId,omitempty"`
	ProcessedAt            time.Time   `json:"processedAt"`
}

func NewWagerTransactionProcessed(t *WagerTransaction) WagerTransactionProcessed {
	ev := WagerTransactionProcessed{
		TransactionID: t.ID(), WalletID: t.WalletID(), PlayerID: t.PlayerID(),
		Origin: t.Origin(), Kind: t.Kind(), Money: t.Money(),
		ReferenceTransactionID: t.ReferenceTransactionID(), ProcessedAt: t.UpdatedAt(),
	}
	if b := t.ResultBalance(); b != nil {
		ev.BalanceAfter = *b
	}
	if e := t.External(); e != nil {
		ev.ProviderID, ev.ExternalTransactionID = e.ProviderID, e.ExternalTransactionID
		ev.RoundID, ev.GameID = e.RoundID, e.GameID
	}
	return ev
}

func (WagerTransactionProcessed) EventType() string        { return EventWagerTransactionProcessed }
func (WagerTransactionProcessed) EventVersion() int        { return 1 }
func (e WagerTransactionProcessed) AggregateID() uuid.UUID { return e.WalletID }

// WagerTransactionRejected: operação recusada.
type WagerTransactionRejected struct {
	TransactionID         uuid.UUID   `json:"transactionId"`
	WalletID              uuid.UUID   `json:"walletId"`
	PlayerID              uuid.UUID   `json:"playerId"`
	Kind                  Kind        `json:"kind"`
	Money                 money.Money `json:"money"`
	FailureCode           FailureCode `json:"failureCode"`
	ProviderID            string      `json:"providerId"`
	ExternalTransactionID string      `json:"externalTransactionId"`
	RoundID               string      `json:"roundId"`
	GameID                string      `json:"gameId"`
	RejectedAt            time.Time   `json:"rejectedAt"`
}

func NewWagerTransactionRejected(t *WagerTransaction) WagerTransactionRejected {
	ev := WagerTransactionRejected{
		TransactionID: t.ID(), WalletID: t.WalletID(), PlayerID: t.PlayerID(),
		Kind: t.Kind(), Money: t.Money(), FailureCode: t.FailureCode(), RejectedAt: t.UpdatedAt(),
	}
	if e := t.External(); e != nil {
		ev.ProviderID, ev.ExternalTransactionID = e.ProviderID, e.ExternalTransactionID
		ev.RoundID, ev.GameID = e.RoundID, e.GameID
	}
	return ev
}

func (WagerTransactionRejected) EventType() string        { return EventWagerTransactionRejected }
func (WagerTransactionRejected) EventVersion() int        { return 1 }
func (e WagerTransactionRejected) AggregateID() uuid.UUID { return e.WalletID }

// WalletBalanceChanged: o saldo mudou. Sai sempre do lançamento de ledger.
type WalletBalanceChanged struct {
	WalletID      uuid.UUID   `json:"walletId"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Direction     Direction   `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

func NewWalletBalanceChanged(e LedgerEntry) WalletBalanceChanged {
	return WalletBalanceChanged{
		WalletID: e.WalletID(), TransactionID: e.TransactionID(), Direction: e.Direction(),
		Money: e.Amount(), BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(),
		WalletVersion: e.WalletVersion(),
	}
}

func (WalletBalanceChanged) EventType() string        { return EventWalletBalanceChanged }
func (WalletBalanceChanged) EventVersion() int        { return 1 }
func (e WalletBalanceChanged) AggregateID() uuid.UUID { return e.WalletID }

// WagerTransactionPendingReference: a operação entrou em espera.
type WagerTransactionPendingReference struct {
	TransactionID                  uuid.UUID   `json:"transactionId"`
	WalletID                       uuid.UUID   `json:"walletId"`
	PlayerID                       uuid.UUID   `json:"playerId"`
	Kind                           Kind        `json:"kind"`
	Money                          money.Money `json:"money"`
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId"`
	ExpiresAt                      time.Time   `json:"expiresAt"`
}

func NewWagerTransactionPendingReference(t *WagerTransaction) WagerTransactionPendingReference {
	ev := WagerTransactionPendingReference{
		TransactionID: t.ID(), WalletID: t.WalletID(), PlayerID: t.PlayerID(),
		Kind: t.Kind(), Money: t.Money(),
	}
	if e := t.External(); e != nil {
		ev.ProviderID, ev.ExternalTransactionID = e.ProviderID, e.ExternalTransactionID
		ev.ReferenceExternalTransactionID = e.ReferenceExternalTransactionID
	}
	if x := t.ExpiresAt(); x != nil {
		ev.ExpiresAt = *x
	}
	return ev
}

func (WagerTransactionPendingReference) EventType() string {
	return EventWagerTransactionPendingReference
}
func (WagerTransactionPendingReference) EventVersion() int { return 1 }
func (e WagerTransactionPendingReference) AggregateID() uuid.UUID {
	return e.WalletID
}
