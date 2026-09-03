package keycloakauth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth"
	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth/kctest"
)

func newVerifier(t *testing.T, realm *kctest.Realm) *keycloakauth.Verifier {
	t.Helper()
	v, err := keycloakauth.New(context.Background(), realm.Issuer)
	if err != nil {
		t.Fatalf("keycloakauth.New: %v", err)
	}
	return v
}

func TestNew_RejectsEmptyIssuer(t *testing.T) {
	if _, err := keycloakauth.New(context.Background(), "  "); err == nil {
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
