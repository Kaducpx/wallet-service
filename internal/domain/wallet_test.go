package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"wallet-service/internal/domain/money"
)

// openWallet abre uma carteira em BRL com o saldo informado.
func openWallet(t *testing.T, initial string) *Wallet {
	t.Helper()
	out, err := OpenWallet(OpenWalletParams{
		WalletID: uuid.New(), PlayerID: uuid.New(),
		InitialBalance:       mustMoney(t, initial, "BRL"),
		OpeningTransactionID: uuid.New(), OpeningEntryID: uuid.New(), Now: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Wallet
}

// Abertura com saldo cria OPENING processado, lançamento 0 -> saldo na
// versão 1 e os dois eventos.
func TestOpenWalletWithPositiveBalance(t *testing.T) {
	out, err := OpenWallet(OpenWalletParams{
		WalletID: uuid.New(), PlayerID: uuid.New(),
		InitialBalance:       mustMoney(t, "1000.00", "BRL"),
		OpeningTransactionID: uuid.New(), OpeningEntryID: uuid.New(), Now: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := out.Wallet
	if w.Version() != 1 || w.Balance().Amount() != "1000.00" {
		t.Fatalf("wallet = v%d %s", w.Version(), w.Balance())
	}
	// OPENING processado, sem metadados externos.
	op := out.Opening
	if op == nil || op.Kind() != KindOpening || op.Status() != StatusProcessed ||
		op.Origin() != OriginInternal || op.External() != nil || op.ProviderID() != "" {
		t.Fatalf("unexpected opening transaction: %+v", op)
	}
	// Lançamento 0.00 -> 1000.00 na versão 1.
	e := out.Entry
	if e == nil || e.Direction() != Credit || e.BalanceBefore().Amount() != "0.00" ||
		e.BalanceAfter().Amount() != "1000.00" || e.WalletVersion() != 1 || e.TransactionID() != op.ID() {
		t.Fatalf("unexpected opening entry: %+v", e)
	}
	// Eventos: processado + saldo alterado.
	if len(out.Events) != 2 ||
		out.Events[0].EventType() != EventWagerTransactionProcessed ||
		out.Events[1].EventType() != EventWalletBalanceChanged {
		t.Fatalf("unexpected events: %+v", out.Events)
	}
	bc := out.Events[1].(WalletBalanceChanged)
	if bc.WalletVersion != 1 || bc.BalanceAfter.Amount() != "1000.00" {
		t.Fatalf("unexpected balance changed payload: %+v", bc)
	}
}

// Abertura com saldo zero não cria OPENING, ledger nem eventos.
func TestOpenWalletWithZeroBalanceHasNoOpening(t *testing.T) {
	out, err := OpenWallet(OpenWalletParams{
		WalletID: uuid.New(), PlayerID: uuid.New(),
		InitialBalance: money.Zero(brl), Now: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Opening != nil || out.Entry != nil || len(out.Events) != 0 || out.Wallet.Version() != 1 {
		t.Fatalf("zero balance must not create OPENING/ledger/events: %+v", out)
	}
}

// Abertura sem valor ou sem id é recusada.
func TestOpenWalletRejectsInvalidInput(t *testing.T) {
	if _, err := OpenWallet(OpenWalletParams{WalletID: uuid.New(), PlayerID: uuid.New(), Now: t0}); !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("uninitialized money: %v", err)
	}
	if _, err := OpenWallet(OpenWalletParams{PlayerID: uuid.New(), InitialBalance: money.Zero(brl), Now: t0}); !errors.Is(err, ErrInvalidWallet) {
		t.Errorf("missing id: %v", err)
	}
}

// Débito e crédito mudam saldo e versão e geram o lançamento correto.
func TestWalletDebitAndCredit(t *testing.T) {
	w := openWallet(t, "100.00")
	e, err := w.Debit(uuid.New(), uuid.New(), mustMoney(t, "80.00", "BRL"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance().Amount() != "20.00" || w.Version() != 2 {
		t.Fatalf("after debit: %s v%d", w.Balance(), w.Version())
	}
	if e.BalanceBefore().Amount() != "100.00" || e.BalanceAfter().Amount() != "20.00" || e.WalletVersion() != 2 {
		t.Fatalf("entry: %+v", e)
	}
	if _, err := w.Credit(uuid.New(), uuid.New(), mustMoney(t, "5.50", "BRL"), t0); err != nil {
		t.Fatal(err)
	}
	if w.Balance().Amount() != "25.50" || w.Version() != 3 {
		t.Fatalf("after credit: %s v%d", w.Balance(), w.Version())
	}
}

// Débito acima do saldo é recusado sem alterar nada; débito exato até zero
// é permitido.
func TestWalletDebitNeverGoesNegative(t *testing.T) {
	w := openWallet(t, "100.00")
	_, err := w.Debit(uuid.New(), uuid.New(), mustMoney(t, "100.01", "BRL"), t0)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	// Estado inalterado após a rejeição.
	if w.Balance().Amount() != "100.00" || w.Version() != 1 {
		t.Fatalf("wallet changed after rejection: %s v%d", w.Balance(), w.Version())
	}
	// Débito exato até zero é permitido.
	if _, err := w.Debit(uuid.New(), uuid.New(), mustMoney(t, "100.00", "BRL"), t0); err != nil {
		t.Fatal(err)
	}
	if !w.Balance().IsZero() {
		t.Fatal("balance must be zero")
	}
}

// Movimentação em outra moeda ou com valor zero é recusada.
func TestWalletRejectsCurrencyMismatchAndNonPositive(t *testing.T) {
	w := openWallet(t, "100.00")
	if _, err := w.Credit(uuid.New(), uuid.New(), mustMoney(t, "1.00", "USD"), t0); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("currency: %v", err)
	}
	if _, err := w.Debit(uuid.New(), uuid.New(), money.Zero(brl), t0); !errors.Is(err, ErrNonPositiveAmount) {
		t.Errorf("zero: %v", err)
	}
	if w.Version() != 1 {
		t.Error("version must not change on rejected movements")
	}
}

// Reidratar mantém o estado lido e recusa registros inválidos.
func TestRehydrateWalletDoesNotEmitOrChange(t *testing.T) {
	id, player := uuid.New(), uuid.New()
	w, err := RehydrateWallet(WalletSnapshot{
		ID: id, PlayerID: player, Balance: mustMoney(t, "975.00", "BRL"),
		Version: 7, CreatedAt: t0, UpdatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.Version() != 7 || w.Balance().Amount() != "975.00" {
		t.Fatalf("rehydrated: %s v%d", w.Balance(), w.Version())
	}
	neg, _ := money.FromUnits(-1, brl)
	for name, s := range map[string]WalletSnapshot{
		"negative": {ID: id, PlayerID: player, Balance: neg, Version: 1, CreatedAt: t0, UpdatedAt: t0},
		"version0": {ID: id, PlayerID: player, Balance: money.Zero(brl), Version: 0, CreatedAt: t0, UpdatedAt: t0},
		"noMoney":  {ID: id, PlayerID: player, Version: 1, CreatedAt: t0, UpdatedAt: t0},
		"noID":     {PlayerID: player, Balance: money.Zero(brl), Version: 1, CreatedAt: t0, UpdatedAt: t0},
	} {
		if _, err := RehydrateWallet(s); !errors.Is(err, ErrInvalidWallet) {
			t.Errorf("%s: expected ErrInvalidWallet, got %v", name, err)
		}
	}
}

// Um lançamento cuja conta não fecha é recusado.
func TestLedgerEntryValidatesArithmetic(t *testing.T) {
	p := LedgerEntryParams{
		ID: uuid.New(), WalletID: uuid.New(), TransactionID: uuid.New(), Direction: Debit,
		Amount: mustMoney(t, "10.00", "BRL"), BalanceBefore: mustMoney(t, "50.00", "BRL"),
		BalanceAfter: mustMoney(t, "40.00", "BRL"), WalletVersion: 2, CreatedAt: t0,
	}
	if _, err := NewLedgerEntry(p); err != nil {
		t.Fatal(err)
	}
	p.BalanceAfter = mustMoney(t, "60.00", "BRL") // seria um crédito
	if _, err := NewLedgerEntry(p); !errors.Is(err, ErrInvalidLedgerEntry) {
		t.Fatalf("expected ErrInvalidLedgerEntry, got %v", err)
	}
}
