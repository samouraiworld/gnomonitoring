package clerkauth

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/clerkauth/clerktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, api *clerktest.API) *Client {
	t.Helper()
	c, err := New(api.URL, clerktest.SecretKey)
	require.NoError(t, err)
	return c
}

func TestVerifyToken_AcceptsClerkSessionToken(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)

	claims, err := c.VerifyToken(context.Background(), api.Token(t, "user_1"))
	require.NoError(t, err)
	assert.Equal(t, "user_1", claims.Subject)
	assert.Equal(t, "sess_1", claims.SessionID)
	assert.Equal(t, clerktest.Issuer, claims.Issuer)
}

// The JWKS is read from the Backend API with the secret key, as clerk-sdk-go
// did, so the fake API refuses it to any other caller.
func TestVerifyToken_RefusedWhenSecretKeyIsWrong(t *testing.T) {
	api := clerktest.NewAPI(t)
	c, err := New(api.URL, "sk_test_wrong")
	require.NoError(t, err)

	_, err = c.VerifyToken(context.Background(), api.Token(t, "user_1"))
	assert.Error(t, err)
}

func TestVerifyToken_EmptySecretKeyMakesNoRequest(t *testing.T) {
	api := clerktest.NewAPI(t)
	c, err := New(api.URL, "")
	require.NoError(t, err)

	_, err = c.VerifyToken(context.Background(), api.Token(t, "user_1"))
	assert.Error(t, err)
	assert.Zero(t, api.JWKSRequests())
}

func TestVerifyToken_Refusals(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)
	hour := time.Hour

	tests := []struct {
		name  string
		token string
	}{
		{"expired", api.TokenWith(t, "user_1", map[string]any{"exp": time.Now().Add(-hour).Unix()})},
		{"no expiry", api.TokenWith(t, "user_1", map[string]any{"exp": nil})},
		// clerk-sdk-go checked nbf and iat with no leeway.
		{"not valid yet", api.TokenWith(t, "user_1", map[string]any{"nbf": time.Now().Add(time.Minute).Unix()})},
		{"issued in the future", api.TokenWith(t, "user_1", map[string]any{"iat": time.Now().Add(time.Minute).Unix()})},
		{"names no key", api.TokenWithoutKeyID(t, "user_1")},
		{"signed by another key", api.TokenSignedByStranger(t, "user_1")},
		{"issuer is not a Clerk Frontend API", api.TokenWith(t, "user_1", map[string]any{"iss": "https://evil.example"})},
		{"no subject", api.TokenWith(t, "", nil)},
		{"not a JWT", "not-a-jwt"},
		{"unsigned", unsignedToken()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.VerifyToken(context.Background(), tt.token)
			assert.Error(t, err)
		})
	}
}

// The development instances' Frontend API lives under .clerk.accounts.dev;
// clerk-sdk-go accepted it, so this does too.
func TestVerifyToken_AcceptsDevelopmentInstanceIssuer(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)

	token := api.TokenWith(t, "user_1", map[string]any{"iss": "https://happy-cat-12.clerk.accounts.dev"})
	_, err := c.VerifyToken(context.Background(), token)
	assert.NoError(t, err)
}

// CVE-2026-116587: go-jose v3, which clerk-sdk-go used, validated an RSA
// private key embedded in a JWS "jwk" header while parsing, before any
// signature check. The token is refused here without that work.
func TestVerifyToken_RefusesEmbeddedPrivateKey(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)

	enc := base64.RawURLEncoding.EncodeToString
	header := `{"alg":"RS256","kid":"clerktest-key","jwk":{"kty":"RSA","n":"AQAB","e":"AQAB","d":"AQAB","p":"Aw","q":"Aw"}}`
	token := enc([]byte(header)) + "." + enc([]byte(`{"sub":"user_1"}`)) + "." + enc([]byte("signature"))

	_, err := c.VerifyToken(context.Background(), token)
	// go-jose v4.1.6 refuses private key material in a header JWK outright.
	assert.ErrorContains(t, err, "JWK contains private parameters")
	assert.Zero(t, api.JWKSRequests(), "the token must be refused before any key lookup")
}

