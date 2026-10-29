package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/clerkauth"
	"github.com/samouraiworld/gnomonitoring/backend/internal/clerkauth/clerktest"
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

// newTestClerk starts a fake Clerk Backend API and a client pointed at it.
func newTestClerk(t *testing.T) (*clerktest.API, *clerkauth.Client) {
	t.Helper()
	api := clerktest.NewAPI(t)
	c, err := clerkauth.New(api.URL, clerktest.SecretKey)
	require.NoError(t, err)
	return api, c
}

// ---------------------------------------------------------------------------
// adminRoleMiddleware (Clerk)
// ---------------------------------------------------------------------------

func TestAdminRoleMiddleware(t *testing.T) {
	api, clerkClient := newTestClerk(t)
	api.SetUser("user_admin", `{"role":"admin"}`)
	api.SetUser("user_member", `{"role":"member"}`)
	api.SetUser("user_no_metadata", `{}`)

	chain := clerkClient.Middleware(adminRoleMiddleware(clerkClient)(dummyOKHandler))

	tests := []struct {
		name    string
		subject string
		want    int
	}{
		{"admin", "user_admin", http.StatusOK},
		{"other role", "user_member", http.StatusForbidden},
		{"no role", "user_no_metadata", http.StatusForbidden},
		{"user lookup fails", "user_unknown", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
			req.Header.Set("Authorization", "Bearer "+api.Token(t, tt.subject))
			w := httptest.NewRecorder()
			chain.ServeHTTP(w, req)
			assert.Equal(t, tt.want, w.Code)
		})
	}
}

func TestAdminRoleMiddleware_RejectsMissingClaims(t *testing.T) {
	_, clerkClient := newTestClerk(t)

	w := httptest.NewRecorder()
	adminRoleMiddleware(clerkClient)(dummyOKHandler).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/status", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
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

	_, clerkClient := newTestClerk(t)
	handler := dualAccept(verifier, clerkClient.Middleware)(next)

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

// A real Clerk session token that Keycloak rejects is verified by the Clerk
// middleware and reaches the handler with Clerk claims only.
func TestDualAccept_ClerkTokenIsVerifiedByClerk(t *testing.T) {
	realm := kctest.NewRealm(t)
	verifier := newTestVerifier(t, realm)
	api, clerkClient := newTestClerk(t)

	var seenUserID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasKC := keycloakauth.ClaimsFromContext(r.Context())
		assert.False(t, hasKC)
		claims, ok := clerkauth.ClaimsFromContext(r.Context())
		require.True(t, ok)
		seenUserID = claims.Subject
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("Authorization", "Bearer "+api.Token(t, "user_2abc"))
	w := httptest.NewRecorder()
	dualAccept(verifier, clerkClient.Middleware)(next).ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "user_2abc", seenUserID)
}

