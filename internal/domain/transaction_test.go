package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// extParams monta parâmetros válidos para uma operação externa.
func extParams(t *testing.T, w *Wallet, kind Kind, amount, extID, ref string) ExternalParams {
	t.Helper()
	return ExternalParams{
		ID: uuid.New(), Kind: kind, WalletID: w.ID(), PlayerID: w.PlayerID(),
		Money: mustMoney(t, amount, w.Currency().String()),
		External: ExternalInfo{
			ProviderID: "provider-a", ExternalTransactionID: extID,
			IdempotencyKey: "provider-a:" + extID, PayloadHash: "hash-" + extID,
			RoundID: "round-1", GameID: "game-1", ReferenceExternalTransactionID: ref,
		},
		Now: t0,
	}
}

// Só os cinco tipos externos são aceitos; OPENING tem erro próprio.
func TestParseExternalKind(t *testing.T) {
	for _, k := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		if _, err := ParseExternalKind(k); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	if _, err := ParseExternalKind("OPENING"); !errors.Is(err, ErrOpeningNotAllowed) {
		t.Errorf("OPENING: %v", err)
	}
	for _, k := range []string{"", "bet", "DEPOSIT"} {
		if _, err := ParseExternalKind(k); !errors.Is(err, ErrInvalidKind) {
			t.Errorf("%q: %v", k, err)
		}
	}
}

// Política de valor zero por tipo: LOSS exige 0.00; os demais exigem > 0.
func TestZeroAmountPolicyPerKind(t *testing.T) {
	w := openWallet(t, "100.00")
	cases := []struct {
		kind    Kind
		amount  string
		ref     string
		wantErr error
	}{
		{KindLoss, "0.00", "", nil},
		{KindLoss, "1.00", "", ErrInvalidAmountForKind},
		{KindBet, "0.00", "", ErrInvalidAmountForKind},
		{KindBet, "0.01", "", nil},
		{KindWin, "0.00", "", ErrInvalidAmountForKind},
		{KindWin, "5.00", "", nil},
		{KindRefund, "0.00", "bet-1", ErrInvalidAmountForKind},
		{KindRefund, "5.00", "bet-1", nil},
		{KindRollback, "0.00", "bet-1", ErrInvalidAmountForKind},
		{KindRollback, "5.00", "bet-1", nil},
	}
	for _, c := range cases {
		_, err := NewExternalTransaction(extParams(t, w, c.kind, c.amount, "tx-"+uuid.NewString(), c.ref))
		if c.wantErr == nil && err != nil {
			t.Errorf("%s %s: unexpected error %v", c.kind, c.amount, err)
		}
		if c.wantErr != nil && !errors.Is(err, c.wantErr) {
			t.Errorf("%s %s: expected %v, got %v", c.kind, c.amount, c.wantErr, err)
		}
	}
}

// Regras de referência por tipo e campos obrigatórios da operação externa.
func TestExternalTransactionValidation(t *testing.T) {
	w := openWallet(t, "100.00")

	p := extParams(t, w, KindRefund, "5.00", "r-1", "")
	if _, err := NewExternalTransaction(p); !errors.Is(err, ErrMissingReference) {
		t.Errorf("REFUND without reference: %v", err)
	}
	p = extParams(t, w, KindRollback, "5.00", "r-2", "")
	if _, err := NewExternalTransaction(p); !errors.Is(err, ErrMissingReference) {
		t.Errorf("ROLLBACK without reference: %v", err)
	}
	p = extParams(t, w, KindBet, "5.00", "b-1", "x")
	if _, err := NewExternalTransaction(p); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("BET with reference: %v", err)
	}
	p = extParams(t, w, KindOpening, "5.00", "o-1", "")
	if _, err := NewExternalTransaction(p); !errors.Is(err, ErrOpeningNotAllowed) {
		t.Errorf("external OPENING: %v", err)
	}
	p = extParams(t, w, KindBet, "5.00", "b-2", "")
	p.External.ProviderID = ""
	var fe *FieldError
	if _, err := NewExternalTransaction(p); !errors.As(err, &fe) || fe.Field != "providerId" {
		t.Errorf("missing provider: %v", err)
	}
	p = extParams(t, w, KindBet, "5.00", "b-3", "")
	if tx, err := NewExternalTransaction(p); err != nil || tx.Status() != StatusPending {
		t.Errorf("valid BET must start in PENDING: %v", err)
	}
}

