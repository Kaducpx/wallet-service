package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var testPolicy = PendingPolicy{TTL: time.Minute, MaxAttempts: 5, BaseBackoff: time.Second, MaxBackoff: 10 * time.Second}

// settle chama domain.Settle com valores padrão e falha o teste em erro.
func settle(t *testing.T, w *Wallet, tx *WagerTransaction, ref ReferenceLookup) Settlement {
	t.Helper()
	s, err := Settle(SettleInput{Wallet: w, Tx: tx, Reference: ref, EntryID: uuid.New(), Policy: testPolicy, Now: t0})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	return s
}

// newExt cria uma operação externa válida em PENDING.
func newExt(t *testing.T, w *Wallet, kind Kind, amount, extID, ref string) *WagerTransaction {
	t.Helper()
	tx, err := NewExternalTransaction(extParams(t, w, kind, amount, extID, ref))
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// processed devolve uma transação já processada para servir de referência.
func processed(t *testing.T, w *Wallet, kind Kind, amount, extID, ref string) *WagerTransaction {
	t.Helper()
	tx := newExt(t, w, kind, amount, extID, ref)
	if err := tx.MarkProcessed(w.Balance(), nil, t0); err != nil {
		t.Fatal(err)
	}
	return tx
}

// eventTypes lista os tipos de evento produzidos, para comparar como texto.
func eventTypes(s Settlement) string {
	var out []string
	for _, e := range s.Events {
		out = append(out, e.EventType())
	}
	return strings.Join(out, ",")
}

// BET debita; uma segunda aposta sem saldo é rejeitada sem mexer na carteira.
func TestSettleBet(t *testing.T) {
	w := openWallet(t, "100.00")
	s := settle(t, w, newExt(t, w, KindBet, "80.00", "b1", ""), ReferenceLookup{})
	if s.Outcome != OutcomeProcessed || w.Balance().Amount() != "20.00" || s.Entry.Direction() != Debit {
		t.Fatalf("bet: %+v balance %s", s, w.Balance())
	}
	if eventTypes(s) != "WagerTransactionProcessed,WalletBalanceChanged" {
		t.Fatalf("events: %s", eventTypes(s))
	}
	// Segunda aposta de 80 sobre saldo 20: rejeitada, saldo intacto.
	tx := newExt(t, w, KindBet, "80.00", "b2", "")
	s = settle(t, w, tx, ReferenceLookup{})
	if s.Outcome != OutcomeRejected || tx.FailureCode() != FailureInsufficientFunds || s.Entry != nil {
		t.Fatalf("second bet: %+v", s)
	}
	if w.Balance().Amount() != "20.00" || w.Version() != 2 {
		t.Fatalf("wallet changed: %s v%d", w.Balance(), w.Version())
	}
	if eventTypes(s) != "WagerTransactionRejected" {
		t.Fatalf("events: %s", eventTypes(s))
	}
}

// WIN credita com ou sem referência; com referência, ela precisa ser uma BET.
func TestSettleWinWithAndWithoutReference(t *testing.T) {
	w := openWallet(t, "100.00")
	s := settle(t, w, newExt(t, w, KindWin, "30.00", "w1", ""), ReferenceLookup{})
	if s.Outcome != OutcomeProcessed || w.Balance().Amount() != "130.00" {
		t.Fatalf("win: %+v %s", s, w.Balance())
	}
	bet := processed(t, w, KindBet, "10.00", "b1", "")
	win := newExt(t, w, KindWin, "50.00", "w2", "b1")
	s = settle(t, w, win, ReferenceLookup{Tx: bet})
	if s.Outcome != OutcomeProcessed || *win.ReferenceTransactionID() != bet.ID() {
		t.Fatalf("win with ref: %+v", s)
	}
	// WIN que referencia algo que não é BET.
	other := processed(t, w, KindWin, "5.00", "w3", "")
	win = newExt(t, w, KindWin, "5.00", "w4", "w3")
	if s = settle(t, w, win, ReferenceLookup{Tx: other}); win.FailureCode() != FailureReferenceInvalidKind {
		t.Fatalf("win referencing win: %+v", s)
	}
}

// LOSS não gera lançamento nem muda a versão; emite só o evento Processed.
func TestSettleLossHasNoMovement(t *testing.T) {
	w := openWallet(t, "100.00")
	s := settle(t, w, newExt(t, w, KindLoss, "0.00", "l1", ""), ReferenceLookup{})
	if s.Outcome != OutcomeProcessed || s.Entry != nil || w.Version() != 1 {
		t.Fatalf("loss: %+v v%d", s, w.Version())
	}
	if eventTypes(s) != "WagerTransactionProcessed" {
		t.Fatalf("LOSS must emit only Processed: %s", eventTypes(s))
	}
}

// REFUND devolve a BET integralmente e uma única vez (REFUND ou ROLLBACK).
func TestSettleRefund(t *testing.T) {
	w := openWallet(t, "100.00")
	bet := processed(t, w, KindBet, "25.00", "b1", "")

	// Valor diferente do original.
	r := newExt(t, w, KindRefund, "10.00", "r0", "b1")
	if settle(t, w, r, ReferenceLookup{Tx: bet}); r.FailureCode() != FailureReferenceAmountMismatch {
		t.Fatalf("partial refund: %s", r.FailureCode())
	}
	// Refund válido.
	r = newExt(t, w, KindRefund, "25.00", "r1", "b1")
	s := settle(t, w, r, ReferenceLookup{Tx: bet})
	if s.Outcome != OutcomeProcessed || w.Balance().Amount() != "125.00" || s.Entry.Direction() != Credit {
		t.Fatalf("refund: %+v %s", s, w.Balance())
	}
	// Segunda reversão da mesma aposta.
	r = newExt(t, w, KindRefund, "25.00", "r2", "b1")
	if settle(t, w, r, ReferenceLookup{Tx: bet, AlreadyReversed: true}); r.FailureCode() != FailureReferenceAlreadyReversed {
		t.Fatalf("double refund: %s", r.FailureCode())
	}
	// ROLLBACK de uma aposta já reembolsada também é bloqueado (não devolve duas vezes).
	rb := newExt(t, w, KindRollback, "25.00", "rb1", "b1")
	if settle(t, w, rb, ReferenceLookup{Tx: bet, AlreadyReversed: true}); rb.FailureCode() != FailureReferenceAlreadyReversed {
		t.Fatalf("rollback after refund: %s", rb.FailureCode())
	}
	// REFUND de algo que não é BET.
	win := processed(t, w, KindWin, "5.00", "w1", "")
	r = newExt(t, w, KindRefund, "5.00", "r3", "w1")
	if settle(t, w, r, ReferenceLookup{Tx: win}); r.FailureCode() != FailureReferenceInvalidKind {
		t.Fatalf("refund of win: %s", r.FailureCode())
	}
}

// ROLLBACK faz o movimento contrário; sem saldo para desfazer um WIN, a
// rejeição tem código próprio (REVERSAL_INSUFFICIENT_FUNDS).
func TestSettleRollback(t *testing.T) {
	w := openWallet(t, "100.00")
	// ROLLBACK de BET credita.
	bet := processed(t, w, KindBet, "40.00", "b1", "")
	rb := newExt(t, w, KindRollback, "40.00", "rb1", "b1")
	if s := settle(t, w, rb, ReferenceLookup{Tx: bet}); s.Entry.Direction() != Credit || w.Balance().Amount() != "140.00" {
		t.Fatalf("rollback bet: %+v %s", s, w.Balance())
	}
	// ROLLBACK de WIN debita.
	win := processed(t, w, KindWin, "30.00", "w1", "")
	rb = newExt(t, w, KindRollback, "30.00", "rb2", "w1")
	if s := settle(t, w, rb, ReferenceLookup{Tx: win}); s.Entry.Direction() != Debit || w.Balance().Amount() != "110.00" {
		t.Fatalf("rollback win: %+v %s", s, w.Balance())
	}
	// ROLLBACK de WIN sem saldo: código próprio, diferente de INSUFFICIENT_FUNDS.
	bigWin := processed(t, w, KindWin, "500.00", "w2", "")
	rb = newExt(t, w, KindRollback, "500.00", "rb3", "w2")
	if s := settle(t, w, rb, ReferenceLookup{Tx: bigWin}); s.Outcome != OutcomeRejected || rb.FailureCode() != FailureReversalInsufficientFunds {
		t.Fatalf("rollback without funds: %+v %s", s, rb.FailureCode())
	}
	if w.Balance().Amount() != "110.00" {
		t.Fatalf("balance changed: %s", w.Balance())
	}
	// ROLLBACK de LOSS não é permitido.
	loss := processed(t, w, KindLoss, "0.00", "l1", "")
	rb = newExt(t, w, KindRollback, "1.00", "rb4", "l1")
	if settle(t, w, rb, ReferenceLookup{Tx: loss}); rb.FailureCode() != FailureReferenceInvalidKind {
		t.Fatalf("rollback of loss: %s", rb.FailureCode())
	}
}

// Referência de outra carteira, outra rodada ou já rejeitada é recusada.
func TestSettleReferenceMismatch(t *testing.T) {
	w := openWallet(t, "100.00")
	other := openWallet(t, "100.00")
	betOther := processed(t, other, KindBet, "10.00", "b1", "")
	r := newExt(t, w, KindRefund, "10.00", "r1", "b1")
	if settle(t, w, r, ReferenceLookup{Tx: betOther}); r.FailureCode() != FailureReferenceMismatch {
		t.Fatalf("other wallet: %s", r.FailureCode())
	}
	// Mesma carteira, rodada diferente.
	p := extParams(t, w, KindBet, "10.00", "b2", "")
	p.External.RoundID = "round-2"
	bet, _ := NewExternalTransaction(p)
	_ = bet.MarkProcessed(w.Balance(), nil, t0)
	r = newExt(t, w, KindRefund, "10.00", "r2", "b2")
	if settle(t, w, r, ReferenceLookup{Tx: bet}); r.FailureCode() != FailureReferenceMismatch {
		t.Fatalf("other round: %s", r.FailureCode())
	}
	// Referência rejeitada.
	rejected := newExt(t, w, KindBet, "10.00", "b3", "")
	bal := w.Balance()
	_ = rejected.MarkRejected(FailureInsufficientFunds, &bal, nil, t0)
	r = newExt(t, w, KindRefund, "10.00", "r3", "b3")
	if settle(t, w, r, ReferenceLookup{Tx: rejected}); r.FailureCode() != FailureReferenceNotProcessed {
		t.Fatalf("rejected ref: %s", r.FailureCode())
	}
}

// Operação de outro jogador ou em outra moeda é rejeitada.
func TestSettleWalletMismatch(t *testing.T) {
	w := openWallet(t, "100.00")
	p := extParams(t, w, KindBet, "10.00", "b1", "")
	p.PlayerID = uuid.New()
	tx, _ := NewExternalTransaction(p)
	if settle(t, w, tx, ReferenceLookup{}); tx.FailureCode() != FailureWalletPlayerMismatch {
		t.Fatalf("player: %s", tx.FailureCode())
	}
	p = extParams(t, w, KindBet, "10.00", "b2", "")
	p.Money = mustMoney(t, "10.00", "USD")
	tx, _ = NewExternalTransaction(p)
	if settle(t, w, tx, ReferenceLookup{}); tx.FailureCode() != FailureWalletCurrencyMismatch {
		t.Fatalf("currency: %s", tx.FailureCode())
	}
}

// Referência ausente: entra em espera, agenda retries e processa quando chega.
func TestSettlePendingReferenceLifecycle(t *testing.T) {
	w := openWallet(t, "100.00")
	r := newExt(t, w, KindRefund, "10.00", "r1", "b-late")

	// Referência ausente: entra em espera e emite o evento.
	s := settle(t, w, r, ReferenceLookup{})
	if s.Outcome != OutcomeAwaitingReference || r.Status() != StatusPendingReference ||
		eventTypes(s) != "WagerTransactionPendingReference" {
		t.Fatalf("pending: %+v", s)
	}
	// Ainda ausente: agenda nova tentativa, sem evento.
	s = settle(t, w, r, ReferenceLookup{})
	if s.Outcome != OutcomeRetryScheduled || r.Attempts() != 1 || len(s.Events) != 0 {
		t.Fatalf("retry: %+v", s)
	}
	// A referência chega: processa.
	bet := processed(t, w, KindBet, "10.00", "b-late", "")
	s = settle(t, w, r, ReferenceLookup{Tx: bet})
	if s.Outcome != OutcomeProcessed || w.Balance().Amount() != "110.00" {
		t.Fatalf("resolved: %+v", s)
	}
}

// Depois do TTL, a espera termina em REJECTED com REFERENCE_NOT_FOUND.
func TestSettlePendingReferenceExpires(t *testing.T) {
	w := openWallet(t, "100.00")
	r := newExt(t, w, KindRollback, "10.00", "rb1", "never")
	settle(t, w, r, ReferenceLookup{})
	// Depois do TTL: REJECTED com REFERENCE_NOT_FOUND.
	s, err := Settle(SettleInput{Wallet: w, Tx: r, Policy: testPolicy, Now: t0.Add(2 * time.Minute), EntryID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if s.Outcome != OutcomeRejected || r.FailureCode() != FailureReferenceNotFound || eventTypes(s) != "WagerTransactionRejected" {
		t.Fatalf("expired: %+v %s", s, r.FailureCode())
	}
}

// Se a própria referência ainda está pendente, a operação também espera.
func TestSettleWaitsWhileReferenceIsPending(t *testing.T) {
	w := openWallet(t, "100.00")
	// A referência (um REFUND) ainda espera a própria referência.
	refund := newExt(t, w, KindRefund, "10.00", "r1", "b1")
	settle(t, w, refund, ReferenceLookup{})
	rb := newExt(t, w, KindRollback, "10.00", "rb1", "r1")
	if s := settle(t, w, rb, ReferenceLookup{Tx: refund}); s.Outcome != OutcomeAwaitingReference {
		t.Fatalf("must wait for pending reference: %+v", s)
	}
}

// O envelope publicado tem todos os campos do contrato, em UTC e RFC 3339.
func TestEnvelopeSerialization(t *testing.T) {
	w := openWallet(t, "100.00")
	entry, _ := w.Debit(uuid.New(), uuid.New(), mustMoney(t, "25.00", "BRL"), t0)
	ev := NewWalletBalanceChanged(entry)
	env, err := NewEnvelope(uuid.New(), ev, "corr-1", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(env)
	s := string(b)
	for _, want := range []string{
		`"eventType":"WalletBalanceChanged"`, `"version":1`, `"occurredAt":"2026-09-08T12:00:00Z"`,
		`"balanceBefore":{"amount":"100.00","currency":"BRL"}`, `"balanceAfter":{"amount":"75.00","currency":"BRL"}`,
		`"direction":"DEBIT"`, `"walletVersion":2`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("envelope missing %s: %s", want, s)
		}
	}
	if strings.Contains(s, "causationId") {
		t.Error("causationId is optional and must be omitted when empty")
	}
	if _, err := NewEnvelope(uuid.Nil, ev, "c", "", t0); err == nil {
		t.Error("eventId is required")
	}
}
