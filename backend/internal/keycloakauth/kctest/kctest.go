// Package kctest spins up a minimal stand-in for a Keycloak realm's OIDC
// endpoints (discovery document + JWKS) and mints tokens signed by the key it
// publishes. It exists so that tests in several packages can exercise the real
// keycloakauth verification path — signature, issuer and expiry checks
// included — instead of stubbing it out. It is test support only and is never
// imported by production code.
package kctest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const keyID = "kctest-key"

// Realm is a fake Keycloak realm served over HTTP.
type Realm struct {
	// Issuer is the URL to pass to keycloakauth.New.
	Issuer string

	key    *rsa.PrivateKey
	signer jose.Signer
}

// NewRealm starts the fake realm and stops it when the test ends.
func NewRealm(t *testing.T) *Realm {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("kctest: generate key: %v", err)
	}

	r := &Realm{key: key}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.Issuer = srv.URL

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/protocol/openid-connect/auth",
			"token_endpoint":                        srv.URL + "/protocol/openid-connect/token",
			"jwks_uri":                              srv.URL + "/protocol/openid-connect/certs",
			"id_token_signing_alg_values_supported": []string{string(jose.RS256)},
		})
	})

	mux.HandleFunc("/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{{
				Key:       key.Public(),
				KeyID:     keyID,
				Algorithm: string(jose.RS256),
				Use:       "sig",
			}},
		})
	})

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID),
	)
	if err != nil {
		t.Fatalf("kctest: new signer: %v", err)
	}
	r.signer = signer

	return r
}

// Token mints a token signed by the realm's key. extra is merged into the
// payload on top of the standard iss/sub/aud/exp/iat claims, so a test can add
// clerk_user_id, resource_access, and so on.
func (r *Realm) Token(t *testing.T, subject string, extra map[string]any) string {
	t.Helper()
	return r.token(t, r.signer, r.Issuer, subject, extra)
}

// TokenWithIssuer mints an otherwise-valid token claiming a different issuer,
// to prove the verifier rejects it.
func (r *Realm) TokenWithIssuer(t *testing.T, issuer, subject string) string {
	t.Helper()
	return r.token(t, r.signer, issuer, subject, nil)
}

// TokenSignedByStranger mints a token for this realm's issuer signed with a key
// the realm does not publish, to prove the signature check is real.
func (r *Realm) TokenSignedByStranger(t *testing.T, subject string) string {
	t.Helper()

	strangerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("kctest: generate stranger key: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: strangerKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID),
	)
	if err != nil {
		t.Fatalf("kctest: new stranger signer: %v", err)
	}
	return r.token(t, signer, r.Issuer, subject, nil)
}

// ExpiredToken mints a correctly signed token that expired an hour ago.
func (r *Realm) ExpiredToken(t *testing.T, subject string) string {
	t.Helper()
	return r.token(t, r.signer, r.Issuer, subject, map[string]any{
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
}

func (r *Realm) token(t *testing.T, signer jose.Signer, issuer, subject string, extra map[string]any) string {
	t.Helper()

	now := time.Now()
	payload := map[string]any{
		"iss": issuer,
		"sub": subject,
		// Keycloak's own shape: aud is "account", and azp names the client the
		// token was issued to. Tests override azp to exercise the allowlist.
		"aud": "account",
		"azp": "gnomonitoring-panel",
		"exp": now.Add(time.Hour).Unix(),
		"iat": now.Unix(),
	}
	for k, v := range extra {
		payload[k] = v
	}

	raw, err := jwt.Signed(signer).Claims(payload).Serialize()
	if err != nil {
		t.Fatalf("kctest: sign token: %v", err)
	}
	return raw
}

// AdminResourceAccess is the resource_access claim value a gnomonitoring admin
// carries: the "admin" role on the gnomonitoring-panel client.
func AdminResourceAccess() map[string]any {
	return resourceAccess("gnomonitoring-panel", "offline_access", "admin")
}

// NonAdminResourceAccess is the resource_access claim value of an
// authenticated-but-not-admin user.
func NonAdminResourceAccess() map[string]any {
	return resourceAccess("gnomonitoring-panel", "offline_access")
}

func resourceAccess(clientID string, roles ...string) map[string]any {
	return map[string]any{clientID: map[string]any{"roles": roles}}
}