// A forged token naming a cached key must not cost a Backend API call, which
// would carry the secret key against Clerk's rate limit.
func TestVerifyToken_ForgedTokensDoNotRefetchKeys(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)

	_, err := c.VerifyToken(context.Background(), api.Token(t, "user_1"))
	require.NoError(t, err)
	for range 5 {
		_, err := c.VerifyToken(context.Background(), api.TokenSignedByStranger(t, "user_1"))
		require.Error(t, err)
	}
	assert.Equal(t, int64(1), api.JWKSRequests())
}

func TestVerifyToken_KeyCache(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)
	now := time.Now()
	c.keys.now = func() time.Time { return now }

	retiredKeyToken := api.TokenWith(t, "user_1", map[string]any{"exp": now.Add(3 * time.Hour).Unix()})
	_, err := c.VerifyToken(context.Background(), retiredKeyToken)
	require.NoError(t, err)
	require.Equal(t, int64(1), api.JWKSRequests())

	// Clerk rotates its key. A token naming the new key id is refused, with
	// no fetch, until minRefreshInterval has passed since the last fetch.
	api.RotateKey(t)
	newKeyToken := api.TokenWith(t, "user_1", map[string]any{"exp": now.Add(3 * time.Hour).Unix()})
	for range 3 {
		_, err = c.VerifyToken(context.Background(), newKeyToken)
		assert.Error(t, err)
	}
	assert.Equal(t, int64(1), api.JWKSRequests(), "unknown key ids must not refetch within minRefreshInterval")

	now = now.Add(minRefreshInterval)
	_, err = c.VerifyToken(context.Background(), newKeyToken)
	require.NoError(t, err)
	assert.Equal(t, int64(2), api.JWKSRequests())

	// The retired key stays out once the new JWKS replaced it.
	_, err = c.VerifyToken(context.Background(), retiredKeyToken)
	assert.Error(t, err)

	// A cached key is trusted for keyMaxAge only, then fetched again.
	now = now.Add(keyMaxAge)
	_, err = c.VerifyToken(context.Background(), newKeyToken)
	require.NoError(t, err)
	assert.Equal(t, int64(3), api.JWKSRequests())
}

// A key Clerk withdraws stops working within keyMaxAge even if every token
// keeps naming it, so no fetch is ever triggered by an unknown key id.
func TestVerifyToken_WithdrawnKeyExpires(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)
	now := time.Now()
	c.keys.now = func() time.Time { return now }

	withdrawnKeyToken := api.TokenWith(t, "user_1", map[string]any{"exp": now.Add(3 * time.Hour).Unix()})
	_, err := c.VerifyToken(context.Background(), withdrawnKeyToken)
	require.NoError(t, err)

	api.RotateKey(t)
	now = now.Add(keyMaxAge)
	_, err = c.VerifyToken(context.Background(), withdrawnKeyToken)
	assert.Error(t, err)
	assert.Equal(t, int64(2), api.JWKSRequests())
}

// A Backend API outage does not refuse every user: while the JWKS cannot be
// fetched, a cached key stays usable for staleGrace past keyMaxAge.
func TestVerifyToken_CachedKeySurvivesJWKSOutage(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)
	now := time.Now()
	c.keys.now = func() time.Time { return now }
	token := api.TokenWith(t, "user_1", map[string]any{"exp": now.Add(48 * time.Hour).Unix()})

	_, err := c.VerifyToken(context.Background(), token)
	require.NoError(t, err)

	api.SetJWKSDown(true)
	now = now.Add(keyMaxAge)
	_, err = c.VerifyToken(context.Background(), token)
	require.NoError(t, err, "the failed refresh must fall back to the cached key")
	assert.Equal(t, int64(2), api.JWKSRequests())

	// Within minRefreshInterval the fallback holds without a new attempt.
	_, err = c.VerifyToken(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, int64(2), api.JWKSRequests())

	// Past the grace period the cached key is no longer used.
	now = now.Add(staleGrace)
	_, err = c.VerifyToken(context.Background(), token)
	assert.Error(t, err)

	// Once the Backend API answers again, the key is fetched and trusted.
	api.SetJWKSDown(false)
	now = now.Add(minRefreshInterval)
	_, err = c.VerifyToken(context.Background(), token)
	assert.NoError(t, err)
}