func TestDualAccept_NoTokenIsRejectedByClerk(t *testing.T) {
	realm := kctest.NewRealm(t)
	verifier := newTestVerifier(t, realm)

	_, clerkClient := newTestClerk(t)
	handler := dualAccept(verifier, clerkClient.Middleware)(dummyOKHandler)

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

func TestAuthUserIDFromContext_Clerk(t *testing.T) {
	prevDevMode := internal.Config.DevMode
	internal.Config.DevMode = false
	defer func() { internal.Config.DevMode = prevDevMode }()

	ctx := clerkauth.NewClaimsContext(context.Background(), &clerkauth.Claims{Subject: "user_2abc"})
	got, err := authUserIDFromContext(httptest.NewRequest(http.MethodGet, "/users", nil).WithContext(ctx))

	require.NoError(t, err)
	assert.Equal(t, "user_2abc", got)
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

// Clerk mode answers as clerk-sdk-go's middleware did: 403 without a token,
// 401 for a refused one, and the admin role read live from the user's
// public metadata.
func TestBuildAuthSetup_ClerkMode(t *testing.T) {
	api := clerktest.NewAPI(t)
	api.SetUser("user_admin", `{"role":"admin"}`)
	api.SetUser("user_member", `{}`)

	prev, prevURL := internal.Config, clerkAPIURL
	defer func() { internal.Config, clerkAPIURL = prev, prevURL }()
	internal.Config.AuthProvider = internal.AuthProviderClerk
	internal.Config.ClerkSecretKey = clerktest.SecretKey
	internal.Config.DevMode = false
	clerkAPIURL = api.URL

	auth, err := buildAuthSetup(context.Background())
	require.NoError(t, err)

	adminChain := auth.admin(auth.adminRole(dummyOKHandler))
	generalChain := auth.general(dummyOKHandler)

	tests := []struct {
		name          string
		chain         http.Handler
		authorization string
		want          int
	}{
		{"admin token reaches /admin", adminChain, "Bearer " + api.Token(t, "user_admin"), http.StatusOK},
		{"non-admin gets 403 on /admin", adminChain, "Bearer " + api.Token(t, "user_member"), http.StatusForbidden},
		{"no token gets 403 on /admin", adminChain, "", http.StatusForbidden},
		{"refused token gets 401 on /admin", adminChain, "Bearer " + api.TokenSignedByStranger(t, "user_admin"), http.StatusUnauthorized},
		{"session token reaches a general route", generalChain, "Bearer " + api.Token(t, "user_member"), http.StatusOK},
		{"no token gets 403 on a general route", generalChain, "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			w := httptest.NewRecorder()
			tt.chain.ServeHTTP(w, req)
			assert.Equal(t, tt.want, w.Code)
		})
	}
}

// An unreachable issuer must not stop startup (monitoring and alerting would go
// down with it), and must never serve a protected route unauthenticated: the
// Keycloak-protected routes answer 503 until discovery succeeds.
func TestBuildAuthSetup_KeycloakModeDegradesOnUnreachableIssuer(t *testing.T) {
	prev := internal.Config
	defer func() { internal.Config = prev }()
	internal.Config.AuthProvider = internal.AuthProviderKeycloak
	internal.Config.KeycloakIssuer = "http://127.0.0.1:1/realms/gno-world"
	internal.Config.ClerkSecretKey = "sk_test_dummy"
	fallbackOff := false
	internal.Config.KeycloakClerkFallback = &fallbackOff

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	auth, err := buildAuthSetup(ctx)
	require.NoError(t, err, "an unreachable issuer must not fail startup")

	for name, chain := range map[string]http.Handler{
		"admin":   auth.admin(auth.adminRole(dummyOKHandler)),
		"general": auth.general(dummyOKHandler),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/users", nil)
			req.Header.Set("Authorization", "Bearer some.unchecked.token")
			w := httptest.NewRecorder()
			chain.ServeHTTP(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})
	}
}

// With the Clerk fallback on, the general routes keep handing non-Keycloak
// tokens to Clerk while the Keycloak issuer is unreachable, so Clerk callers
// keep working. A Keycloak token gets 503: Clerk would answer 401, which a
// Keycloak client would take for a bad session.
func TestDualAccept_WhileIssuerUnreachable(t *testing.T) {
	realm := kctest.NewRealm(t)
	keycloakToken := realm.Token(t, "kc-sub-1", nil)
	realm.SetDown(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	verifier, err := keycloakauth.Discover(ctx, realm.Issuer, nil, keycloakauth.DefaultBackoff)
	require.NoError(t, err)
	require.False(t, verifier.Ready())

	for name, tc := range map[string]struct {
		token        string
		wantCode     int
		reachesClerk bool
	}{
		"clerk token goes to Clerk": {"some.clerk.token", http.StatusOK, true},
		"keycloak token gets 503":   {keycloakToken, http.StatusServiceUnavailable, false},
	} {
		t.Run(name, func(t *testing.T) {
			reachedClerk := false
			clerkProtect := func(http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					reachedClerk = true
					w.WriteHeader(http.StatusOK)
				})
			}

			req := httptest.NewRequest(http.MethodGet, "/users", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			dualAccept(verifier, clerkProtect)(dummyOKHandler).ServeHTTP(w, req)

			assert.Equal(t, tc.reachesClerk, reachedClerk)
			assert.Equal(t, tc.wantCode, w.Code)
		})
	}
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