// Transições permitidas funcionam e estados terminais não mudam mais.
func TestStateMachine(t *testing.T) {
	w := openWallet(t, "100.00")
	newTx := func() *WagerTransaction {
		tx, err := NewExternalTransaction(extParams(t, w, KindRefund, "5.00", "r-"+uuid.NewString(), "bet-x"))
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	bal := w.Balance()

	// PENDING -> PENDING_REFERENCE -> PROCESSED
	tx := newTx()
	if err := tx.MarkPendingReference(t0.Add(time.Second), t0.Add(time.Minute), t0); err != nil {
		t.Fatal(err)
	}
	if err := tx.ScheduleRetry(t0.Add(2*time.Second), t0); err != nil || tx.Attempts() != 1 {
		t.Fatalf("retry: %v attempts=%d", err, tx.Attempts())
	}
	if err := tx.MarkProcessed(bal, nil, t0); err != nil {
		t.Fatal(err)
	}
	// Estado terminal não aceita novas transições.
	if err := tx.MarkRejected(FailureInsufficientFunds, &bal, nil, t0); !errors.Is(err, ErrInvalidStateTransition) {
		t.Errorf("terminal -> REJECTED: %v", err)
	}
	if err := tx.MarkFailed(FailurePermanentProcessingError, t0); !errors.Is(err, ErrInvalidStateTransition) {
		t.Errorf("terminal -> FAILED: %v", err)
	}
	if err := tx.ScheduleRetry(t0, t0); !errors.Is(err, ErrInvalidStateTransition) {
		t.Errorf("retry on terminal: %v", err)
	}

	// PENDING -> REJECTED e PENDING -> FAILED
	tx = newTx()
	if err := tx.MarkRejected(FailureReferenceMismatch, &bal, nil, t0); err != nil || tx.FailureCode() != FailureReferenceMismatch {
		t.Fatal(err)
	}
	tx = newTx()
	if err := tx.MarkFailed(FailurePermanentProcessingError, t0); err != nil {
		t.Fatal(err)
	}
	// PENDING_REFERENCE não volta para PENDING_REFERENCE.
	tx = newTx()
	_ = tx.MarkPendingReference(t0.Add(time.Second), t0.Add(time.Minute), t0)
	if err := tx.MarkPendingReference(t0.Add(time.Second), t0.Add(time.Minute), t0); !errors.Is(err, ErrInvalidStateTransition) {
		t.Errorf("double pending: %v", err)
	}
	// Uma BET (sem referência) não pode esperar referência.
	bet, _ := NewExternalTransaction(extParams(t, w, KindBet, "1.00", "b-"+uuid.NewString(), ""))
	if err := bet.MarkPendingReference(t0.Add(time.Second), t0.Add(time.Minute), t0); !errors.Is(err, ErrInvalidStateTransition) {
		t.Errorf("BET pending reference: %v", err)
	}
}

// Reidratar não executa transições e recusa registros incoerentes.
func TestRehydrateTransactionDoesNotTransition(t *testing.T) {
	bal := mustMoney(t, "975.00", "BRL")
	tx, err := RehydrateTransaction(TransactionSnapshot{
		ID: uuid.New(), Origin: OriginExternal, Kind: KindBet, Status: StatusProcessed,
		WalletID: uuid.New(), PlayerID: uuid.New(), Money: mustMoney(t, "25.00", "BRL"),
		External:      &ExternalInfo{ProviderID: "p", ExternalTransactionID: "e", IdempotencyKey: "k", PayloadHash: "h", RoundID: "r", GameID: "g"},
		ResultBalance: &bal, CreatedAt: t0, UpdatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status() != StatusProcessed || tx.ResultBalance().Amount() != "975.00" {
		t.Fatalf("unexpected: %+v", tx)
	}
	// OPENING com metadados externos é inválido.
	_, err = RehydrateTransaction(TransactionSnapshot{
		ID: uuid.New(), Origin: OriginInternal, Kind: KindOpening, Status: StatusProcessed,
		WalletID: uuid.New(), PlayerID: uuid.New(), Money: bal,
		External: &ExternalInfo{ProviderID: "p"}, CreatedAt: t0, UpdatedAt: t0,
	})
	if !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("expected ErrInvalidTransaction, got %v", err)
	}
}
