package api

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/clerkauth"
	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth"
)

// middleware is the shape both Clerk's and Keycloak's request protection take:
// a wrapper that either writes 401 itself or calls next with authenticated
// claims on the request context.
type middleware func(http.Handler) http.Handler

// authSetup holds the middlewares the route registration functions need. It is
// built once at startup by buildAuthSetup, from internal.Config.
type authSetup struct {
	// general protects the non-admin routes (/webhooks/*, /users,
	// /alert-contacts, /usersH). In Keycloak mode with the Clerk fallback on,
	// this accepts either token type — see buildAuthSetup.
	general middleware
	// admin protects /admin/*. It is always single-provider: the panel is the
	// only caller, and it cuts over in the same deploy as this backend.
	admin middleware
	// adminRole gates on the admin role, after admin has authenticated.
	adminRole middleware
}

// clerkAPIURL is Clerk's Backend API; tests point it at a clerktest.API.
var clerkAPIURL = clerkauth.DefaultAPIURL

// buildAuthSetup wires the middlewares for the configured auth_provider.
//
// The asymmetry between `general` and `admin` is deliberate, and is the whole
// point of this function. /admin/* is called only by this repo's own panel,
// which cuts over to Keycloak in the same deploy as this backend, so it can be
// strict immediately. The general routes are also called by memba's /alerts
// page and gnolove's leaderboard-webhooks route using tokens from *those apps'*
// own Clerk sessions; those apps migrate on their own schedule, so until they
// do, the general routes must accept both token types or they break with 401s.
func buildAuthSetup(ctx context.Context) (*authSetup, error) {
	// Clerk is needed whenever any Clerk path can still run: in Clerk mode, and
	// in Keycloak mode while the general-route fallback is on. Built
	// unconditionally; it makes no request before the first Clerk token.
	clerkClient, err := clerkauth.New(clerkAPIURL, internal.Config.ClerkSecretKey)
	if err != nil {
		return nil, err
	}

	if internal.Config.AuthProvider != internal.AuthProviderKeycloak {
		clerkProtect := middleware(clerkClient.Middleware)
		return &authSetup{
			general:   clerkProtect,
			admin:     clerkProtect,
			adminRole: adminRoleMiddleware(clerkClient),
		}, nil
	}

	// An unreachable issuer does not fail startup: monitoring and alerting
	// must keep running. Discover retries in the background, and until it
	// succeeds the Keycloak-protected routes answer 503.
	verifier, err := keycloakauth.Discover(ctx, internal.Config.KeycloakIssuer,
		internal.Config.KeycloakAllowedClients, keycloakauth.DefaultBackoff)
	if err != nil {
		return nil, err
	}

	general := middleware(verifier.Middleware)
	if internal.Config.ClerkFallbackEnabled() {
		if internal.Config.ClerkSecretKey == "" {
			log.Printf("[auth] WARNING: keycloak_clerk_fallback is on but clerk_secret_key is empty; " +
				"memba and gnolove callers presenting Clerk tokens will get 401")
		}
		general = dualAccept(verifier, clerkClient.Middleware)
		log.Printf("[auth] general routes accept Keycloak tokens, falling back to Clerk")
	} else {
		log.Printf("[auth] general routes accept Keycloak tokens only (Clerk fallback disabled)")
	}

	return &authSetup{
		general:   general,
		admin:     verifier.Middleware,
		adminRole: keycloakAdminRoleMiddleware,
	}, nil
}

// tokenVerifier is the part of keycloakauth.Verifier and LazyVerifier that
// dualAccept needs.
type tokenVerifier interface {
	VerifyToken(ctx context.Context, rawToken string) (*keycloakauth.Claims, error)
}

// dualAccept tries Keycloak verification first and delegates to Clerk's
// middleware when the bearer token is absent or is not a valid Keycloak token.
// Clerk's middleware is the last link, so it — not this wrapper — writes the
// 401 when neither provider accepts the request, preserving the exact response
// the general routes returned before the cutover.
//
// A Keycloak token that verifies never reaches Clerk, so the fallback costs a
// Keycloak-authenticated request nothing. While the Keycloak issuer has not
// been discovered yet, a token issued by it gets 503 and every other token
// goes to Clerk, so Clerk callers keep working.
func dualAccept(verifier tokenVerifier, clerkProtect func(http.Handler) http.Handler) middleware {
	return func(next http.Handler) http.Handler {
		clerkChain := clerkProtect(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rawToken, ok := keycloakauth.BearerToken(r); ok {
				claims, err := verifier.VerifyToken(r.Context(), rawToken)
				if err == nil {
					next.ServeHTTP(w, r.WithContext(keycloakauth.NewClaimsContext(r.Context(), claims)))
					return
				}
				// A Keycloak token that cannot be checked yet is neither valid
				// nor invalid: Clerk would answer 401, and a Keycloak client
				// would take that for a bad session.
				if errors.Is(err, keycloakauth.ErrNotReady) {
					keycloakauth.WriteUnavailable(w)
					return
				}
			}
			clerkChain.ServeHTTP(w, r)
		})
	}
}

// keycloakAdminRoleMiddleware replaces the Clerk path's live API call
// (a Clerk user lookup + publicMetadata.role check) with a role claim already present
// in the validated token — no network round-trip per admin request. Must be
// chained after a middleware that put Keycloak claims on the context.
func keycloakAdminRoleMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := keycloakauth.ClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if !claims.IsAdmin() {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
