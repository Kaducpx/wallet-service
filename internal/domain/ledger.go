package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain/money"
)

// Direction diz se o lançamento tira (DEBIT) ou põe (CREDIT) dinheiro.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

func (d Direction) Valid() bool { return d == Debit || d == Credit }

// LedgerEntry é uma linha do extrato da carteira. Nunca muda depois de
// criada: corrigir algo exige um lançamento novo. O banco bloqueia UPDATE,
// DELETE e TRUNCATE na tabela.
//
// Somando créditos e subtraindo débitos chega-se ao saldo armazenado. É isso
// que a reconciliação confere.
type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID // operação que gerou o lançamento
	direction     Direction
	amount        money.Money // sempre positivo; a direção dá o sinal
	balanceBefore money.Money
	balanceAfter  money.Money
	walletVersion int64 // versão que a carteira passa a ter
	createdAt     time.Time
}

// LedgerEntryParams agrupa os campos para criar ou reidratar um lançamento.
type LedgerEntryParams struct {
	ID            uuid.UUID
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	WalletVersion int64
	CreatedAt     time.Time
}

// NewLedgerEntry só aceita lançamentos cuja conta fecha:
// depois = antes + valor (CREDIT) ou antes - valor (DEBIT).
// O banco tem um CHECK com a mesma regra.
func NewLedgerEntry(p LedgerEntryParams) (LedgerEntry, error) {
	bad := func(msg string) (LedgerEntry, error) {
		return LedgerEntry{}, fmt.Errorf("%w: %s", ErrInvalidLedgerEntry, msg)
	}

	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.TransactionID == uuid.Nil {
		return bad("ids are required")
	}
	if !p.Direction.Valid() {
		return bad("invalid direction")
	}
	if !p.Amount.IsPositive() {
		return bad("amount must be positive")
	}
	if p.WalletVersion < 1 {
		return bad("wallet version must be >= 1")
	}
	if p.CreatedAt.IsZero() {
		return bad("createdAt is required")
	}

	// Refaz a conta e compara com o saldo final informado.
	var expected money.Money
	var err error
	if p.Direction == Credit {
		expected, err = p.BalanceBefore.Add(p.Amount)
	} else {
		expected, err = p.BalanceBefore.Sub(p.Amount)
	}
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %v", ErrInvalidLedgerEntry, err)
	}
	if !expected.Equal(p.BalanceAfter) {
		return bad(fmt.Sprintf("balanceAfter %s != expected %s", p.BalanceAfter, expected))
	}

	if p.BalanceAfter.IsNegative() || p.BalanceBefore.IsNegative() {
		return bad("balances cannot be negative")
	}
	return LedgerEntry{
		id: p.ID, walletID: p.WalletID, transactionID: p.TransactionID,
		direction: p.Direction, amount: p.Amount,
		balanceBefore: p.BalanceBefore, balanceAfter: p.BalanceAfter,
		walletVersion: p.WalletVersion, createdAt: p.CreatedAt.UTC(),
	}, nil
}

// RehydrateLedgerEntry carrega um lançamento do banco com as mesmas
// validações: lançamento inconsistente é erro, não dado.
func RehydrateLedgerEntry(p LedgerEntryParams) (LedgerEntry, error) { return NewLedgerEntry(p) }

func (e LedgerEntry) ID() uuid.UUID              { return e.id }
func (e LedgerEntry) WalletID() uuid.UUID        { return e.walletID }
func (e LedgerEntry) TransactionID() uuid.UUID   { return e.transactionID }
func (e LedgerEntry) Direction() Direction       { return e.direction }
func (e LedgerEntry) Amount() money.Money        { return e.amount }
func (e LedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e LedgerEntry) WalletVersion() int64       { return e.walletVersion }
func (e LedgerEntry) CreatedAt() time.Time       { return e.createdAt }
