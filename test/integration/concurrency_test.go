//go:build integration

package integration

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Teste obrigatório: carteira com 100.00 recebe ao mesmo tempo duas apostas
// distintas de 80.00. Resultado: uma processada, uma rejeitada por saldo,
// saldo final 20.00 e um único débito. Reenvios não mudam nada.
func TestTwoConcurrentBetsOf80(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	w, p := openWallet(t, app.base, "100.00")
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
			start.Wait() // dispara as duas ao mesmo tempo
			results[i] = submit(t, app.base, "provider-a", bets[i])
		}(i)
	}
	start.Done()
	wg.Wait()

	assertOneProcessedOneRejected(t, results)
	assertFinal20(t, app.base, w)

	// Reenvio das duas: replays, nada muda.
	for _, b := range bets {
		if r := submit(t, app.base, "provider-a", b); r.Body["idempotentReplay"] != true {
			t.Fatalf("resend must be a replay: %s", r.Raw)
		}
	}
	assertFinal20(t, app.base, w)
}

// assertOneProcessedOneRejected confere o resultado da disputa 80 + 80.
func assertOneProcessedOneRejected(t *testing.T, results []response) {
	t.Helper()
	processed, rejected := 0, 0
	for _, r := range results {
		switch {
		case r.Status == http.StatusCreated && r.str("status") == "PROCESSED":
			processed++
		case r.Status == http.StatusUnprocessableEntity && r.str("failureCode") == "INSUFFICIENT_FUNDS":
			rejected++
		default:
			t.Fatalf("unexpected result: %d %s", r.Status, r.Raw)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed=%d rejected=%d", processed, rejected)
	}
}

func assertFinal20(t *testing.T, base, w string) {
	t.Helper()
	if bal, _ := walletState(t, w); bal != 2000 {
		t.Fatalf("final balance = %d, want 2000", bal)
	}
	if _, debits := ledgerEntries(t, w); debits != 1 {
		t.Fatalf("debits = %d, want 1", debits)
	}
	assertConsistent(t, base, w)
}

// A mesma aposta enviada 50 vezes em paralelo gera um único débito; as
// demais respostas são replays (deduplicação da aplicação comprovada pela
// métrica de duplicatas).
func TestSameBet50TimesInParallel(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	w, p := openWallet(t, app.base, "100.00")
	body := op("provider-a", extID("bet"), p, w, "BET", "10.00", "")

	results := make([]response, 50)
	var start, wg sync.WaitGroup
	start.Add(1)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			results[i] = submit(t, app.base, "provider-a", body)
		}(i)
	}
	start.Done()
	wg.Wait()

	ids := map[string]bool{}
	fresh, replays := 0, 0
	for _, r := range results {
		if r.str("status") != "PROCESSED" || r.str("balance", "amount") != "90.00" {
			t.Fatalf("unexpected: %d %s", r.Status, r.Raw)
		}
		ids[r.str("transactionId")] = true
		if r.Body["idempotentReplay"] == true {
			replays++
		} else {
			fresh++
		}
	}
	if len(ids) != 1 || fresh != 1 || replays != 49 {
		t.Fatalf("ids=%d fresh=%d replays=%d", len(ids), fresh, replays)
	}
	if _, debits := ledgerEntries(t, w); debits != 1 {
		t.Fatalf("debits = %d", debits)
	}
	if d := metricValue(t, app.base, "wallet_duplicates_total", `channel="http"`); d < 49 {
		t.Fatalf("duplicate metric = %v", d)
	}
	assertConsistent(t, app.base, w)
}

// Carteiras diferentes avançam em paralelo: com a linha da carteira A
// bloqueada por outra transação, uma operação na carteira B termina
// normalmente (não existe lock global); a da carteira A espera.
func TestDistinctWalletsProgressInParallel(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	wa, pa := openWallet(t, app.base, "100.00")
	wb, pb := openWallet(t, app.base, "100.00")

	// Segura o lock da carteira A.
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, wa); err != nil {
		t.Fatal(err)
	}

	doneA := make(chan response, 1)
	go func() {
		doneA <- submit(t, app.base, "provider-a", op("provider-a", extID("bet"), pa, wa, "BET", "1.00", ""))
	}()

	start := time.Now()
	rb := submit(t, app.base, "provider-a", op("provider-a", extID("bet"), pb, wb, "BET", "1.00", ""))
	if rb.Status != http.StatusCreated || time.Since(start) > 3*time.Second {
		t.Fatalf("wallet B must not wait for wallet A: %d after %s", rb.Status, time.Since(start))
	}
	select {
	case r := <-doneA:
		t.Fatalf("wallet A should be blocked while locked, got %d", r.Status)
	case <-time.After(500 * time.Millisecond):
	}
	_ = tx.Rollback(ctx)
	if r := <-doneA; r.Status != http.StatusCreated {
		t.Fatalf("wallet A after unlock: %s", r.Raw)
	}

	// Carga simultânea em várias carteiras.
	const wallets, betsPerWallet = 10, 10
	type wp struct{ w, p string }
	var ws []wp
	for i := 0; i < wallets; i++ {
		w, p := openWallet(t, app.base, "100.00")
		ws = append(ws, wp{w, p})
	}
	var wg sync.WaitGroup
	for _, x := range ws {
		for j := 0; j < betsPerWallet; j++ {
			wg.Add(1)
			go func(x wp) {
				defer wg.Done()
				if r := submit(t, app.base, "provider-a", op("provider-a", extID("bet"), x.p, x.w, "BET", "5.00", "")); r.Status != http.StatusCreated {
					t.Errorf("bet: %d %s", r.Status, r.Raw)
				}
			}(x)
		}
	}
	wg.Wait()
	for _, x := range ws {
		if bal, _ := walletState(t, x.w); bal != 5000 {
			t.Fatalf("wallet %s balance = %d", x.w, bal)
		}
		assertConsistent(t, app.base, x.w)
	}
}
