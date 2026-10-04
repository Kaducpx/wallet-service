//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Abertura com saldo: OPENING PROCESSED + lançamento + 2 eventos no mesmo
// commit; saldo zero não cria nada disso; segunda abertura é conflito.
func TestOpenWallet(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))

	walletID, playerID := openWallet(t, app.base, "1000.00")
	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE wallet_id=$1 AND kind='OPENING' AND status='PROCESSED' AND origin='INTERNAL'`, walletID); n != 1 {
		t.Fatalf("OPENING rows = %d", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type IN ('WagerTransactionProcessed','WalletBalanceChanged')`, walletID); n != 2 {
		t.Fatalf("opening events = %d", n)
	}
	assertConsistent(t, app.base, walletID)

	// Mesmo jogador e moeda: conflito.
	r := call(t, http.MethodPost, app.base+"/wallets", token(t, "wallet-backoffice"), nil, map[string]any{
		"playerId": playerID, "initialBalance": map[string]string{"amount": "1.00", "currency": "BRL"},
	})
	if r.Status != http.StatusConflict || r.str("error", "code") != "WALLET_ALREADY_EXISTS" {
		t.Fatalf("duplicate wallet: %d %s", r.Status, r.Raw)
	}

	// Saldo zero: sem OPENING, sem ledger, sem eventos financeiros.
	zeroID, _ := openWallet(t, app.base, "0.00")
	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE wallet_id=$1`, zeroID); n != 0 {
		t.Fatalf("zero wallet transactions = %d", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1`, zeroID); n != 0 {
		t.Fatalf("zero wallet events = %d", n)
	}
	r = call(t, http.MethodGet, app.base+"/wallets/"+zeroID, token(t, "wallet-backoffice"), nil, nil)
	if r.str("balance", "amount") != "0.00" || r.str("version") != "1" {
		t.Fatalf("zero wallet: %s", r.Raw)
	}
	assertConsistent(t, app.base, zeroID)
}

