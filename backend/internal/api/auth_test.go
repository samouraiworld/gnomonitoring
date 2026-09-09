package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth"
	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth/kctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestVerifier(t *testing.T, realm *kctest.Realm) *keycloakauth.Verifier {
	t.Helper()
	v, err := keycloakauth.New(context.Background(), realm.Issuer, []string{keycloakauth.PanelClientID})
	require.NoError(t, err)
	return v
}

// ---------------------------------------------------------------------------
// keycloakAdminRoleMiddleware
// ---------------------------------------------------------------------------

func TestKeycloakAdminRoleMiddleware_AllowsAdmin(t *testing.T) {
	ctx := keycloakauth.NewClaimsContext(context.Background(), &keycloakauth.Claims{
		Subject: "kc-sub-1",
		ResourceAccess: map[string]keycloakauth.ClientRoles{
			keycloakauth.PanelClientID: {Roles: []string{"admin"}},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	keycloakAdminRoleMiddleware(dummyOKHandler).ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestKeycloakAdminRoleMiddleware_RejectsNonAdmin(t *testing.T) {
	ctx := keycloakauth.NewClaimsContext(context.Background(), &keycloakauth.Claims{Subject: "kc-sub-1"})
	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	keycloakAdminRoleMiddleware(dummyOKHandler).ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// An admin role on another client in the shared gno-world realm must not open
// gnomonitoring's admin panel — that is the whole reason the role is
// client-scoped rather than realm-wide.
func TestKeycloakAdminRoleMiddleware_RejectsOtherClientAdmin(t *testing.T) {
	ctx := keycloakauth.NewClaimsContext(context.Background(), &keycloakauth.Claims{
		Subject:        "kc-sub-1",
		RealmAccess:    keycloakauth.ClientRoles{Roles: []string{"admin"}},
		ResourceAccess: map[string]keycloakauth.ClientRoles{"memba-web": {Roles: []string{"admin"}}},
	})
	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	keycloakAdminRoleMiddleware(dummyOKHandler).ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestKeycloakAdminRoleMiddleware_RejectsMissingClaims(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	w := httptest.NewRecorder()

	keycloakAdminRoleMiddleware(dummyOKHandler).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ---------------------------------------------------------------------------
// dualAccept — the bridge that keeps memba and gnolove working during cutover
// ---------------------------------------------------------------------------

func TestDualAccept_AcceptsKeycloakToken(t *testing.T) {
	realm := kctest.NewRealm(t)
	verifier := newTestVerifier(t, realm)

	var seenUserID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := keycloakauth.ClaimsFromContext(r.Context())
		require.True(t, ok, "dualAccept must put the Keycloak claims on the request context")
		seenUserID = claims.EffectiveUserID()
		w.WriteHeader(http.StatusOK)
	})

	handler := dualAccept(verifier, clerkhttp.RequireHeaderAuthorization())(next)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("Authorization", "Bearer "+realm.Token(t, "kc-sub-1", map[string]any{"clerk_user_id": "user_2abc"}))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// A migrated user's rows are keyed by their original Clerk id, so that is
	// what the handler must see — not the Keycloak sub.
	assert.Equal(t, "user_2abc", seenUserID)
}

// A token this Keycloak realm does not recognise (a memba Clerk session token,
// in production) must be handed to the Clerk middleware rather than rejected
// outright. Clerk rejects this particular one because it is not a Clerk token
// either — what matters is that the request reached Clerk's middleware and that
// the Keycloak claims were not set.
func TestDualAccept_FallsBackToClerkForNonKeycloakToken(t *testing.T) {
	realm := kctest.NewRealm(t)
	verifier := newTestVerifier(t, realm)

	clerkReached := false
	fakeClerk := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clerkReached = true
			_, hasKC := keycloakauth.ClaimsFromContext(r.Context())
			assert.False(t, hasKC, "the Clerk fallback must not see Keycloak claims on the context")
			next.ServeHTTP(w, r)
		})
	}

	handler := dualAccept(verifier, fakeClerk)(dummyOKHandler)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("Authorization", "Bearer this-is-a-clerk-token-not-a-keycloak-one")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.True(t, clerkReached, "a token Keycloak rejects must be offered to Clerk")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestDualAccept_NoTokenIsRejectedByClerk(t *testing.T) {
	realm := kctest.NewRealm(t)
	verifier := newTestVerifier(t, realm)

	handler := dualAccept(verifier, clerkhttp.RequireHeaderAuthorization())(dummyOKHandler)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusOK, w.Code, "an unauthenticated request must not reach the handler")
}

// ---------------------------------------------------------------------------
// authUserIDFromContext
// ---------------------------------------------------------------------------

func TestAuthUserIDFromContext_Keycloak(t *testing.T) {
	prevDevMode := internal.Config.DevMode
	internal.Config.DevMode = false
	defer func() { internal.Config.DevMode = prevDevMode }()

	t.Run("prefers the clerk_user_id so legacy rows still resolve", func(t *testing.T) {
		ctx := keycloakauth.NewClaimsContext(context.Background(), &keycloakauth.Claims{
			Subject: "kc-sub-1", ClerkUserID: "user_2abc",
		})
		req := httptest.NewRequest(http.MethodGet, "/users", nil).WithContext(ctx)

		got, err := authUserIDFromContext(req)

		require.NoError(t, err)
		assert.Equal(t, "user_2abc", got)
	})

	t.Run("falls back to sub for users created after the cutover", func(t *testing.T) {
		ctx := keycloakauth.NewClaimsContext(context.Background(), &keycloakauth.Claims{Subject: "kc-sub-2"})
		req := httptest.NewRequest(http.MethodGet, "/users", nil).WithContext(ctx)

		got, err := authUserIDFromContext(req)

		require.NoError(t, err)
		assert.Equal(t, "kc-sub-2", got)
	})

	t.Run("errors when no provider authenticated the request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)

		_, err := authUserIDFromContext(req)

		assert.Error(t, err)
	})
}

func TestAuthUserIDFromContext_DevModeUnchanged(t *testing.T) {
	prevDevMode := internal.Config.DevMode
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = prevDevMode }()

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("X-Debug-UserID", "debug-user")
	got, err := authUserIDFromContext(req)
	require.NoError(t, err)
	assert.Equal(t, "debug-user", got)

	got, err = authUserIDFromContext(httptest.NewRequest(http.MethodGet, "/users", nil))
	require.NoError(t, err)
	assert.Equal(t, "local-dev-user", got)
}

