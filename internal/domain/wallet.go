package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain/money"
)

// Wallet é a raiz do agregado: todo dinheiro passa por ela. O saldo só muda
// via Debit/Credit, e cada mudança devolve o lançamento de ledger que a
// explica. Não tem como alterar saldo sem gerar lançamento.
//
// Os campos são privados para ninguém de fora mexer no saldo direto.
//
// Concorrência: esta struct é só a cópia em memória de uma linha travada com
// SELECT ... FOR UPDATE. O UPDATE ainda confere a versão lida, então uma
// escrita concorrente nunca se perde.
type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	currency  money.Currency // fixa; toda movimentação usa a mesma
	balance   money.Money    // nunca negativo
	version   int64          // começa em 1 e sobe a cada mudança de saldo
	createdAt time.Time
	updatedAt time.Time
}

// OpenWalletParams são os dados para abrir uma carteira.
type OpenWalletParams struct {
	WalletID       uuid.UUID
	PlayerID       uuid.UUID
	InitialBalance money.Money // pode ser zero
	// Só usados quando o saldo inicial é positivo.
	OpeningTransactionID uuid.UUID
	OpeningEntryID       uuid.UUID
	Now                  time.Time
}

// OpenedWallet é tudo que a abertura gera. Precisa ir para o banco num único
// commit (quem faz isso é o WalletService.Open).
type OpenedWallet struct {
	Wallet  *Wallet
	Opening *WagerTransaction // nil se o saldo inicial for zero
	Entry   *LedgerEntry      // nil se o saldo inicial for zero
	Events  []DomainEvent     // vazio se o saldo inicial for zero
}

// OpenWallet cria a carteira na versão 1.
//
// Com saldo inicial positivo, gera também a transação OPENING, o lançamento
// 0,00 -> saldo inicial (versão 1) e os eventos Processed e BalanceChanged.
// Com saldo zero, só a carteira.
func OpenWallet(p OpenWalletParams) (OpenedWallet, error) {
	if p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return OpenedWallet{}, fmt.Errorf("%w: wallet and player ids are required", ErrInvalidWallet)
	}
	if !p.InitialBalance.IsInitialized() {
		return OpenedWallet{}, fieldErr("initialBalance", money.ErrUninitialized)
	}
	if p.InitialBalance.IsNegative() {
		return OpenedWallet{}, fieldErr("initialBalance", money.ErrNegativeAmount)
	}
	if p.Now.IsZero() {
		return OpenedWallet{}, fmt.Errorf("%w: timestamp is required", ErrInvalidWallet)
	}

	now := p.Now.UTC()
	w := &Wallet{
		id: p.WalletID, playerID: p.PlayerID,
		currency: p.InitialBalance.Currency(), balance: p.InitialBalance,
		version: 1, createdAt: now, updatedAt: now,
	}
	out := OpenedWallet{Wallet: w}

	if p.InitialBalance.IsZero() {
		return out, nil
	}

	// O crédito inicial é registrado como qualquer outro: transação + lançamento.
	opening, err := NewOpeningTransaction(OpeningParams{
		ID: p.OpeningTransactionID, WalletID: w.id, PlayerID: w.playerID,
		Money: p.InitialBalance, Now: now,
	})
	if err != nil {
		return OpenedWallet{}, err
	}
	entry, err := NewLedgerEntry(LedgerEntryParams{
		ID: p.OpeningEntryID, WalletID: w.id, TransactionID: opening.ID(),
		Direction: Credit, Amount: p.InitialBalance,
		BalanceBefore: money.Zero(w.currency), BalanceAfter: p.InitialBalance,
		WalletVersion: 1, CreatedAt: now,
	})
	if err != nil {
		return OpenedWallet{}, err
	}
	if err := opening.MarkProcessed(p.InitialBalance, nil, now); err != nil {
		return OpenedWallet{}, err
	}
	out.Opening = opening
	out.Entry = &entry
	out.Events = []DomainEvent{
		NewWagerTransactionProcessed(opening),
		NewWalletBalanceChanged(entry),
	}
	return out, nil
}

