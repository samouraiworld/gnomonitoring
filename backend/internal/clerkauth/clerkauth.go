// Package clerkauth verifies Clerk session tokens and reads a Clerk user's
// public metadata through Clerk's Backend API. It replaces the two parts of
// clerk-sdk-go/v2 this backend used, http.RequireHeaderAuthorization and
// user.Get, and keeps their behaviour.
//
// clerk-sdk-go parses tokens with go-jose v3, which has no fix for
// CVE-2026-116587: parsing a JWS whose "jwk" header embeds a crafted RSA
// private key burns CPU before any signature check, so any caller could stall
// the API without credentials. Tokens are checked here with go-jose v4, which
// refuses such a key while parsing (fixed in v4.1.6).
package clerkauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// DefaultAPIURL is Clerk's Backend API base URL.
const DefaultAPIURL = "https://api.clerk.com/v1"

// apiVersion pins the Backend API version clerk-sdk-go v2.4.0 sent.
const apiVersion = "2025-04-10"

// requestTimeout bounds every Backend API request, as clerk-sdk-go's default
// client did. http.DefaultClient has no timeout.
const requestTimeout = 5 * time.Second

// maxUserResponseBytes caps the user lookup response read into memory.
const maxUserResponseBytes = 1 << 20

// Claims is the part of a Clerk session token this backend reads.
type Claims struct {
	Issuer          string `json:"iss"`
	Subject         string `json:"sub"`
	SessionID       string `json:"sid"`
	AuthorizedParty string `json:"azp"`
}

// Client verifies session tokens against the instance's JWKS and looks users
// up, both through the Backend API authenticated with the secret key.
type Client struct {
	apiURL    string
	secretKey string
	http      *http.Client
	keys      *keySet
}

// errMalformed marks a token that is not a compact RS256 JWS at all.
var errMalformed = errors.New("clerkauth: malformed token")

// New returns a Client for the Backend API at apiURL (DefaultAPIURL outside
// tests). It makes no request: the JWKS is fetched on the first token (see
// keySet for when it is fetched again).
func New(apiURL, secretKey string) (*Client, error) {
	apiURL = strings.TrimRight(apiURL, "/")
	if u, err := url.Parse(apiURL); err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("clerkauth: invalid Backend API URL %q", apiURL)
	}

	c := &Client{apiURL: apiURL, secretKey: secretKey}
	c.http = &http.Client{
		Timeout:   requestTimeout,
		Transport: apiTransport{secretKey: secretKey},
		// Every request carries the secret key, and this client only ever
		// calls apiURL: never follow a redirect elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	c.keys = &keySet{url: apiURL + "/jwks", client: c.http, now: time.Now, fetchSem: make(chan struct{}, 1)}
	return c, nil
}

// apiTransport authenticates every request with the secret key.
type apiTransport struct {
	secretKey string
}

func (t apiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.secretKey)
	req.Header.Set("Clerk-API-Version", apiVersion)
	return http.DefaultTransport.RoundTrip(req)
}

// VerifyToken checks a session token's signature, expiry and issuer and
// returns its claims, with clerk-sdk-go's checks: an RS256 signature by a key
// of the instance's JWKS, named by kid, and exp, nbf and iat with no leeway.
// A Clerk session token has no audience, and its issuer is the instance's
// Frontend API URL, which this backend is not configured with: the signature
// is what ties the token to this instance. It writes nothing.
func (c *Client) VerifyToken(ctx context.Context, rawToken string) (*Claims, error) {
	// Without a key the Backend API refuses the JWKS request anyway.
	if c.secretKey == "" {
		return nil, errors.New("clerkauth: clerk_secret_key is empty")
	}
	token, err := jwt.ParseSigned(rawToken, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformed, err)
	}
	kid := token.Headers[0].KeyID
	if kid == "" {
		return nil, errors.New("clerkauth: token names no key (kid)")
	}
	key, err := c.keys.key(ctx, kid)
	if err != nil {
		return nil, err
	}

	var registered jwt.Claims
	var claims Claims
	if err := token.Claims(key, &registered, &claims); err != nil {
		return nil, fmt.Errorf("clerkauth: verify token: %w", err)
	}
	// go-jose skips the expiry check when exp is absent; Clerk always sets it.
	if registered.Expiry == nil {
		return nil, errors.New("clerkauth: token has no expiry")
	}
	if err := registered.ValidateWithLeeway(jwt.Expected{Time: c.keys.now()}, 0); err != nil {
		return nil, fmt.Errorf("clerkauth: validate claims: %w", err)
	}
	if !isClerkIssuer(claims.Issuer) {
		return nil, fmt.Errorf("clerkauth: invalid issuer %q", claims.Issuer)
	}
	if claims.Subject == "" {
		return nil, errors.New("clerkauth: token has no subject")
	}
	return &claims, nil
}

// isClerkIssuer is clerk-sdk-go's issuer check for an instance that is
// neither a satellite nor behind a proxy: a production Frontend API
// (https://clerk.<domain>) or a development one (*.clerk.accounts.dev).
func isClerkIssuer(iss string) bool {
	return strings.HasPrefix(iss, "https://clerk.") || strings.Contains(iss, ".clerk.accounts")
}

// Middleware validates the request's session token and, on success, stores
// its Claims on the request context (ClaimsFromContext). Otherwise it answers
// as clerk-sdk-go's RequireHeaderAuthorization did, with an empty body: 403
// when there is no token or it does not even parse, 401 when it is refused.
func (c *Client) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawToken := bearerToken(r)
		if rawToken == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		claims, err := c.VerifyToken(r.Context(), rawToken)
		if errors.Is(err, errMalformed) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(NewClaimsContext(r.Context(), claims)))
	})
}

// bearerToken extracts the token as clerk-sdk-go's default extractor did: the
// trimmed Authorization header, without a leading "Bearer ".
func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
}

// UserPublicMetadata returns the public_metadata of the user userID, read
// live from the Backend API (GET /users/{id}).
func (c *Client) UserPublicMetadata(ctx context.Context, userID string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"/users/"+url.PathEscape(userID), nil)
	if err != nil {
		return nil, fmt.Errorf("clerkauth: build user request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clerkauth: get user %s: %w", userID, err)
	}
	defer func() {
		// Drain a little so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clerkauth: get user %s: %s", userID, resp.Status)
	}
	var user struct {
		PublicMetadata json.RawMessage `json:"public_metadata"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxUserResponseBytes)).Decode(&user); err != nil {
		return nil, fmt.Errorf("clerkauth: decode user %s: %w", userID, err)
	}
	return user.PublicMetadata, nil
}

type contextKey struct{}

// NewClaimsContext stores claims on ctx. Exported so that tests in other
// packages can simulate an already-authenticated request without a token.
func NewClaimsContext(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// ClaimsFromContext returns the Claims stored by Middleware, if any.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(contextKey{}).(*Claims)
	return c, ok
}
