//go:build integration

package integration

import (
	"bufio"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Asserções direto no banco: saldo x ledger, contagens e métricas.

// walletState lê saldo (centavos) e versão direto no banco.
func walletState(t testing.TB, walletID string) (balance, version int64) {
	t.Helper()
	if err := db.QueryRow(context.Background(),
		`SELECT balance_units, version FROM wallets WHERE id = $1`, walletID).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	return
}

// ledgerEntries conta lançamentos e débitos de uma carteira.
func ledgerEntries(t testing.TB, walletID string) (count int, debits int) {
	t.Helper()
	if err := db.QueryRow(context.Background(), `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE direction = 'DEBIT')
		  FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&count, &debits); err != nil {
		t.Fatal(err)
	}
	return
}

// assertConsistent confere saldo armazenado = créditos - débitos do ledger
// e versão = número de lançamentos (abertura inclusa), direto no banco e
// pelo endpoint de reconciliação.
func assertConsistent(t testing.TB, base, walletID string) {
	t.Helper()
	var balance, version, sum, entries int64
	err := db.QueryRow(context.Background(), `
		SELECT w.balance_units, w.version,
		       COALESCE(SUM(CASE l.direction WHEN 'CREDIT' THEN l.amount_units ELSE -l.amount_units END), 0)::BIGINT,
		       COUNT(l.id)
		  FROM wallets w LEFT JOIN wallet_ledger_entries l ON l.wallet_id = w.id
		 WHERE w.id = $1 GROUP BY w.id`, walletID).Scan(&balance, &version, &sum, &entries)
	if err != nil {
		t.Fatal(err)
	}
	if balance != sum {
		t.Fatalf("wallet %s: stored balance %d != ledger sum %d", walletID, balance, sum)
	}
	if balance < 0 {
		t.Fatalf("wallet %s: negative balance %d", walletID, balance)
	}
	expectedVersion := entries
	if entries == 0 {
		expectedVersion = 1
	}
	if version != expectedVersion {
		t.Fatalf("wallet %s: version %d but %d ledger entries", walletID, version, entries)
	}
	if base != "" {
		r := call(t, http.MethodPost, base+"/wallets/"+walletID+"/reconciliation", token(t, "wallet-backoffice"), nil, nil)
		if r.Status != http.StatusOK || r.Body["consistent"] != true || r.str("difference", "amount") != "0.00" {
			t.Fatalf("reconciliation: %d %s", r.Status, r.Raw)
		}
	}
}

// countRows executa um SELECT COUNT(*) e devolve o número.
func countRows(t testing.TB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// eventually repete cond até ser verdadeira ou o prazo vencer.
func eventually(t testing.TB, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", what)
}

// metricValue lê um contador do /metrics (soma das séries que casam com os
// fragmentos de label informados).
func metricValue(t testing.TB, base, name string, labelFragments ...string) float64 {
	t.Helper()
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var total float64
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, name) {
			continue
		}
		match := true
		for _, f := range labelFragments {
			if !strings.Contains(line, f) {
				match = false
			}
		}
		if !match {
			continue
		}
		fields := strings.Fields(line)
		v, _ := strconv.ParseFloat(fields[len(fields)-1], 64)
		total += v
	}
	return total
}
