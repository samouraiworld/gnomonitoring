package keycloakauth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth"
	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth/kctest"
)

func newVerifier(t *testing.T, realm *kctest.Realm, allowedClients ...string) *keycloakauth.Verifier {
	t.Helper()
	if len(allowedClients) == 0 {
		allowedClients = []string{keycloakauth.PanelClientID}
	}
	v, err := keycloakauth.New(context.Background(), realm.Issuer, allowedClients)
	if err != nil {
		t.Fatalf("keycloakauth.New: %v", err)
	}
	return v
}

func TestNew_RejectsEmptyIssuer(t *testing.T) {
	if _, err := keycloakauth.New(context.Background(), "  ", nil); err == nil {
		t.Fatal("New(\"\") returned no error, want one: an empty keycloak_issuer must fail loudly at startup")
	}
}

func TestVerifyToken_AcceptsRealmToken(t *testing.T) {
	realm := kctest.NewRealm(t)
	v := newVerifier(t, realm)

	raw := realm.Token(t, "kc-sub-1", map[string]any{
		"clerk_user_id":   "user_2abc123",
		"resource_access": kctest.AdminResourceAccess(),
	})

	claims, err := v.VerifyToken(context.Background(), raw)
	if err != nil {
		t.Fatalf("VerifyToken() error = %v, want nil", err)
	}
	if claims.Subject != "kc-sub-1" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "kc-sub-1")
	}
	if got := claims.EffectiveUserID(); got != "user_2abc123" {
		t.Errorf("EffectiveUserID() = %q, want the clerk_user_id claim decoded from the token", got)
	}
	if !claims.IsAdmin() {
		t.Errorf("IsAdmin() = false, want true for a token carrying the gnomonitoring-panel admin client role")
	}
}

func TestVerifyToken_Rejects(t *testing.T) {
	realm := kctest.NewRealm(t)
	v := newVerifier(t, realm)

	cases := map[string]string{
		"signed by an unpublished key": realm.TokenSignedByStranger(t, "kc-sub-1"),
		"issued by another issuer":     realm.TokenWithIssuer(t, "https://evil.example/realms/gno-world", "kc-sub-1"),
		"expired":                      realm.ExpiredToken(t, "kc-sub-1"),
		"not a JWT at all":             "not-a-token",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.VerifyToken(context.Background(), raw); err == nil {
				t.Errorf("VerifyToken() accepted a token %s, want an error", name)
			}
		})
	}
}

func TestMiddleware(t *testing.T) {
	realm := kctest.NewRealm(t)
	v := newVerifier(t, realm)

	var seen *keycloakauth.Claims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = keycloakauth.ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := v.Middleware(next)

	t.Run("valid token passes and populates the context", func(t *testing.T) {
		seen = nil
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		req.Header.Set("Authorization", "Bearer "+realm.Token(t, "kc-sub-1", map[string]any{"clerk_user_id": "user_2abc"}))
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if seen == nil || seen.ClerkUserID != "user_2abc" {
			t.Errorf("claims on context = %+v, want the decoded token claims", seen)
		}
	})

	t.Run("missing and invalid tokens get 401", func(t *testing.T) {
		for name, header := range map[string]string{
			"no header":      "",
			"wrong scheme":   "Basic Zm9v",
			"garbage token":  "Bearer not-a-token",
			"stranger's key": "Bearer " + realm.TokenSignedByStranger(t, "kc-sub-1"),
		} {
			t.Run(name, func(t *testing.T) {
				seen = nil
				req := httptest.NewRequest(http.MethodGet, "/users", nil)
				if header != "" {
					req.Header.Set("Authorization", header)
				}
				w := httptest.NewRecorder()

				handler.ServeHTTP(w, req)

				if w.Code != http.StatusUnauthorized {
					t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
				}
				if seen != nil {
					t.Errorf("next handler ran with claims %+v, want it not to run at all", seen)
				}
			})
		}
	})
}

// The gno-world realm is shared with memba and gnolove. A correctly signed
// token issued to another client must not authenticate here unless that client
// was explicitly allowed.
func TestVerifyToken_EnforcesClientAllowlist(t *testing.T) {
	realm := kctest.NewRealm(t)
	v := newVerifier(t, realm, keycloakauth.PanelClientID)

	t.Run("rejects another realm client", func(t *testing.T) {
		raw := realm.Token(t, "kc-sub-1", map[string]any{"azp": "memba-web"})
		if _, err := v.VerifyToken(context.Background(), raw); err == nil {
			t.Error("VerifyToken() accepted a token issued to memba-web, want it rejected")
		}
	})

	t.Run("rejects a token whose only audience is account", func(t *testing.T) {
		raw := realm.Token(t, "kc-sub-1", map[string]any{"azp": ""})
		if _, err := v.VerifyToken(context.Background(), raw); err == nil {
			t.Error("VerifyToken() accepted a token with no azp and aud=account, want it rejected")
		}
	})

	t.Run("accepts an explicitly allowed second client", func(t *testing.T) {
		multi := newVerifier(t, realm, keycloakauth.PanelClientID, "memba-web")
		claims, err := multi.VerifyToken(context.Background(), realm.Token(t, "kc-sub-1", map[string]any{"azp": "memba-web"}))
		if err != nil {
			t.Fatalf("VerifyToken() = %v, want nil once memba-web is allowed", err)
		}
		if claims.Subject != "kc-sub-1" {
			t.Errorf("Subject = %q, want %q", claims.Subject, "kc-sub-1")
		}
	})

	t.Run("empty allowlist accepts any client in the realm", func(t *testing.T) {
		open, err := keycloakauth.New(context.Background(), realm.Issuer, nil)
		if err != nil {
			t.Fatalf("keycloakauth.New: %v", err)
		}
		if _, err := open.VerifyToken(context.Background(), realm.Token(t, "kc-sub-1", map[string]any{"azp": "anything"})); err != nil {
			t.Errorf("VerifyToken() = %v, want nil for an explicitly empty allowlist", err)
		}
	})

	t.Run("aud is honoured when a token carries no azp", func(t *testing.T) {
		audOnly := newVerifier(t, realm, "gnolove-web")
		raw := realm.Token(t, "kc-sub-1", map[string]any{"azp": "", "aud": []string{"account", "gnolove-web"}})
		if _, err := audOnly.VerifyToken(context.Background(), raw); err != nil {
			t.Errorf("VerifyToken() = %v, want nil when aud names an allowed client", err)
		}
	})
}
