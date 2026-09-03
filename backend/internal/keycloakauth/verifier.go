// Package keycloakauth validates bearer JWTs issued by the Keycloak
// `gno-world` realm and exposes the claims this backend cares about. It is
// the replacement for the previous clerk-sdk-go/v2-based verification, and
// runs alongside it while `auth_provider` can still be flipped back to
// "clerk" (see internal/api/auth.go).
//
// EffectiveUserID() preserves backward compatibility with every user_id
// already stored in this service's database: those rows were keyed by
// Clerk's `sub` claim. The Keycloak realm carries that original value
// forward as a `clerk_user_id` custom claim (set by the migration sync tool
// on import — see the `keycloak` repo's sync system plan) for every user
// migrated from Clerk. Users created directly in Keycloak *after* full
// cutover won't have that claim, so EffectiveUserID falls back to Keycloak's
// own `sub` for them — which is fine, since no legacy rows reference them
// under a Clerk id.
package keycloakauth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// PanelClientID is this backend's own Keycloak client. Admin authorization is
// scoped to it specifically — a client role, not a realm role — because
// gno-world is shared with memba and gnolove, and being admin on one must not
// imply admin on the others. See
// docs/2026-09-02-samourai-lasuite-realm-naming.md §4 in the keycloak repo.
const PanelClientID = "gnomonitoring-panel"

// adminRole is the client role name that grants access to /admin/*.
const adminRole = "admin"

// ClientRoles mirrors one entry of the standard OIDC "resource_access" claim
// (and the shape of "realm_access"): the roles the token bearer holds in one
// specific scope.
type ClientRoles struct {
	Roles []string `json:"roles"`
}

// Claims is the subset of a Keycloak access token this backend reads.
type Claims struct {
	Subject     string `json:"sub"`
	ClerkUserID string `json:"clerk_user_id"`
	Email       string `json:"email"`
	// RealmAccess is decoded for logging/debugging only. It is deliberately
	// NOT consulted by IsAdmin: see PanelClientID.
	RealmAccess    ClientRoles            `json:"realm_access"`
	ResourceAccess map[string]ClientRoles `json:"resource_access"`
}

// IsAdmin reports whether the token carries the `admin` role on the
// gnomonitoring-panel client. A realm-wide `admin` role, or an `admin` role on
// another client in the same realm, does not count.
func (c *Claims) IsAdmin() bool {
	for _, r := range c.ResourceAccess[PanelClientID].Roles {
		if r == adminRole {
			return true
		}
	}
	return false
}

// EffectiveUserID returns the id this backend's database rows are keyed by:
// the original Clerk user id when the token carries one, else Keycloak's sub.
func (c *Claims) EffectiveUserID() string {
	if c.ClerkUserID != "" {
		return c.ClerkUserID
	}
	return c.Subject
}

// Verifier validates access tokens against the realm's published JWKS.
type Verifier struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
}

// New fetches the realm's OIDC discovery document (JWKS included) and returns
// a Verifier. issuerURL is e.g.
// "https://auth.samourai.app/realms/gno-world".
func New(ctx context.Context, issuerURL string) (*Verifier, error) {
	issuerURL = strings.TrimSpace(issuerURL)
	if issuerURL == "" {
		return nil, fmt.Errorf("keycloakauth: keycloak_issuer is empty")
	}
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("keycloakauth: discover issuer %s: %w", issuerURL, err)
	}
	// SkipClientIDCheck: this backend accepts tokens minted for any client in
	// the realm (gnomonitoring-panel today, memba-web and gnolove-web once
	// those cut over) rather than pinning to one audience — matching
	// clerkhttp's prior behaviour of trusting any valid session token from the
	// shared Clerk app. Authorization is enforced separately, and per client,
	// by Claims.IsAdmin.
	verifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return &Verifier{provider: provider, verifier: verifier}, nil
}

// VerifyToken validates a raw bearer token's signature, issuer and expiry and
// returns its decoded claims. It writes nothing and is the primitive the
// dual-accept middleware needs in order to fall back to Clerk on failure.
func (v *Verifier) VerifyToken(ctx context.Context, rawToken string) (*Claims, error) {
	idToken, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("keycloakauth: verify token: %w", err)
	}
	var claims Claims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("keycloakauth: decode claims: %w", err)
	}
	return &claims, nil
}

type contextKey struct{}

// Middleware validates the Authorization: Bearer <token> header and, on
// success, stores the parsed Claims on the request context for downstream
// handlers (ClaimsFromContext). On failure it writes 401 itself and does not
// call next — mirroring clerkhttp.RequireHeaderAuthorization's behaviour so
// the existing corsThenAuth wrapping in api.go/api-admin.go needs no
// structural change, only a different `protect` function.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawToken, ok := BearerToken(r)
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		claims, err := v.VerifyToken(r.Context(), rawToken)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(NewClaimsContext(r.Context(), claims)))
	})
}

// BearerToken extracts the raw token from an "Authorization: Bearer <token>"
// header. The scheme match is case-insensitive, as RFC 7235 requires.
func BearerToken(r *http.Request) (string, bool) {
	const prefix = "bearer "
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) < len(prefix) || !strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(authHeader[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// NewClaimsContext stores claims on ctx. Exported so that tests in other
// packages — and any future middleware — can simulate an already-authenticated
// request without a real token.
func NewClaimsContext(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// ClaimsFromContext returns the Claims stored by Middleware, if any.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(contextKey{}).(*Claims)
	return c, ok
}
