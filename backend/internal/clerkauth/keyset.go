package clerkauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// keyMaxAge is how long a key is trusted after the JWKS that published it was
// fetched, as in clerk-sdk-go: past it, the JWKS is fetched again, so a key
// Clerk withdraws stops working within the hour even if every token keeps
// naming it.
const keyMaxAge = time.Hour

// staleGrace is how much longer than keyMaxAge a key stays usable while the
// JWKS cannot be fetched (Backend API down, slow or rate limiting), so a Clerk
// outage does not refuse every user. A key a successful fetch no longer lists
// is refused at once, whatever its age.
const staleGrace = 24 * time.Hour

// minRefreshInterval spaces two JWKS fetches. Only a token naming a key id
// that is not cached triggers a fetch, so a forged token cannot turn every
// request into a Backend API call carrying the secret key, against the rate
// limit the admin lookups share. A genuine token signed by a key published
// since the last fetch is refused for at most this long.
const minRefreshInterval = 10 * time.Second

// maxJWKSResponseBytes caps the JWKS response read into memory.
const maxJWKSResponseBytes = 1 << 20

// keySet is the instance's JWKS, cached by key id.
type keySet struct {
	url    string
	client *http.Client
	now    func() time.Time

	// fetchSem (capacity 1) serializes fetches and guards triedAt and
	// fetchFailed. A request waiting for it gives up when its context ends.
	fetchSem    chan struct{}
	triedAt     time.Time
	fetchFailed bool // the last fetch attempt failed

	mu        sync.RWMutex
	keys      map[string]jose.JSONWebKey
	fetchedAt time.Time
}

// key returns the key named kid, fetching the JWKS when kid is not cached or
// the cache is older than keyMaxAge, at most once per minRefreshInterval.
// While fetches fail, a cached key stays usable for staleGrace more.
func (s *keySet) key(ctx context.Context, kid string) (jose.JSONWebKey, error) {
	if key, ok := s.cached(kid, keyMaxAge); ok {
		return key, nil
	}

	select {
	case s.fetchSem <- struct{}{}:
	case <-ctx.Done():
		return jose.JSONWebKey{}, ctx.Err()
	}
	defer func() { <-s.fetchSem }()
	// Another request may have fetched while this one waited.
	if key, ok := s.cached(kid, keyMaxAge); ok {
		return key, nil
	}
	now := s.now()
	if !s.triedAt.IsZero() && now.Sub(s.triedAt) < minRefreshInterval {
		if key, ok := s.cached(kid, keyMaxAge+staleGrace); ok && s.fetchFailed {
			return key, nil
		}
		return jose.JSONWebKey{}, fmt.Errorf("clerkauth: no current key %q, and the JWKS was fetched less than %s ago", kid, minRefreshInterval)
	}
	s.triedAt = now

	// One request's cancellation must not fail the fetch for the others
	// waiting on it; the client's timeout still bounds it.
	keys, err := s.fetch(context.WithoutCancel(ctx))
	s.fetchFailed = err != nil
	if err != nil {
		// At most one line per minRefreshInterval: the attempts are throttled.
		log.Printf("[clerkauth] %v", err)
		if key, ok := s.cached(kid, keyMaxAge+staleGrace); ok {
			return key, nil
		}
		return jose.JSONWebKey{}, err
	}
	s.mu.Lock()
	s.keys, s.fetchedAt = keys, now
	s.mu.Unlock()

	key, ok := keys[kid]
	if !ok {
		return jose.JSONWebKey{}, fmt.Errorf("clerkauth: the JWKS has no key %q", kid)
	}
	return key, nil
}

// cached returns the key named kid if the JWKS that listed it was fetched
// less than maxAge ago.
func (s *keySet) cached(kid string, maxAge time.Duration) (jose.JSONWebKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.keys == nil || s.now().Sub(s.fetchedAt) >= maxAge {
		return jose.JSONWebKey{}, false
	}
	key, ok := s.keys[kid]
	return key, ok
}

func (s *keySet) fetch(ctx context.Context) (map[string]jose.JSONWebKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, fmt.Errorf("clerkauth: build JWKS request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clerkauth: fetch JWKS: %w", err)
	}
	defer func() {
		// Drain a little so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clerkauth: fetch JWKS: %s", resp.Status)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSResponseBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("clerkauth: decode JWKS: %w", err)
	}
	keys := make(map[string]jose.JSONWebKey, len(set.Keys))
	for _, key := range set.Keys {
		if key.KeyID != "" {
			keys[key.KeyID] = key
		}
	}
	return keys, nil
}
