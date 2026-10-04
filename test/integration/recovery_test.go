//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/platform/faults"
)

// Cenários com processos independentes (memória e conexões próprias).

// Três instâncias: a disputa 80/80, 50 envios duplicados e carteiras
// distintas, com as requisições espalhadas entre os processos.
func TestThreeInstances(t *testing.T) {
	q := newQueues(t, 5, 5)
	var procs []*proc
	for i := 0; i < 3; i++ {
		cfg := testConfig(q)
		cfg.InstanceID = fmt.Sprintf("multi-%d", i+1)
		procs = append(procs, startProc(t, cfg))
	}
	base := procs[0].base

	// 1) 80 + 80 sobre 100.00, cada aposta por uma instância diferente.
	w, p := openWallet(t, base, "100.00")
	bets := []map[string]any{
		op("provider-a", extID("bet"), p, w, "BET", "80.00", ""),
		op("provider-a", extID("bet"), p, w, "BET", "80.00", ""),
	}
	results := make([]response, 2)
	var start, wg sync.WaitGroup
	start.Add(1)
	for i := range bets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			results[i] = submit(t, procs[i].base, "provider-a", bets[i])
		}(i)
	}
	start.Done()
	wg.Wait()
	assertOneProcessedOneRejected(t, results)
	// Reenvio pela terceira instância.
	for _, b := range bets {
		if r := submit(t, procs[2].base, "provider-a", b); r.Body["idempotentReplay"] != true {
			t.Fatalf("resend must replay: %s", r.Raw)
		}
	}
	assertFinal20(t, base, w)

	// 2) Mesma aposta 50x em paralelo, distribuída entre as 3 instâncias.
	w2, p2 := openWallet(t, base, "100.00")
	same := op("provider-a", extID("bet"), p2, w2, "BET", "10.00", "")
	fresh := 0
	var mu sync.Mutex
	start = sync.WaitGroup{}
	start.Add(1)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			r := submit(t, procs[i%3].base, "provider-a", same)
			if r.str("status") != "PROCESSED" {
				t.Errorf("unexpected: %s", r.Raw)
			}
			if r.Body["idempotentReplay"] == false {
				mu.Lock()
				fresh++
				mu.Unlock()
			}
		}(i)
	}
	start.Done()
	wg.Wait()
	if _, debits := ledgerEntries(t, w2); debits != 1 || fresh != 1 {
		t.Fatalf("debits=%d fresh=%d", debits, fresh)
	}
	assertConsistent(t, base, w2)

	// 3) Carteiras distintas, operações por HTTP e SQS ao mesmo tempo,
	// consumidas pelas 3 instâncias.
	type wp struct{ w, p string }
	var ws []wp
	for i := 0; i < 6; i++ {
		w, p := openWallet(t, base, "100.00")
		ws = append(ws, wp{w, p})
	}
	var msgIDs []string
	for i, x := range ws {
		for j := 0; j < 5; j++ {
			body := op("provider-a", extID("bet"), x.p, x.w, "BET", "2.00", "")
			if j%2 == 0 {
				id := "msg-" + uuid.NewString()
				msgIDs = append(msgIDs, id)
				sendWagerMessage(t, q, id, sqsData(body), id)
				continue
			}
			wg.Add(1)
			go func(base string) {
				defer wg.Done()
				submit(t, base, "provider-a", body)
			}(procs[(i+j)%3].base)
		}
	}
	wg.Wait()
	eventually(t, 30*time.Second, func() bool {
		n := 0
		for _, id := range msgIDs {
			n += countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, id)
		}
		return n == len(msgIDs)
	}, "sqs messages processed by the instances")
	for _, x := range ws {
		if bal, _ := walletState(t, x.w); bal != 9000 {
			t.Fatalf("wallet %s balance = %d", x.w, bal)
		}
		assertConsistent(t, base, x.w)
	}
}

// Consumidor cai depois do commit e antes de remover a mensagem. Outra
// instância recebe a reentrega e a inbox impede o reprocessamento.
func TestConsumerCrashAfterCommitIsRedelivered(t *testing.T) {
	q := newQueues(t, 5, 3)
	cfg := testConfig(q)
	cfg.FaultPoints = "consumer.after_commit=exit"
	crashing := startProc(t, cfg)
	w, p := openWallet(t, crashing.base, "100.00")

	msgID := "msg-" + uuid.NewString()
	sendWagerMessage(t, q, msgID, sqsData(op("provider-a", extID("bet"), p, w, "BET", "30.00", "")), msgID)
	if code := crashing.waitExit(t, 30*time.Second); code != faults.ExitCode {
		t.Fatalf("expected injected crash, exit code %d", code)
	}
	// O commit aconteceu: inbox e débito já estão no banco.
	if n := countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msgID); n != 1 {
		t.Fatalf("inbox rows after crash = %d", n)
	}

	healthy := startProc(t, testConfig(q))
	// A mensagem volta após o visibility timeout e é removida sem efeito.
	eventually(t, 30*time.Second, func() bool {
		return metricValue(t, healthy.base, "wallet_duplicates_total", `kind="inbox"`) >= 1
	}, "redelivery recognized")
	if _, debits := ledgerEntries(t, w); debits != 1 {
		t.Fatalf("debits = %d", debits)
	}
	if bal, _ := walletState(t, w); bal != 7000 {
		t.Fatalf("balance = %d", bal)
	}
	// A fila ficou vazia (a mensagem foi removida pela segunda instância).
	time.Sleep(4 * time.Second)
	if left := drain(t, q.URL, 2*time.Second, nil); len(left) != 0 {
		t.Fatalf("message still in queue: %d", len(left))
	}
	assertConsistent(t, healthy.base, w)
}