// ---------------------------------------------------------------------------
// buildAuthSetup
// ---------------------------------------------------------------------------

func TestBuildAuthSetup_ClerkModeIsUnchanged(t *testing.T) {
	prev := internal.Config
	defer func() { internal.Config = prev }()
	internal.Config.AuthProvider = internal.AuthProviderClerk
	internal.Config.ClerkSecretKey = "sk_test_dummy"

	auth, err := buildAuthSetup(context.Background())
	require.NoError(t, err)

	// A request with no Authorization header must still be rejected by the
	// Clerk middleware exactly as before this change.
	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	w := httptest.NewRecorder()
	auth.admin(auth.adminRole(dummyOKHandler)).ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusOK, w.Code)
}

func TestBuildAuthSetup_KeycloakModeRejectsUnreachableIssuer(t *testing.T) {
	prev := internal.Config
	defer func() { internal.Config = prev }()
	internal.Config.AuthProvider = internal.AuthProviderKeycloak
	internal.Config.KeycloakIssuer = "http://127.0.0.1:1/realms/gno-world"

	_, err := buildAuthSetup(context.Background())

	assert.Error(t, err, "startup must fail loudly rather than silently serving unauthenticated")
}

func TestBuildAuthSetup_KeycloakMode(t *testing.T) {
	realm := kctest.NewRealm(t)

	prev := internal.Config
	defer func() { internal.Config = prev }()
	internal.Config.AuthProvider = internal.AuthProviderKeycloak
	internal.Config.KeycloakIssuer = realm.Issuer
	internal.Config.KeycloakAllowedClients = []string{keycloakauth.PanelClientID}
	internal.Config.ClerkSecretKey = "sk_test_dummy"
	internal.Config.DevMode = false

	fallbackOff := false
	internal.Config.KeycloakClerkFallback = &fallbackOff

	auth, err := buildAuthSetup(context.Background())
	require.NoError(t, err)

	adminChain := auth.admin(auth.adminRole(dummyOKHandler))

	t.Run("admin token reaches /admin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
		req.Header.Set("Authorization", "Bearer "+realm.Token(t, "kc-sub-1", map[string]any{
			"resource_access": kctest.AdminResourceAccess(),
		}))
		w := httptest.NewRecorder()
		adminChain.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("authenticated non-admin gets 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
		req.Header.Set("Authorization", "Bearer "+realm.Token(t, "kc-sub-2", map[string]any{
			"resource_access": kctest.NonAdminResourceAccess(),
		}))
		w := httptest.NewRecorder()
		adminChain.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("no token gets 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
		w := httptest.NewRecorder()
		adminChain.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("preflight still gets CORS headers before auth runs", func(t *testing.T) {
		internal.Config.AllowedOrigins = []string{"https://panel.example.com"}
		defer func() { internal.Config.AllowedOrigins = nil }()

		req := httptest.NewRequest(http.MethodOptions, "/users", nil)
		req.Header.Set("Origin", "https://panel.example.com")
		w := httptest.NewRecorder()

		corsThenAuth(dummyOKHandler, auth.general).ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "https://panel.example.com", w.Header().Get("Access-Control-Allow-Origin"))
	})
}