// A request waiting for another one's JWKS fetch gives up with its context.
func TestVerifyToken_WaitForFetchEndsWithContext(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)
	c.keys.fetchSem <- struct{}{} // a fetch in flight
	t.Cleanup(func() { <-c.keys.fetchSem })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.VerifyToken(ctx, api.Token(t, "user_1"))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Zero(t, api.JWKSRequests())
}

func unsignedToken() string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"sub":"user_1","iss":"https://clerk.clerktest.example"}`)) + "."
}

func TestMiddleware(t *testing.T) {
	api := clerktest.NewAPI(t)
	c := newTestClient(t, api)

	var gotSubject string
	handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		require.True(t, ok)
		gotSubject = claims.Subject
		w.WriteHeader(http.StatusOK)
	}))

	token := api.Token(t, "user_1")
	tests := []struct {
		name          string
		authorization string
		want          int
	}{
		{"valid token", "Bearer " + token, http.StatusOK},
		// clerk-sdk-go's extractor trimmed the header and an optional "Bearer ".
		{"valid token without Bearer prefix", token, http.StatusOK},
		{"valid token with surrounding whitespace", "  Bearer " + token + "  ", http.StatusOK},
		// The answers clerk-sdk-go's RequireHeaderAuthorization gave.
		{"no token", "", http.StatusForbidden},
		{"token that does not parse", "Bearer not-a-jwt", http.StatusForbidden},
		{"refused token", "Bearer " + api.TokenSignedByStranger(t, "user_1"), http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSubject = ""
			r := httptest.NewRequest(http.MethodGet, "/users", nil)
			if tt.authorization != "" {
				r.Header.Set("Authorization", tt.authorization)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			assert.Equal(t, tt.want, w.Code)
			if tt.want == http.StatusOK {
				assert.Equal(t, "user_1", gotSubject)
			} else {
				assert.Empty(t, gotSubject, "a refused request must not reach the handler")
			}
		})
	}
}

func TestUserPublicMetadata(t *testing.T) {
	api := clerktest.NewAPI(t)
	api.SetUser("user_1", `{"role":"admin"}`)
	c := newTestClient(t, api)

	metadata, err := c.UserPublicMetadata(context.Background(), "user_1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"role":"admin"}`, string(metadata))

	_, err = c.UserPublicMetadata(context.Background(), "user_unknown")
	assert.Error(t, err, "a 404 must be an error")

	wrongKey, err := New(api.URL, "sk_test_wrong")
	require.NoError(t, err)
	_, err = wrongKey.UserPublicMetadata(context.Background(), "user_1")
	assert.Error(t, err, "a 401 must be an error")
}

// The secret key rides on every Backend API request, so a redirect must not
// carry it anywhere else.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	var leaked bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = true
	}))
	t.Cleanup(elsewhere.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)

	c, err := New(redirecting.URL+"/v1", clerktest.SecretKey)
	require.NoError(t, err)

	_, err = c.UserPublicMetadata(context.Background(), "user_1")
	assert.Error(t, err)
	assert.False(t, leaked, "the redirect target must not be called")
}

func TestNew_RefusesInvalidAPIURL(t *testing.T) {
	_, err := New("not a url", clerktest.SecretKey)
	assert.Error(t, err)
}
