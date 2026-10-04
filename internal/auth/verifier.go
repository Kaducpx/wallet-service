package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"

	"wallet-service/internal/platform/config"
)

var (
	ErrMissingToken = errors.New("missing bearer token")
	ErrInvalidToken = errors.New("invalid token")
)

// Verifier confere tokens com as chaves públicas do Keycloak.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// Campos do token que usamos. Exemplo (resumido) de um token do provider-a:
//
//	{"iss":"http://localhost:8080/realms/wallet", "aud":"wallet-service",
//	 "exp":1760000000, "typ":"Bearer", "azp":"provider-a",
//	 "provider_id":"provider-a", "realm_access":{"roles":["wager-provider"]}}
//
// iss, aud e exp são conferidos pelo go-oidc no Verify.
type claims struct {
	Typ         string `json:"typ"`
	Azp         string `json:"azp"`
	ProviderID  string `json:"provider_id"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

var Module = fx.Module("auth", fx.Provide(NewVerifier))

// NewVerifier prepara a validação. As chaves do Keycloak são baixadas sob
// demanda e ficam em cache. No start, confere se o Keycloak responde.
//
// Issuer e URL das chaves são separados porque, no Docker, o issuer público
// é localhost:8080 e a rede interna usa keycloak:8080.
func NewVerifier(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) *Verifier {
	keySet := oidc.NewRemoteKeySet(context.Background(), cfg.OIDCJWKSURL)
	v := &Verifier{verifier: oidc.NewVerifier(cfg.OIDCIssuer, keySet, &oidc.Config{
		ClientID:             cfg.OIDCAudience,
		SupportedSigningAlgs: []string{oidc.RS256},
	})}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		client := &http.Client{Timeout: 5 * time.Second}
		for {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.OIDCJWKSURL, nil)
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					log.Info("identity provider reachable")
					return nil
				}
				err = fmt.Errorf("jwks status %d", resp.StatusCode)
			}
			log.Warn("identity provider not ready yet", slog.String("error", err.Error()))
			select {
			case <-ctx.Done():
				return fmt.Errorf("identity provider unavailable: %w", err)
			case <-time.After(time.Second):
			}
		}
	}})
	return v
}

// NewVerifierWithClock é para testes que precisam mexer no relógio.
func NewVerifierWithClock(cfg config.Config, now func() time.Time) *Verifier {
	keySet := oidc.NewRemoteKeySet(context.Background(), cfg.OIDCJWKSURL)
	return &Verifier{verifier: oidc.NewVerifier(cfg.OIDCIssuer, keySet, &oidc.Config{
		ClientID: cfg.OIDCAudience, SupportedSigningAlgs: []string{oidc.RS256}, Now: now,
	})}
}

// Authenticate lê "Authorization: Bearer <token>" e valida: formato,
// assinatura (token alterado falha aqui), issuer, audience, expiração e se é
// access token. O token nunca é guardado nem logado.
func (v *Verifier) Authenticate(ctx context.Context, header string) (Principal, error) {
	scheme, raw, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(raw) == "" {
		return Principal{}, ErrMissingToken
	}
	tok, err := v.verifier.Verify(ctx, strings.TrimSpace(raw))
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	var c claims
	if err := tok.Claims(&c); err != nil {
		return Principal{}, fmt.Errorf("%w: claims: %v", ErrInvalidToken, err)
	}
	// Só access token (ID token vem com typ=ID).
	if c.Typ != "" && !strings.EqualFold(c.Typ, "Bearer") {
		return Principal{}, fmt.Errorf("%w: unexpected token type %q", ErrInvalidToken, c.Typ)
	}
	roles := make(map[string]bool, len(c.RealmAccess.Roles))
	for _, r := range c.RealmAccess.Roles {
		roles[r] = true
	}
	return Principal{Subject: tok.Subject, ClientID: c.Azp, ProviderID: c.ProviderID, roles: roles}, nil
}
