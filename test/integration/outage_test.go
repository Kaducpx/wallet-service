//go:build integration

package integration

import (
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Indisponibilidade temporária das dependências. O container é congelado
// com "docker pause": o processo para de responder sem recusar conexões, o
// pior caso para quem chama (nada falha rápido, tudo fica esperando).

var (
	pgContainer  = envOr("IT_PG_CONTAINER", "wallet-service-postgres-1")
	sqsContainer = envOr("IT_SQS_CONTAINER", "wallet-service-localstack-1")
)

// pause congela o container e garante o descongelamento ao fim do teste,
// mesmo em caso de falha. Devolve a função para descongelar antes.
func pause(t *testing.T, container string) (unpause func()) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available")
	}
	if out, err := exec.Command("docker", "pause", container).CombinedOutput(); err != nil {
		t.Skipf("cannot pause %s: %v %s", container, err, out)
	}
	done := false
	unpause = func() {
		if !done {
			done = true
			_ = exec.Command("docker", "unpause", container).Run()
		}
	}
	t.Cleanup(unpause)
	return unpause
}

// PostgreSQL fora do ar: a API responde 503 dentro do prazo (sem efeito
// financeiro), o readiness acusa a falha, a mensagem SQS fica para nova
// tentativa e, quando o banco volta, tudo é processado exatamente uma vez.
func TestPostgresOutage(t *testing.T) {
	q := newQueues(t, 10, 5)
	cfg := testConfig(q)
	cfg.HTTPRequestTimeout = 3 * time.Second
	app := startApp(t, cfg)
	w, p := openWallet(t, app.base, "100.00")
	httpBet := op("provider-a", extID("bet"), p, w, "BET", "10.00", "")
	sqsBet := op("provider-a", extID("bet"), p, w, "BET", "20.00", "")
	msgID := "msg-" + uuid.NewString()
	tok := token(t, "provider-a") // obtido antes: o IdP não depende do Postgres, mas evita ruído

	unpause := pause(t, pgContainer)

	// HTTP: 503 com Retry-After, dentro do prazo configurado.
	start := time.Now()
	r := call(t, http.MethodPost, app.base+"/wagering/transactions", tok,
		map[string]string{"Idempotency-Key": "provider-a:" + httpBet["externalTransactionId"].(string)}, httpBet)
	if r.Status != http.StatusServiceUnavailable || r.str("error", "code") != "SERVICE_UNAVAILABLE" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 503 with Retry-After, got %d %s", r.Status, r.Raw)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("request took %s; it must fail within the request timeout", elapsed)
	}
	// Readiness acusa o banco.
	ready := call(t, http.MethodGet, app.base+"/health/ready", "", nil, nil)
	if ready.Status != http.StatusServiceUnavailable || ready.str("checks", "postgres") != "DOWN" {
		t.Fatalf("readiness during outage: %d %s", ready.Status, ready.Raw)
	}
	// SQS: a mensagem chega com o banco fora; o consumidor tenta e falha.
	sendWagerMessage(t, q, msgID, sqsData(sqsBet), msgID)
	time.Sleep(5 * time.Second)

	unpause()

	eventually(t, 30*time.Second, func() bool {
		return call(t, http.MethodGet, app.base+"/health/ready", "", nil, nil).Status == http.StatusOK
	}, "readiness recovered")
	// O cliente HTTP repete a mesma operação: processa uma única vez.
	r = submit(t, app.base, "provider-a", httpBet)
	if r.str("status") != "PROCESSED" {
		t.Fatalf("retry after outage: %d %s", r.Status, r.Raw)
	}
	// A mensagem SQS é reprocessada após o backoff.
	eventually(t, 40*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msgID) == 1
	}, "sqs message processed after recovery")
	if v := metricValue(t, app.base, "wallet_sqs_retries_total"); v < 1 {
		t.Fatalf("expected at least one SQS retry, got %v", v)
	}
	// Exatamente um débito de cada operação; eventos publicados.
	if bal, _ := walletState(t, w); bal != 7000 {
		t.Fatalf("balance = %d, want 7000", bal)
	}
	if _, debits := ledgerEntries(t, w); debits != 2 {
		t.Fatalf("debits = %d, want 2", debits)
	}
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w) == 0
	}, "outbox drained after recovery")
	assertConsistent(t, app.base, w)
}

// SQS fora do ar: operações HTTP continuam (não dependem da fila), os
// eventos ficam guardados na outbox e são publicados quando o SQS volta.
func TestSQSOutage(t *testing.T) {
	q := newQueues(t, 5, 5)
	app := startApp(t, testConfig(q))
	w, p := openWallet(t, app.base, "100.00")
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w) == 0
	}, "opening events published before the outage")

	unpause := pause(t, sqsContainer)

	r := submit(t, app.base, "provider-a", op("provider-a", extID("bet"), p, w, "BET", "10.00", ""))
	if r.Status != http.StatusCreated {
		t.Fatalf("HTTP must keep working without SQS: %d %s", r.Status, r.Raw)
	}
	ready := call(t, http.MethodGet, app.base+"/health/ready", "", nil, nil)
	if ready.Status != http.StatusServiceUnavailable || ready.str("checks", "sqs") != "DOWN" || ready.str("checks", "postgres") != "UP" {
		t.Fatalf("readiness during SQS outage: %d %s", ready.Status, ready.Raw)
	}
	time.Sleep(3 * time.Second)
	if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w); n != 2 {
		t.Fatalf("events must wait in the outbox during the outage, pending = %d", n)
	}

	unpause()

	eventually(t, 60*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w) == 0
	}, "outbox drained after SQS recovered")
	eventually(t, 30*time.Second, func() bool {
		return call(t, http.MethodGet, app.base+"/health/ready", "", nil, nil).Status == http.StatusOK
	}, "readiness recovered")

	// O consumidor também se recupera.
	msgID := "msg-" + uuid.NewString()
	sendWagerMessage(t, q, msgID, sqsData(op("provider-a", extID("bet"), p, w, "BET", "5.00", "")), msgID)
	eventually(t, 40*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msgID) == 1
	}, "consumer recovered")
	assertConsistent(t, app.base, w)
}