// WalletSnapshot é a carteira como está no banco.
type WalletSnapshot struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RehydrateWallet carrega uma carteira do banco. Diferente de OpenWallet, não
// gera transação nem evento: só valida e devolve o estado. (Se gerasse, cada
// leitura publicaria eventos duplicados.)
func RehydrateWallet(s WalletSnapshot) (*Wallet, error) {
	// Registro inconsistente no banco vira erro, não dado confiável.
	switch {
	case s.ID == uuid.Nil || s.PlayerID == uuid.Nil:
		return nil, fmt.Errorf("%w: ids are required", ErrInvalidWallet)
	case !s.Balance.IsInitialized():
		return nil, fmt.Errorf("%w: balance is required", ErrInvalidWallet)
	case s.Balance.IsNegative():
		return nil, fmt.Errorf("%w: negative balance", ErrInvalidWallet)
	case s.Version < 1:
		return nil, fmt.Errorf("%w: version must be >= 1", ErrInvalidWallet)
	case s.CreatedAt.IsZero() || s.UpdatedAt.IsZero():
		return nil, fmt.Errorf("%w: timestamps are required", ErrInvalidWallet)
	}
	return &Wallet{
		id: s.ID, playerID: s.PlayerID, currency: s.Balance.Currency(),
		balance: s.Balance, version: s.Version,
		createdAt: s.CreatedAt.UTC(), updatedAt: s.UpdatedAt.UTC(),
	}, nil
}

func (w *Wallet) ID() uuid.UUID            { return w.id }
func (w *Wallet) PlayerID() uuid.UUID      { return w.playerID }
func (w *Wallet) Currency() money.Currency { return w.currency }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Version() int64           { return w.version }
func (w *Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time     { return w.updatedAt }

// Debit tira amount do saldo. Sem saldo suficiente: ErrInsufficientFunds.
func (w *Wallet) Debit(entryID, transactionID uuid.UUID, amount money.Money, now time.Time) (LedgerEntry, error) {
	return w.move(entryID, transactionID, Debit, amount, now)
}

// Credit soma amount ao saldo.
func (w *Wallet) Credit(entryID, transactionID uuid.UUID, amount money.Money, now time.Time) (LedgerEntry, error) {
	return w.move(entryID, transactionID, Credit, amount, now)
}

// move calcula o novo saldo e monta o lançamento antes de mexer no estado.
// Se algo falhar no caminho, a carteira fica como estava.
func (w *Wallet) move(entryID, txID uuid.UUID, dir Direction, amount money.Money, now time.Time) (LedgerEntry, error) {
	if !amount.IsInitialized() {
		return LedgerEntry{}, money.ErrUninitialized
	}
	if amount.Currency() != w.currency {
		return LedgerEntry{}, fmt.Errorf("%w: wallet %s, movement %s", money.ErrCurrencyMismatch, w.currency, amount.Currency())
	}
	if !amount.IsPositive() {
		return LedgerEntry{}, ErrNonPositiveAmount
	}

	var next money.Money
	var err error
	if dir == Credit {
		next, err = w.balance.Add(amount)
	} else {
		next, err = w.balance.Sub(amount)
	}
	if err != nil {
		return LedgerEntry{}, err
	}

	// Saldo nunca fica negativo (o banco tem um CHECK com a mesma regra).
	if next.IsNegative() {
		return LedgerEntry{}, ErrInsufficientFunds
	}

	entry, err := NewLedgerEntry(LedgerEntryParams{
		ID: entryID, WalletID: w.id, TransactionID: txID, Direction: dir,
		Amount: amount, BalanceBefore: w.balance, BalanceAfter: next,
		WalletVersion: w.version + 1, CreatedAt: now,
	})
	if err != nil {
		return LedgerEntry{}, err
	}

	// Tudo validado: agora sim altera o estado.
	w.balance = next
	w.version++
	w.updatedAt = now.UTC()
	return entry, nil
}
