package keycloakauth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// discoveryTimeout bounds one OIDC discovery attempt. Without it, an issuer
// whose packets are dropped would hang the attempt forever: go-oidc uses
// http.DefaultClient, which has no timeout.
const discoveryTimeout = 15 * time.Second

// retryAfterSeconds is the Retry-After header of the 503 answered while the
// issuer has not been discovered yet.
const retryAfterSeconds = "30"

// ErrNotReady is returned by LazyVerifier.VerifyToken until issuer discovery
// succeeds.
var ErrNotReady = errors.New("keycloakauth: issuer discovery has not succeeded yet")

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
	verifier atomic.Pointer[Verifier]
}

// Discover runs a first OIDC discovery of issuerURL synchronously, so a
// reachable issuer is ready as soon as Discover returns, exactly as New. When
// that attempt fails, Discover logs it, returns a LazyVerifier that is not
// ready yet, and retries in the background with backoff until one attempt
// succeeds or ctx is done.
//
// Only an empty issuerURL is an error: it is a configuration mistake that no
// retry can fix.
func Discover(ctx context.Context, issuerURL string, allowedClients []string, backoff Backoff) (*LazyVerifier, error) {
	if strings.TrimSpace(issuerURL) == "" {
		return nil, fmt.Errorf("keycloakauth: keycloak_issuer is empty")
	}
	l := &LazyVerifier{}
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
	// The timeout is safe to cancel after discovery: go-oidc detaches the
	// key set's later JWKS fetches from this context's cancellation.
	attemptCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	v, err := New(attemptCtx, issuerURL, allowedClients)
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

// VerifyToken is Verifier.VerifyToken, or ErrNotReady before discovery
// succeeds.
func (l *LazyVerifier) VerifyToken(ctx context.Context, rawToken string) (*Claims, error) {
	v := l.verifier.Load()
	if v == nil {
		return nil, ErrNotReady
	}
	return v.VerifyToken(ctx, rawToken)
}

// Middleware is Verifier.Middleware once discovery has succeeded. Before that,
// it answers 503 with a Retry-After header and does not call next.
func (l *LazyVerifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := l.verifier.Load()
		if v == nil {
			w.Header().Set("Retry-After", retryAfterSeconds)
			http.Error(w, "Authentication provider unavailable", http.StatusServiceUnavailable)
			return
		}
		v.authenticate(w, r, next)
	})
}
