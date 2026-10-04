//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Cliente HTTP e atalhos para as chamadas da API.

var httpClient = &http.Client{Timeout: 60 * time.Second}

type response struct {
	Status int
	Body   map[string]any
	Raw    string
	Header http.Header
}

func (r response) str(path ...string) string {
	var cur any = r.Body
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[p]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprint(cur)
}

// call faz uma requisição e falha o teste se a conexão falhar.
func call(t testing.TB, method, url, tok string, headers map[string]string, body any) response {
	t.Helper()
	r, err := tryCall(method, url, tok, headers, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return r
}

// tryCall faz uma requisição e devolve o erro de conexão em vez de falhar
// (usado quando o teste espera que o servidor caia).
func tryCall(method, url, tok string, headers map[string]string, body any) (response, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := response{Status: resp.StatusCode, Raw: string(raw), Header: resp.Header}
	_ = json.Unmarshal(raw, &out.Body)
	return out, nil
}

// openWallet abre uma carteira como o serviço interno.
func openWallet(t testing.TB, base, amount string) (walletID, playerID string) {
	t.Helper()
	playerID = uuid.NewString()
	r := call(t, http.MethodPost, base+"/wallets", token(t, "wallet-backoffice"), nil, map[string]any{
		"playerId": playerID, "initialBalance": map[string]string{"amount": amount, "currency": "BRL"},
	})
	if r.Status != http.StatusCreated {
		t.Fatalf("open wallet: %d %s", r.Status, r.Raw)
	}
	return r.str("id"), playerID
}

// op monta o corpo de uma operação externa.
func op(provider, extID, playerID, walletID, kind, amount, ref string) map[string]any {
	m := map[string]any{
		"providerId": provider, "externalTransactionId": extID, "playerId": playerID, "walletId": walletID,
		"roundId": "round-1", "gameId": "fortune-chimp", "kind": kind,
		"money": map[string]string{"amount": amount, "currency": "BRL"},
	}
	if ref != "" {
		m["referenceExternalTransactionId"] = ref
	}
	return m
}

// submit envia uma operação com Idempotency-Key = providerId:externalTransactionId.
func submit(t testing.TB, base, client string, body map[string]any) response {
	t.Helper()
	key := fmt.Sprintf("%s:%s", body["providerId"], body["externalTransactionId"])
	return call(t, http.MethodPost, base+"/wagering/transactions", token(t, client),
		map[string]string{"Idempotency-Key": key}, body)
}

// extID gera um id externo único para cada teste.
func extID(prefix string) string { return prefix + "-" + uuid.NewString()[:12] }
