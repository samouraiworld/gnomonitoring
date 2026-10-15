package keycloakauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// issuerClient bounds every request to the issuer: the discovery and, since
// go-oidc keeps the client of the discovery context, the later JWKS fetches.
// Without it go-oidc uses http.DefaultClient, which has no timeout, so an
// issuer whose packets are dropped would hang a request forever.
var issuerClient = &http.Client{Timeout: 15 * time.Second}

// retryAfterSeconds is the Retry-After header of the 503 answered while the
// issuer has not been discovered yet.
const retryAfterSeconds = "30"

// ErrNotReady is returned by LazyVerifier.VerifyToken, until issuer discovery
// succeeds, for a token that claims to come from the issuer. Its signature
// cannot be checked yet, so the caller should answer 503, not 401.
var ErrNotReady = errors.New("keycloakauth: issuer discovery has not succeeded yet")

// errForeignToken is returned by LazyVerifier.VerifyToken, until issuer
// discovery succeeds, for a token that does not claim to come from the
// issuer: it can be rejected (or handed to another provider) at once.
var errForeignToken = errors.New("keycloakauth: issuer not discovered yet and token not issued by it")

// Backoff bounds the delay between two discovery attempts: it starts at
// Initial and doubles after each failure, up to Max.
type Backoff struct {
	Initial time.Duration
	Max     time.Duration
}

// DefaultBackoff is the retry schedule used in production.
var DefaultBackoff = Backoff{Initial: 5 * time.Second, Max: 5 * time.Minute}

// LazyVerifier is a Verifier that may not exist yet. An unreachable issuer at
// boot used to make the whole backend exit, taking monitoring and alerting
// down with it. Discover instead returns a LazyVerifier that keeps retrying
// discovery in the background; until it succeeds, authenticated requests get
// 503 rather than 401, since the token was never actually checked.
type LazyVerifier struct {
	issuer   string
	verifier atomic.Pointer[Verifier]
}

// Discover runs a first OIDC discovery of issuerURL synchronously, so a
// reachable issuer is ready as soon as Discover returns, exactly as New. When
// that attempt fails, Discover logs it, returns a LazyVerifier that is not
// ready yet, and retries in the background with backoff until one attempt
// succeeds or ctx is done.
//
// An empty or malformed issuerURL is an error: it is a configuration mistake
// that no retry can fix.
func Discover(ctx context.Context, issuerURL string, allowedClients []string, backoff Backoff) (*LazyVerifier, error) {
	issuerURL = strings.TrimSpace(issuerURL)
	if issuerURL == "" {
		return nil, fmt.Errorf("keycloakauth: keycloak_issuer is empty")
	}
	if u, err := url.Parse(issuerURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("keycloakauth: keycloak_issuer %q is not an http(s) URL", issuerURL)
	}
	if backoff.Initial <= 0 {
		backoff.Initial = DefaultBackoff.Initial
	}
	backoff.Max = max(backoff.Max, backoff.Initial)
	l := &LazyVerifier{issuer: issuerURL}
	err := l.attempt(ctx, issuerURL, allowedClients)
	if err == nil {
		return l, nil
	}
	log.Printf("[auth] ERROR: Keycloak issuer discovery failed: %v; authenticated routes answer 503 "+
		"until it succeeds, next attempt in %s", err, backoff.Initial)
	go l.retry(ctx, issuerURL, allowedClients, backoff)
	return l, nil
}

// attempt runs one bounded discovery and, on success, publishes the Verifier.
func (l *LazyVerifier) attempt(ctx context.Context, issuerURL string, allowedClients []string) error {
	v, err := New(oidc.ClientContext(ctx, issuerClient), issuerURL, allowedClients)
	if err != nil {
		return err
	}
	l.verifier.Store(v)
	return nil
}

func (l *LazyVerifier) retry(ctx context.Context, issuerURL string, allowedClients []string, backoff Backoff) {
	delay := backoff.Initial
	for attempt := 2; ; attempt++ {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		err := l.attempt(ctx, issuerURL, allowedClients)
		if err == nil {
			log.Printf("[auth] Keycloak issuer discovery succeeded on attempt %d; authenticated routes enabled", attempt)
			return
		}
		delay = min(delay*2, backoff.Max)
		log.Printf("[auth] ERROR: Keycloak issuer discovery attempt %d failed: %v; next attempt in %s",
			attempt, err, delay)
	}
}

// Ready reports whether issuer discovery has succeeded.
func (l *LazyVerifier) Ready() bool {
	return l.verifier.Load() != nil
}

// VerifyToken is Verifier.VerifyToken once discovery has succeeded. Before
// that, it returns ErrNotReady for a token whose (unverified) "iss" claim
// names the issuer, and another error for any other token.
func (l *LazyVerifier) VerifyToken(ctx context.Context, rawToken string) (*Claims, error) {
	v := l.verifier.Load()
	if v == nil {
		if unverifiedIssuer(rawToken) == l.issuer {
			return nil, ErrNotReady
		}
		return nil, errForeignToken
	}
	return v.VerifyToken(ctx, rawToken)
}

// unverifiedIssuer returns the "iss" claim of a JWT without checking its
// signature, or "" when rawToken is not a JWT. It only chooses between 503
// and another provider; it never authenticates anything.
func unverifiedIssuer(rawToken string) string {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Issuer
}

// WriteUnavailable answers 503 with a Retry-After header, for a request whose
// token cannot be checked because the issuer has not been discovered yet.
func WriteUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", retryAfterSeconds)
	http.Error(w, "Authentication provider unavailable", http.StatusServiceUnavailable)
}

// Middleware is Verifier.Middleware once discovery has succeeded. Before that,
// it answers 503 with a Retry-After header and does not call next.
func (l *LazyVerifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := l.verifier.Load()
		if v == nil {
			WriteUnavailable(w)
			return
		}
		v.authenticate(w, r, next)
	})
}
