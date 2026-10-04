//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type envelope struct {
	EventID       string          `json:"eventId"`
	EventType     string          `json:"eventType"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	CausationID   string          `json:"causationId"`
	OccurredAt    string          `json:"occurredAt"`
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

// envelopes decodifica os corpos das mensagens da fila de eventos.
func envelopes(msgs []types.Message) []envelope {
	var out []envelope
	for _, m := range msgs {
		var e envelope
		_ = json.Unmarshal([]byte(aws.ToString(m.Body)), &e)
		out = append(out, e)
	}
	return out
}

// Eventos só existem depois do commit e chegam ao destino com o envelope
// completo e o payload tipado.
func TestOutboxPublishesEnvelopes(t *testing.T) {
	q := newQueues(t, 5, 5)
	app := startApp(t, testConfig(q))
	w, p := openWallet(t, app.base, "100.00")
	submit(t, app.base, "provider-a", op("provider-a", extID("bet"), p, w, "BET", "25.00", ""))

	msgs := drain(t, q.EventsURL, 0, func(m []types.Message) bool {
		n := 0
		for _, e := range envelopes(m) {
			if e.AggregateID == w {
				n++
			}
		}
		return n >= 4 // abertura (2) + aposta (2)
	})
	var balanceChanged *envelope
	for _, e := range envelopes(msgs) {
		if e.AggregateID != w {
			continue
		}
		if e.EventID == "" || e.CorrelationID == "" || e.Version != 1 {
			t.Fatalf("incomplete envelope: %+v", e)
		}
		if _, err := time.Parse(time.RFC3339, e.OccurredAt); err != nil {
			t.Fatalf("occurredAt not RFC 3339: %s", e.OccurredAt)
		}
		if e.EventType == "WalletBalanceChanged" {
			e := e
			balanceChanged = &e
		}
	}
	if balanceChanged == nil {
		t.Fatal("WalletBalanceChanged not published")
	}
	var data map[string]any
	_ = json.Unmarshal(balanceChanged.Data, &data)
	for _, f := range []string{"walletId", "transactionId", "direction", "money", "balanceBefore", "balanceAfter", "walletVersion"} {
		if _, ok := data[f]; !ok {
			t.Fatalf("WalletBalanceChanged missing %s: %s", f, balanceChanged.Data)
		}
	}
	eventually(t, 10*time.Second, func() bool {
		return countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w) == 0
	}, "all events marked as published")
}

// Dois publicadores disputam a mesma outbox: cada evento é confirmado uma
// única vez e todos chegam ao destino com o eventId original.
func TestTwoPublishersCompeteForOutbox(t *testing.T) {
	q := newQueues(t, 5, 5)

	// Gera eventos com o publicador desligado.
	cfg := testConfig(q)
	cfg.OutboxEnabled = false
	producer := startApp(t, cfg)
	var wallets []string
	for i := 0; i < 15; i++ {
		w, p := openWallet(t, producer.base, "100.00")
		submit(t, producer.base, "provider-a", op("provider-a", extID("bet"), p, w, "BET", "1.00", ""))
		wallets = append(wallets, w)
	}
	producer.stop(t)
	var expected int
	for _, w := range wallets {
		expected += countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1`, w)
	}

	// Dois publicadores simultâneos.
	pub1, pub2 := testConfig(q), testConfig(q)
	pub1.OutboxBatchSize, pub2.OutboxBatchSize = 5, 5
	startApp(t, pub1)
	startApp(t, pub2)

	eventually(t, 30*time.Second, func() bool {
		pending := 0
		for _, w := range wallets {
			pending += countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, w)
		}
		return pending == 0
	}, "outbox drained by two publishers")

	ids := map[string]bool{}
	inWallets := map[string]bool{}
	for _, w := range wallets {
		inWallets[w] = true
	}
	msgs := drain(t, q.EventsURL, 3*time.Second, nil)
	for _, e := range envelopes(msgs) {
		if inWallets[e.AggregateID] {
			ids[e.EventID] = true
		}
	}
	if len(ids) != expected {
		t.Fatalf("distinct events delivered = %d, expected %d", len(ids), expected)
	}
}
