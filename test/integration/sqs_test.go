//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

// sqsData converte o corpo HTTP no campo data da mensagem SQS,
// acrescentando idempotencyKey.
func sqsData(body map[string]any) map[string]any {
	data := map[string]any{}
	for k, v := range body {
		data[k] = v
	}
	data["idempotencyKey"] = body["providerId"].(string) + ":" + body["externalTransactionId"].(string)
	return data
}

// Mensagem processada uma vez; reentrega do MESMO messageId (com outro
// MessageDeduplicationId, para passar pela deduplicação do FIFO) é
// reconhecida pela inbox da aplicação.
func TestSQSConsumerProcessesAndDeduplicates(t *testing.T) {
	q := newQueues(t, 5, 5)
	app := startApp(t, testConfig(q))
	w, p := openWallet(t, app.base, "100.00")
	body := op("provider-a", extID("bet"), p, w, "BET", "25.00", "")
	msgID := "msg-" + uuid.NewString()

	sendWagerMessage(t, q, msgID, sqsData(body), uuid.NewString())
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msgID) == 1
	}, "message processed")

	for i := 0; i < 3; i++ {
		sendWagerMessage(t, q, msgID, sqsData(body), uuid.NewString())
	}
	eventually(t, 20*time.Second, func() bool {
		return metricValue(t, app.base, "wallet_duplicates_total", `channel="sqs"`, `kind="inbox"`) >= 3
	}, "redeliveries recognized by the inbox")

	if n := countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msgID); n != 1 {
		t.Fatalf("inbox rows = %d", n)
	}
	if _, debits := ledgerEntries(t, w); debits != 1 {
		t.Fatalf("debits = %d", debits)
	}
	assertConsistent(t, app.base, w)
}

// HTTP e SQS compartilham a idempotência financeira: a mesma operação pelos
// dois canais (nas duas ordens) gera um único efeito.
func TestHTTPAndSQSSameOperation(t *testing.T) {
	q := newQueues(t, 5, 5)
	app := startApp(t, testConfig(q))
	w, p := openWallet(t, app.base, "100.00")

	// HTTP primeiro, depois SQS.
	body := op("provider-a", extID("bet"), p, w, "BET", "10.00", "")
	if r := submit(t, app.base, "provider-a", body); r.Status != http.StatusCreated {
		t.Fatal(r.Raw)
	}
	msg1 := "msg-" + uuid.NewString()
	sendWagerMessage(t, q, msg1, sqsData(body), msg1)
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msg1) == 1
	}, "sqs replay recorded in inbox")

	// SQS primeiro, depois HTTP.
	body2 := op("provider-a", extID("bet"), p, w, "BET", "15.00", "")
	msg2 := "msg-" + uuid.NewString()
	sendWagerMessage(t, q, msg2, sqsData(body2), msg2)
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msg2) == 1
	}, "sqs processed")
	r := submit(t, app.base, "provider-a", body2)
	if r.Status != http.StatusOK || r.Body["idempotentReplay"] != true {
		t.Fatalf("http after sqs must replay: %d %s", r.Status, r.Raw)
	}

	// Concorrência entre canais: a mesma operação chega pelos dois ao mesmo tempo.
	body3 := op("provider-a", extID("bet"), p, w, "BET", "5.00", "")
	msg3 := "msg-" + uuid.NewString()
	sendWagerMessage(t, q, msg3, sqsData(body3), msg3)
	submit(t, app.base, "provider-a", body3)
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msg3) == 1
	}, "concurrent sqs handled")

	if _, debits := ledgerEntries(t, w); debits != 3 {
		t.Fatalf("debits = %d, want 3", debits)
	}
	if bal, _ := walletState(t, w); bal != 7000 {
		t.Fatalf("balance = %d", bal)
	}
	assertConsistent(t, app.base, w)
}