// Cobre as regras dos cinco tipos externos ponta a ponta.
func TestOperationKinds(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	w, p := openWallet(t, app.base, "100.00")
	pa := "provider-a"

	// BET
	bet := extID("bet")
	r := submit(t, app.base, pa, op(pa, bet, p, w, "BET", "40.00", ""))
	if r.Status != http.StatusCreated || r.str("balance", "amount") != "60.00" {
		t.Fatalf("bet: %d %s", r.Status, r.Raw)
	}
	// WIN referenciando a aposta da rodada.
	win := extID("win")
	r = submit(t, app.base, pa, op(pa, win, p, w, "WIN", "30.00", bet))
	if r.Status != http.StatusCreated || r.str("balance", "amount") != "90.00" {
		t.Fatalf("win: %d %s", r.Status, r.Raw)
	}
	// LOSS: sem ledger e sem mudança de versão.
	_, versionBefore := walletState(t, w)
	r = submit(t, app.base, pa, op(pa, extID("loss"), p, w, "LOSS", "0.00", ""))
	if r.Status != http.StatusCreated || r.str("status") != "PROCESSED" {
		t.Fatalf("loss: %d %s", r.Status, r.Raw)
	}
	if _, v := walletState(t, w); v != versionBefore {
		t.Fatalf("LOSS changed version %d -> %d", versionBefore, v)
	}
	// LOSS com valor diferente de zero é entrada inválida (nada persistido).
	r = submit(t, app.base, pa, op(pa, extID("loss"), p, w, "LOSS", "1.00", ""))
	if r.Status != http.StatusBadRequest || r.str("error", "code") != "INVALID_AMOUNT_FOR_KIND" {
		t.Fatalf("loss nonzero: %d %s", r.Status, r.Raw)
	}
	// REFUND da aposta.
	refund := extID("refund")
	r = submit(t, app.base, pa, op(pa, refund, p, w, "REFUND", "40.00", bet))
	if r.Status != http.StatusCreated || r.str("balance", "amount") != "130.00" {
		t.Fatalf("refund: %d %s", r.Status, r.Raw)
	}
	// Segunda devolução da mesma aposta (REFUND ou ROLLBACK): rejeitada.
	r = submit(t, app.base, pa, op(pa, extID("refund"), p, w, "REFUND", "40.00", bet))
	if r.Status != http.StatusUnprocessableEntity || r.str("failureCode") != "REFERENCE_ALREADY_REVERSED" {
		t.Fatalf("double refund: %d %s", r.Status, r.Raw)
	}
	r = submit(t, app.base, pa, op(pa, extID("rb"), p, w, "ROLLBACK", "40.00", bet))
	if r.Status != http.StatusUnprocessableEntity || r.str("failureCode") != "REFERENCE_ALREADY_REVERSED" {
		t.Fatalf("rollback after refund: %d %s", r.Status, r.Raw)
	}
	// ROLLBACK do WIN (débito).
	r = submit(t, app.base, pa, op(pa, extID("rb"), p, w, "ROLLBACK", "30.00", win))
	if r.Status != http.StatusCreated || r.str("balance", "amount") != "100.00" {
		t.Fatalf("rollback win: %d %s", r.Status, r.Raw)
	}
	// ROLLBACK que debitaria mais que o saldo: código próprio e auditável.
	bigWin := extID("win")
	submit(t, app.base, pa, op(pa, bigWin, p, w, "WIN", "500.00", ""))
	submit(t, app.base, pa, op(pa, extID("bet"), p, w, "BET", "550.00", ""))
	rbID := extID("rb")
	r = submit(t, app.base, pa, op(pa, rbID, p, w, "ROLLBACK", "500.00", bigWin))
	if r.Status != http.StatusUnprocessableEntity || r.str("failureCode") != "REVERSAL_INSUFFICIENT_FUNDS" {
		t.Fatalf("rollback without funds: %d %s", r.Status, r.Raw)
	}
	q := call(t, http.MethodGet, app.base+"/providers/provider-a/wagering/transactions/"+rbID, token(t, pa), nil, nil)
	if q.str("status") != "REJECTED" || q.str("failureCode") != "REVERSAL_INSUFFICIENT_FUNDS" {
		t.Fatalf("rejected rollback must be auditable: %s", q.Raw)
	}
	// BET sem saldo: INSUFFICIENT_FUNDS (diferente do código da reversão).
	r = submit(t, app.base, pa, op(pa, extID("bet"), p, w, "BET", "999.00", ""))
	if r.str("failureCode") != "INSUFFICIENT_FUNDS" {
		t.Fatalf("bet without funds: %s", r.Raw)
	}
	// Reversão parcial não é aceita.
	bet2 := extID("bet")
	submit(t, app.base, pa, op(pa, bet2, p, w, "BET", "10.00", ""))
	r = submit(t, app.base, pa, op(pa, extID("refund"), p, w, "REFUND", "5.00", bet2))
	if r.str("failureCode") != "REFERENCE_AMOUNT_MISMATCH" {
		t.Fatalf("partial refund: %s", r.Raw)
	}
	// OPENING externo é rejeitado.
	r = submit(t, app.base, pa, op(pa, extID("op"), p, w, "OPENING", "10.00", ""))
	if r.Status != http.StatusBadRequest || r.str("error", "code") != "OPENING_NOT_ALLOWED" {
		t.Fatalf("external opening: %d %s", r.Status, r.Raw)
	}
	// Carteira de outro jogador: rejeição de negócio.
	r = submit(t, app.base, pa, op(pa, extID("bet"), "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", w, "BET", "1.00", ""))
	if r.str("failureCode") != "WALLET_PLAYER_MISMATCH" {
		t.Fatalf("player mismatch: %s", r.Raw)
	}
	// Valor monetário em formato inválido.
	body := op(pa, extID("bet"), p, w, "BET", "1.5", "")
	if r = submit(t, app.base, pa, body); r.Status != http.StatusBadRequest || r.str("error", "code") != "INVALID_MONEY" {
		t.Fatalf("invalid money: %d %s", r.Status, r.Raw)
	}
	assertConsistent(t, app.base, w)
}

// Idempotência persistente: replay devolve o resultado original (inclusive
// o saldo daquele momento); conflitos são distinguíveis.
func TestIdempotency(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	w, p := openWallet(t, app.base, "100.00")
	pa := "provider-a"
	ext := extID("bet")
	body := op(pa, ext, p, w, "BET", "25.00", "")

	first := submit(t, app.base, pa, body)
	submit(t, app.base, pa, op(pa, extID("bet"), p, w, "BET", "10.00", "")) // move o saldo depois

	replay := submit(t, app.base, pa, body)
	if replay.Status != http.StatusOK || replay.Body["idempotentReplay"] != true ||
		replay.str("transactionId") != first.str("transactionId") || replay.str("balance", "amount") != "75.00" {
		t.Fatalf("replay must return original result (75.00): %d %s", replay.Status, replay.Raw)
	}

	// Mesma chave, conteúdo diferente.
	changed := op(pa, ext, p, w, "BET", "26.00", "")
	r := submit(t, app.base, pa, changed)
	if r.Status != http.StatusConflict || r.str("error", "code") != "IDEMPOTENCY_KEY_CONFLICT" {
		t.Fatalf("key conflict: %d %s", r.Status, r.Raw)
	}
	// Mesma operação (provider, externalTransactionId) com outra chave.
	r = call(t, http.MethodPost, app.base+"/wagering/transactions", token(t, pa),
		map[string]string{"Idempotency-Key": "another-key-" + ext}, body)
	if r.Status != http.StatusConflict || r.str("error", "code") != "EXTERNAL_TRANSACTION_CONFLICT" {
		t.Fatalf("external id conflict: %d %s", r.Status, r.Raw)
	}
	// Sem chave.
	r = call(t, http.MethodPost, app.base+"/wagering/transactions", token(t, pa), nil, body)
	if r.Status != http.StatusBadRequest || r.str("error", "code") != "MISSING_IDEMPOTENCY_KEY" {
		t.Fatalf("missing key: %d %s", r.Status, r.Raw)
	}
	// Replay de rejeição também é estável.
	rej := op(pa, extID("bet"), p, w, "BET", "999.00", "")
	a, b := submit(t, app.base, pa, rej), submit(t, app.base, pa, rej)
	if a.Status != 422 || b.Status != 422 || b.Body["idempotentReplay"] != true || a.str("failureCode") != b.str("failureCode") {
		t.Fatalf("rejection replay: %s / %s", a.Raw, b.Raw)
	}
	if n, _ := ledgerEntries(t, w); n != 3 { // abertura + 2 apostas
		t.Fatalf("ledger entries = %d", n)
	}
	assertConsistent(t, app.base, w)
}

