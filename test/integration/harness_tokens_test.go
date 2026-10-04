//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Tokens reais emitidos pelo Keycloak (client_credentials), com cache.

var clientSecrets = map[string]string{
	"provider-a":             "provider-a-local-secret",
	"provider-b":             "provider-b-local-secret",
	"provider-a-short-lived": "provider-a-short-local-secret",
	"wallet-backoffice":      "wallet-backoffice-local-secret",
	"no-role-client":         "no-role-local-secret",
}

type cachedToken struct {
	value  string
	expiry time.Time
}

var (
	tokenMu    sync.Mutex
	tokenCache = map[string]cachedToken{}
)

// fetchToken pede um token ao Keycloak com client_credentials (sem cache).
func fetchToken(client string) (string, time.Duration, error) {
	form := url.Values{
		"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {clientSecrets[client]},
	}
	resp, err := http.PostForm(keycloakURL+"/realms/wallet/protocol/openid-connect/token", form)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		return "", 0, fmt.Errorf("token for %s: status %d %v", client, resp.StatusCode, err)
	}
	return body.AccessToken, time.Duration(body.ExpiresIn) * time.Second, nil
}

// token devolve um token válido do client, com cache.
func token(t testing.TB, client string) string {
	t.Helper()
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if c, ok := tokenCache[client]; ok && time.Until(c.expiry) > 30*time.Second {
		return c.value
	}
	v, ttl, err := fetchToken(client)
	if err != nil {
		t.Fatal(err)
	}
	tokenCache[client] = cachedToken{value: v, expiry: time.Now().Add(ttl)}
	return v
}
