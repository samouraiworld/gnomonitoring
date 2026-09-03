package api

import (
	"context"
	"log"
	"net/http"

	clerk "github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
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
	// Clerk's key is needed whenever any Clerk path can still run: in Clerk
	// mode, and in Keycloak mode while the general-route fallback is on.
	if internal.Config.ClerkSecretKey != "" {
		clerk.SetKey(internal.Config.ClerkSecretKey)
	}

	if internal.Config.AuthProvider != internal.AuthProviderKeycloak {
		clerkProtect := middleware(clerkhttp.RequireHeaderAuthorization())
		return &authSetup{
			general:   clerkProtect,
			admin:     clerkProtect,
			adminRole: adminRoleMiddleware,
		}, nil
	}

	verifier, err := keycloakauth.New(ctx, internal.Config.KeycloakIssuer)
	if err != nil {
		return nil, err
	}

	general := middleware(verifier.Middleware)
	if internal.Config.ClerkFallbackEnabled() {
		if internal.Config.ClerkSecretKey == "" {
			log.Printf("[auth] WARNING: keycloak_clerk_fallback is on but clerk_secret_key is empty; " +
				"memba and gnolove callers presenting Clerk tokens will get 401")
		}
		general = dualAccept(verifier, clerkhttp.RequireHeaderAuthorization())
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

// dualAccept tries Keycloak verification first and delegates to Clerk's
// middleware when the bearer token is absent or is not a valid Keycloak token.
// Clerk's middleware is the last link, so it — not this wrapper — writes the
// 401 when neither provider accepts the request, preserving the exact response
// the general routes returned before the cutover.
//
// A Keycloak token that verifies never reaches Clerk, so the fallback costs a
// Keycloak-authenticated request nothing.
func dualAccept(verifier *keycloakauth.Verifier, clerkProtect func(http.Handler) http.Handler) middleware {
	return func(next http.Handler) http.Handler {
		clerkChain := clerkProtect(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rawToken, ok := keycloakauth.BearerToken(r); ok {
				if claims, err := verifier.VerifyToken(r.Context(), rawToken); err == nil {
					next.ServeHTTP(w, r.WithContext(keycloakauth.NewClaimsContext(r.Context(), claims)))
					return
				}
			}
			clerkChain.ServeHTTP(w, r)
		})
	}
}

// keycloakAdminRoleMiddleware replaces the Clerk path's live API call
// (clerkuser.Get + publicMetadata.role check) with a role claim already present
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