// Queda entre o commit e a publicação: os eventos ficam na outbox e outra
// instância os publica.
func TestCrashBetweenCommitAndPublish(t *testing.T) {
	q := newQueues(t, 5, 5)
	cfg := testConfig(q)
	cfg.OutboxEnabled = false
	cfg.FaultPoints = "wager.after_commit=exit"
	crashing := startProc(t, cfg)
	w, p := openWallet(t, crashing.base, "100.00")

	body := op("provider-a", extID("bet"), p, w, "BET", "10.00", "")
	_, err := tryCall(http.MethodPost, crashing.base+"/wagering/transactions", token(t, "provider-a"),
		map[string]string{"Idempotency-Key": "provider-a:" + body["externalTransactionId"].(string)}, body)
	if err == nil {
		t.Fatal("expected the process to die before answering")
	}
	crashing.waitExit(t, 10*time.Second)
	unpublished := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w)
	if unpublished != 4 {
		t.Fatalf("unpublished events after crash = %d", unpublished)
	}

	// Outra instância assume; o cliente reenviando recebe replay.
	other := startProc(t, testConfig(q))
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w) == 0
	}, "events published by another instance")
	r := submit(t, other.base, "provider-a", body)
	if r.Body["idempotentReplay"] != true || r.str("balance", "amount") != "90.00" {
		t.Fatalf("client retry must replay: %s", r.Raw)
	}
	assertConsistent(t, other.base, w)
}

// Queda entre publicar e confirmar na outbox: o lease vence, outra instância
// republica com o MESMO eventId e confirma.
func TestCrashBetweenPublishAndConfirm(t *testing.T) {
	q := newQueues(t, 5, 5)
	producerCfg := testConfig(q)
	producerCfg.OutboxEnabled = false
	producer := startApp(t, producerCfg)
	w, _ := openWallet(t, producer.base, "100.00")
	producer.stop(t)

	var ids []string
	rows, _ := db.Query(t.Context(), `SELECT id::text FROM outbox_events WHERE aggregate_id=$1 ORDER BY occurred_at`, w)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	// Isolamento: eventos remanescentes de outros testes não podem ser os
	// primeiros da fila deste cenário.
	if _, err := db.Exec(t.Context(), `UPDATE outbox_events SET published_at = now(), locked_by = NULL, locked_until = NULL
		WHERE published_at IS NULL AND aggregate_id <> $1`, w); err != nil {
		t.Fatal(err)
	}

	crashCfg := testConfig(q)
	crashCfg.FaultPoints = "outbox.after_publish=exit"
	crashCfg.OutboxBatchSize = 1
	crashing := startProcNoWait(t, crashCfg)
	crashing.waitExit(t, 30*time.Second)

	// Um evento foi publicado mas não confirmado.
	var attempts int
	var lockedBy *string
	if err := db.QueryRow(t.Context(), `SELECT attempts, locked_by FROM outbox_events WHERE id=$1`, ids[0]).Scan(&attempts, &lockedBy); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lockedBy == nil {
		t.Fatalf("first event: attempts=%d locked_by=%v", attempts, lockedBy)
	}

	startApp(t, testConfig(q))
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w) == 0
	}, "recovered publication")
	if err := db.QueryRow(t.Context(), `SELECT attempts FROM outbox_events WHERE id=$1`, ids[0]).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("event must have been republished, attempts=%d", attempts)
	}
	got := map[string]bool{}
	for _, e := range envelopes(drain(t, q.EventsURL, 3*time.Second, nil)) {
		if e.AggregateID == w {
			got[e.EventID] = true
		}
	}
	for _, id := range ids {
		if !got[id] {
			t.Fatalf("event %s not delivered with its original id (got %v)", id, got)
		}
	}
}

// Reinício completo: idempotência, pendências e consistência sobrevivem.
func TestRestartPreservesState(t *testing.T) {
	q := newQueues(t, 5, 5)
	first := startProc(t, testConfig(q))
	w, p := openWallet(t, first.base, "100.00")
	bet := op("provider-a", extID("bet"), p, w, "BET", "30.00", "")
	original := submit(t, first.base, "provider-a", bet)
	lateBet := extID("bet")
	pending := submit(t, first.base, "provider-a", op("provider-a", extID("refund"), p, w, "REFUND", "20.00", lateBet))
	if pending.Status != http.StatusAccepted {
		t.Fatalf("pending: %s", pending.Raw)
	}
	first.kill() // queda abrupta

	second := startProc(t, testConfig(q))
	replay := submit(t, second.base, "provider-a", bet)
	if replay.Body["idempotentReplay"] != true || replay.str("transactionId") != original.str("transactionId") ||
		replay.str("balance", "amount") != "70.00" {
		t.Fatalf("replay after restart: %s", replay.Raw)
	}
	pendingID := pending.str("transactionId")
	q1 := call(t, http.MethodGet, second.base+"/wagering/transactions/"+pendingID, token(t, "provider-a"), nil, nil)
	if q1.str("status") != "PENDING_REFERENCE" {
		t.Fatalf("pending lost on restart: %s", q1.Raw)
	}
	submit(t, second.base, "provider-a", op("provider-a", lateBet, p, w, "BET", "20.00", ""))
	eventually(t, 15*time.Second, func() bool {
		r := call(t, http.MethodGet, second.base+"/wagering/transactions/"+pendingID, token(t, "provider-a"), nil, nil)
		return r.str("status") == "PROCESSED"
	}, "pending resumed by the new process")
	assertConsistent(t, second.base, w)
}
