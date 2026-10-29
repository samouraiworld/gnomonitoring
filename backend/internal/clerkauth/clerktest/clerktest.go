// Package clerktest spins up a minimal stand-in for the two Clerk Backend API
// endpoints clerkauth calls (the JWKS and a user lookup) and mints session
// tokens signed by the key it publishes. It exists so that tests in several
// packages can exercise the real clerkauth verification path — signature,
// issuer and expiry checks included — instead of stubbing it out. It is test
// support only and is never imported by production code.
package clerktest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	// SecretKey is the only secret key the fake API accepts.
	SecretKey = "sk_test_clerktest"
	// Issuer is the "iss" of every token Token mints, shaped like a Clerk
	// production Frontend API URL.
	Issuer = "https://clerk.clerktest.example"
)

// API is a fake Clerk Backend API served over HTTP.
type API struct {
	// URL is the base URL to pass to clerkauth.New, standing in for
	// clerkauth.DefaultAPIURL.
	URL string

	jwksRequests atomic.Int64
	jwksDown     atomic.Bool

	mu       sync.Mutex
	key      *rsa.PrivateKey // the key the JWKS publishes and Token signs with
	keyID    string
	rotation int
	users    map[string]json.RawMessage // user id -> public_metadata
}

// NewAPI starts the fake API and stops it when the test ends.
func NewAPI(t *testing.T) *API {
	t.Helper()

	a := &API{users: map[string]json.RawMessage{}}
	a.RotateKey(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/jwks", func(w http.ResponseWriter, r *http.Request) {
		a.jwksRequests.Add(1)
		if a.jwksDown.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		a.mu.Lock()
		jwk := jose.JSONWebKey{Key: &a.key.PublicKey, KeyID: a.keyID, Algorithm: string(jose.RS256), Use: "sig"}
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	})
	mux.HandleFunc("GET /v1/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		a.mu.Lock()
		metadata, ok := a.users[r.PathValue("id")]
		a.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":              r.PathValue("id"),
			"public_metadata": metadata,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	a.URL = srv.URL + "/v1"
	return a
}

// RotateKey replaces the signing key with a new one under a new key id, as
// Clerk does on a key rotation: the JWKS stops publishing the old key, and
// Token signs with the new one.
func (a *API) RotateKey(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("clerktest: generate key: %v", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rotation++
	a.key, a.keyID = key, fmt.Sprintf("clerktest-key-%d", a.rotation)
}

func authorized(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+SecretKey
}

// SetUser registers a user the user endpoint returns, with publicMetadata
// (a JSON object, e.g. `{"role":"admin"}`) as its public_metadata.
func (a *API) SetUser(id, publicMetadata string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.users[id] = json.RawMessage(publicMetadata)
}

// SetJWKSDown makes the JWKS endpoint answer 503 (true) or answer again
// (false), standing in for a Backend API outage.
func (a *API) SetJWKSDown(down bool) {
	a.jwksDown.Store(down)
}

// JWKSRequests is the number of requests the JWKS endpoint has received.
func (a *API) JWKSRequests() int64 {
	return a.jwksRequests.Load()
}

// Token mints a valid session token for subject, shaped like a Clerk one.
func (a *API) Token(t *testing.T, subject string) string {
	t.Helper()
	return a.TokenWith(t, subject, nil)
}

// TokenWith mints a session token for subject whose claims are overridden by
// extra; a nil value removes that claim.
func (a *API) TokenWith(t *testing.T, subject string, extra map[string]any) string {
	t.Helper()
	a.mu.Lock()
	key, keyID := a.key, a.keyID
	a.mu.Unlock()
	return sign(t, key, keyID, subject, extra)
}

// TokenWithoutKeyID mints a token signed by the API's current key that names
// no key id.
func (a *API) TokenWithoutKeyID(t *testing.T, subject string) string {
	t.Helper()
	a.mu.Lock()
	key := a.key
	a.mu.Unlock()
	return sign(t, key, "", subject, nil)
}

// TokenSignedByStranger mints a token that names the API's current key id but
// is signed by an unrelated key.
func (a *API) TokenSignedByStranger(t *testing.T, subject string) string {
	t.Helper()
	strangerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("clerktest: generate stranger key: %v", err)
	}
	a.mu.Lock()
	keyID := a.keyID
	a.mu.Unlock()
	return sign(t, strangerKey, keyID, subject, nil)
}

// sign mints a token signed by key; an empty keyID leaves kid out.
func sign(t *testing.T, key *rsa.PrivateKey, keyID, subject string, extra map[string]any) string {
	t.Helper()

	options := (&jose.SignerOptions{}).WithType("JWT")
	if keyID != "" {
		options = options.WithHeader(jose.HeaderKey("kid"), keyID)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, options)
	if err != nil {
		t.Fatalf("clerktest: build signer: %v", err)
	}

	now := time.Now()
	// Clerk's own session token shape: no aud, azp names the frontend origin,
	// nbf a few seconds before iat.
	payload := map[string]any{
		"iss": Issuer,
		"sub": subject,
		"sid": "sess_" + strings.TrimPrefix(subject, "user_"),
		"azp": "https://app.clerktest.example",
		"exp": now.Add(time.Minute).Unix(),
		"iat": now.Unix(),
		"nbf": now.Add(-10 * time.Second).Unix(),
	}
	for k, v := range extra {
		if v == nil {
			delete(payload, k)
			continue
		}
		payload[k] = v
	}

	raw, err := jwt.Signed(signer).Claims(payload).Serialize()
	if err != nil {
		t.Fatalf("clerktest: sign token: %v", err)
	}
	return raw
}
