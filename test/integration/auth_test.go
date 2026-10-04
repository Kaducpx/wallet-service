//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Credenciais ausentes, inválidas, adulteradas ou expiradas: 401 e nenhum
// efeito no banco.
func TestAuthenticationIsRequired(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	w, p := openWallet(t, app.base, "100.00")
	ext := extID("bet")
	body := op("provider-a", ext, p, w, "BET", "10.00", "")
	headers := map[string]string{"Idempotency-Key": "provider-a:" + ext}

	valid := token(t, "provider-a")
	parts := strings.Split(valid, ".")
	sig := []byte(parts[2])
	if sig[5] == 'A' {
		sig[5] = 'B'
	} else {
		sig[5] = 'A'
	}
	tampered := parts[0] + "." + parts[1] + "." + string(sig)

	short, _, err := fetchToken("provider-a-short-lived")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(4500 * time.Millisecond) // o token de 3s expira

	for name, tok := range map[string]string{
		"missing": "", "garbage": "not-a-jwt", "tampered": tampered, "expired": short,
	} {
		r := call(t, http.MethodPost, app.base+"/wagering/transactions", tok, headers, body)
		if r.Status != http.StatusUnauthorized || r.str("error", "code") != "UNAUTHENTICATED" {
			t.Errorf("%s token: %d %s", name, r.Status, r.Raw)
		}
		r = call(t, http.MethodGet, app.base+"/wallets/"+w, tok, nil, nil)
		if r.Status != http.StatusUnauthorized {
			t.Errorf("%s token on wallet: %d", name, r.Status)
		}
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE external_transaction_id=$1`, ext); n != 0 {
		t.Fatalf("unauthenticated request produced %d transactions", n)
	}
	// O mesmo corpo com token válido funciona (prova que o 401 era do token).
	if r := call(t, http.MethodPost, app.base+"/wagering/transactions", valid, headers, body); r.Status != http.StatusCreated {
		t.Fatalf("valid token: %d %s", r.Status, r.Raw)
	}
	// Health checks são públicos.
	if r := call(t, http.MethodGet, app.base+"/health/live", "", nil, nil); r.Status != http.StatusOK {
		t.Fatalf("live: %d", r.Status)
	}
}

// Autorização: papéis por rota e isolamento entre provedores, inclusive em
// consultas e replays.
func TestAuthorizationAndProviderIsolation(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	w, p := openWallet(t, app.base, "100.00")
	ext := extID("bet")
	bodyA := op("provider-a", ext, p, w, "BET", "10.00", "")
	created := submit(t, app.base, "provider-a", bodyA)
	txID := created.str("transactionId")

	// Operações de carteira são exclusivas do serviço interno.
	for _, client := range []string{"provider-a", "no-role-client"} {
		r := call(t, http.MethodPost, app.base+"/wallets", token(t, client), nil, map[string]any{
			"playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", "initialBalance": map[string]string{"amount": "1.00", "currency": "BRL"},
		})
		if r.Status != http.StatusForbidden {
			t.Errorf("%s POST /wallets: %d", client, r.Status)
		}
		for _, path := range []string{"/wallets/" + w, "/wallets/" + w + "/ledger"} {
			if r := call(t, http.MethodGet, app.base+path, token(t, client), nil, nil); r.Status != http.StatusForbidden {
				t.Errorf("%s GET %s: %d", client, path, r.Status)
			}
		}
		if r := call(t, http.MethodPost, app.base+"/wallets/"+w+"/reconciliation", token(t, client), nil, nil); r.Status != http.StatusForbidden {
			t.Errorf("%s reconciliation: %d", client, r.Status)
		}
	}
	// O serviço interno não envia operações de provedor; sem papel também não.
	for _, client := range []string{"wallet-backoffice", "no-role-client"} {
		r := call(t, http.MethodPost, app.base+"/wagering/transactions", token(t, client),
			map[string]string{"Idempotency-Key": "x:" + extID("y")}, op("provider-a", extID("y"), p, w, "BET", "1.00", ""))
		if r.Status != http.StatusForbidden {
			t.Errorf("%s submit: %d", client, r.Status)
		}
	}

	// provider-b não pode agir em nome do provider-a — nem em replay.
	r := call(t, http.MethodPost, app.base+"/wagering/transactions", token(t, "provider-b"),
		map[string]string{"Idempotency-Key": "provider-a:" + ext}, bodyA)
	if r.Status != http.StatusForbidden || r.str("error", "code") != "PROVIDER_MISMATCH" || strings.Contains(r.Raw, txID) {
		t.Fatalf("impersonation/replay by provider-b: %d %s", r.Status, r.Raw)
	}
	// Mesma chave/ids no espaço do provider-b: operação independente, não
	// devolve o resultado do provider-a.
	bodyB := op("provider-b", ext, p, w, "BET", "10.00", "")
	r = call(t, http.MethodPost, app.base+"/wagering/transactions", token(t, "provider-b"),
		map[string]string{"Idempotency-Key": "provider-a:" + ext}, bodyB)
	if r.Body["idempotentReplay"] == true || r.str("transactionId") == txID {
		t.Fatalf("provider-b must not see provider-a's result: %s", r.Raw)
	}

	// Consultas.
	if r := call(t, http.MethodGet, app.base+"/wagering/transactions/"+txID, token(t, "provider-b"), nil, nil); r.Status != http.StatusNotFound {
		t.Errorf("provider-b reading provider-a tx by id: %d", r.Status)
	}
	if r := call(t, http.MethodGet, app.base+"/providers/provider-a/wagering/transactions/"+ext, token(t, "provider-b"), nil, nil); r.Status != http.StatusForbidden {
		t.Errorf("provider-b reading provider-a route: %d", r.Status)
	}
	if r := call(t, http.MethodGet, app.base+"/providers/provider-a/wagering/transactions/"+ext, token(t, "provider-a"), nil, nil); r.Status != http.StatusOK || r.str("transactionId") != txID {
		t.Errorf("provider-a own tx: %d %s", r.Status, r.Raw)
	}
	if r := call(t, http.MethodGet, app.base+"/wagering/transactions/"+txID, token(t, "wallet-backoffice"), nil, nil); r.Status != http.StatusOK {
		t.Errorf("operator reading tx: %d", r.Status)
	}
	// OPENING é interno: provedores não enxergam.
	var openingID string
	if err := db.QueryRow(t.Context(), `SELECT id::text FROM wager_transactions WHERE wallet_id=$1 AND kind='OPENING'`, w).Scan(&openingID); err != nil {
		t.Fatal(err)
	}
	if r := call(t, http.MethodGet, app.base+"/wagering/transactions/"+openingID, token(t, "provider-a"), nil, nil); r.Status != http.StatusNotFound {
		t.Errorf("provider reading OPENING: %d", r.Status)
	}

	// Nenhum efeito financeiro das tentativas negadas: apenas os débitos
	// legítimos (provider-a e a operação própria do provider-b).
	if _, debits := ledgerEntries(t, w); debits != 2 {
		t.Fatalf("debits = %d", debits)
	}
	assertConsistent(t, app.base, w)
}