// Mensagens inválidas e conflitantes vão direto para a DLQ (sem gastar
// tentativas e sem efeito financeiro).
func TestSQSInvalidAndConflictingMessagesGoToDLQ(t *testing.T) {
	q := newQueues(t, 5, 5)
	app := startApp(t, testConfig(q))
	w, p := openWallet(t, app.base, "100.00")

	sendRaw(t, q.URL, `{not json`, w, uuid.NewString())
	bad := sqsData(op("provider-a", extID("bet"), p, w, "BET", "1e2", ""))
	sendWagerMessage(t, q, "msg-"+uuid.NewString(), bad, uuid.NewString())
	opening := sqsData(op("provider-a", extID("op"), p, w, "OPENING", "10.00", ""))
	sendWagerMessage(t, q, "msg-"+uuid.NewString(), opening, uuid.NewString())

	// Mesmo messageId com conteúdo diferente.
	msgID := "msg-" + uuid.NewString()
	first := sqsData(op("provider-a", extID("bet"), p, w, "BET", "1.00", ""))
	sendWagerMessage(t, q, msgID, first, uuid.NewString())
	eventually(t, 20*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msgID) == 1
	}, "first message processed")
	tampered := sqsData(op("provider-a", extID("bet"), p, w, "BET", "2.00", ""))
	sendWagerMessage(t, q, msgID, tampered, uuid.NewString())

	msgs := drain(t, q.DLQURL, 0, func(m []types.Message) bool { return len(m) >= 4 })
	reasons := map[string]int{}
	for _, m := range msgs {
		reasons[aws.ToString(m.MessageAttributes["failureReason"].StringValue)]++
	}
	if reasons["invalid_message"] != 3 || reasons["conflict"] != 1 {
		t.Fatalf("DLQ reasons = %v", reasons)
	}
	if _, debits := ledgerEntries(t, w); debits != 1 {
		t.Fatalf("debits = %d", debits)
	}
	assertConsistent(t, app.base, w)
}

// Falha transitória: retry com backoff até dar certo.
func TestSQSTransientFailureIsRetried(t *testing.T) {
	q := newQueues(t, 5, 5)
	cfg := testConfig(q)
	cfg.FaultPoints = "wager.submit=fail:2"
	app := startApp(t, cfg)
	w, p := openWallet(t, app.base, "100.00")

	msgID := "msg-" + uuid.NewString()
	sendWagerMessage(t, q, msgID, sqsData(op("provider-a", extID("bet"), p, w, "BET", "5.00", "")), msgID)
	eventually(t, 40*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msgID) == 1
	}, "message processed after retries")
	if v := metricValue(t, app.base, "wallet_sqs_retries_total"); v < 2 {
		t.Fatalf("retries metric = %v", v)
	}
	assertConsistent(t, app.base, w)
}

// Tentativas esgotadas: a redrive policy leva a mensagem para a DLQ.
func TestSQSExhaustedRetriesGoToDLQ(t *testing.T) {
	q := newQueues(t, 3, 5)
	cfg := testConfig(q)
	cfg.FaultPoints = "wager.submit=fail"
	app := startApp(t, cfg)
	w, p := openWallet(t, app.base, "100.00")

	msgID := "msg-" + uuid.NewString()
	sendWagerMessage(t, q, msgID, sqsData(op("provider-a", extID("bet"), p, w, "BET", "5.00", "")), msgID)
	msgs := drain(t, q.DLQURL, 0, func(m []types.Message) bool { return len(m) >= 1 })
	if len(msgs) != 1 {
		t.Fatalf("DLQ messages = %d", len(msgs))
	}
	if n := countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id=$1`, msgID); n != 0 {
		t.Fatalf("failed message must not be in the inbox")
	}
	assertConsistent(t, app.base, w)
}

// Mensagens movidas pela redrive policy (sem passar pelo código) aparecem
// na métrica de profundidade da DLQ.
func TestDLQDepthMetric(t *testing.T) {
	q := newQueues(t, 2, 5)
	cfg := testConfig(q)
	cfg.FaultPoints = "wager.submit=fail"
	app := startApp(t, cfg)
	w, p := openWallet(t, app.base, "100.00")

	msgID := "msg-" + uuid.NewString()
	sendWagerMessage(t, q, msgID, sqsData(op("provider-a", extID("bet"), p, w, "BET", "5.00", "")), msgID)
	eventually(t, 60*time.Second, func() bool {
		return metricValue(t, app.base, "wallet_sqs_dlq_depth") >= 1
	}, "dlq depth metric reflects the redriven message")
	if v := metricValue(t, app.base, "wallet_sqs_dlq_total"); v != 0 {
		t.Fatalf("message was moved by SQS, not by the consumer; dlq_total = %v", v)
	}
}