// Reversão antes da referência: fica PENDING_REFERENCE e é resolvida pelo
// worker quando a aposta chega; sem referência, expira em REJECTED.
func TestReversalBeforeReference(t *testing.T) {
	cfg := testConfig(newQueues(t, 5, 5))
	cfg.PendingTTL = 4 * time.Second
	app := startApp(t, cfg)
	w, p := openWallet(t, app.base, "100.00")
	pa := "provider-a"

	bet := extID("bet")
	refund := extID("refund")
	r := submit(t, app.base, pa, op(pa, refund, p, w, "REFUND", "20.00", bet))
	if r.Status != http.StatusAccepted || r.str("status") != "PENDING_REFERENCE" {
		t.Fatalf("early refund: %d %s", r.Status, r.Raw)
	}
	pendingID := r.str("transactionId")
	if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE event_type='WagerTransactionPendingReference' AND payload::jsonb->'data'->>'transactionId'=$1`, pendingID); n != 1 {
		t.Fatalf("pending event = %d", n)
	}

	// A aposta chega depois.
	if r = submit(t, app.base, pa, op(pa, bet, p, w, "BET", "20.00", "")); r.Status != http.StatusCreated {
		t.Fatalf("bet: %s", r.Raw)
	}
	eventually(t, 15*time.Second, func() bool {
		q := call(t, http.MethodGet, app.base+"/wagering/transactions/"+pendingID, token(t, pa), nil, nil)
		return q.str("status") == "PROCESSED"
	}, "pending refund processed")
	if bal, _ := walletState(t, w); bal != 10000 {
		t.Fatalf("balance after bet+refund = %d", bal)
	}

	// Referência que nunca chega: REFERENCE_NOT_FOUND após o TTL.
	r = submit(t, app.base, pa, op(pa, extID("rb"), p, w, "ROLLBACK", "5.00", "never-arrives"))
	lostID := r.str("transactionId")
	eventually(t, 20*time.Second, func() bool {
		q := call(t, http.MethodGet, app.base+"/wagering/transactions/"+lostID, token(t, pa), nil, nil)
		return q.str("status") == "REJECTED" && q.str("failureCode") == "REFERENCE_NOT_FOUND"
	}, "expired reversal rejected")
	if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE event_type='WagerTransactionRejected' AND payload::jsonb->'data'->>'transactionId'=$1`, lostID); n != 1 {
		t.Fatalf("rejection event = %d", n)
	}
	assertConsistent(t, app.base, w)
}

// Paginação do ledger com cursor opaco e ordem estável.
func TestLedgerPagination(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	w, p := openWallet(t, app.base, "100.00")
	for i := 0; i < 6; i++ {
		submit(t, app.base, "provider-a", op("provider-a", extID("bet"), p, w, "BET", "1.00", ""))
	}
	var versions []string
	cursor := ""
	for page := 0; page < 10; page++ {
		url := app.base + "/wallets/" + w + "/ledger?limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		r := call(t, http.MethodGet, url, token(t, "wallet-backoffice"), nil, nil)
		for _, e := range r.Body["entries"].([]any) {
			versions = append(versions, fmt.Sprint(e.(map[string]any)["walletVersion"]))
		}
		cursor = r.str("nextCursor")
		if cursor == "" {
			break
		}
	}
	want := []string{"1", "2", "3", "4", "5", "6", "7"}
	if len(versions) != len(want) {
		t.Fatalf("ledger versions = %v", versions)
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("ledger order = %v", versions)
		}
	}
	r := call(t, http.MethodGet, app.base+"/wallets/"+w+"/ledger?cursor=bad!", token(t, "wallet-backoffice"), nil, nil)
	if r.Status != http.StatusBadRequest {
		t.Fatalf("invalid cursor: %d", r.Status)
	}
}
