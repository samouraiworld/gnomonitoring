package keycloakauth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth"
)

func TestClaims_IsAdmin(t *testing.T) {
	c := &keycloakauth.Claims{
		ResourceAccess: map[string]keycloakauth.ClientRoles{
			"gnomonitoring-panel": {Roles: []string{"offline_access", "admin"}},
		},
	}
	if !c.IsAdmin() {
		t.Errorf("expected IsAdmin() true when resource_access.gnomonitoring-panel.roles contains \"admin\"")
	}

	c2 := &keycloakauth.Claims{
		ResourceAccess: map[string]keycloakauth.ClientRoles{
			"gnomonitoring-panel": {Roles: []string{"offline_access"}},
		},
	}
	if c2.IsAdmin() {
		t.Errorf("expected IsAdmin() false when resource_access.gnomonitoring-panel.roles does not contain \"admin\"")
	}

	c3 := &keycloakauth.Claims{ResourceAccess: map[string]keycloakauth.ClientRoles{}}
	if c3.IsAdmin() {
		t.Errorf("expected IsAdmin() false when resource_access has no entry for gnomonitoring-panel at all")
	}
}

// A realm-wide "admin" role must NOT grant gnomonitoring admin: gno-world is
// shared with memba and gnolove, and the whole point of the client-scoped role
// is that being admin on one app does not make you admin on the others.
func TestClaims_IsAdmin_IgnoresOtherClientsAndRealmRoles(t *testing.T) {
	c := &keycloakauth.Claims{
		RealmAccess: keycloakauth.ClientRoles{Roles: []string{"admin"}},
		ResourceAccess: map[string]keycloakauth.ClientRoles{
			"memba-web": {Roles: []string{"admin"}},
		},
	}
	if c.IsAdmin() {
		t.Errorf("expected IsAdmin() false: neither a realm-wide admin role nor another client's admin role grants gnomonitoring admin")
	}
}

func TestClaims_EffectiveUserID_PrefersClerkUserID(t *testing.T) {
	c := &keycloakauth.Claims{Subject: "f47ac10b-...", ClerkUserID: "user_2abc123"}
	if got := c.EffectiveUserID(); got != "user_2abc123" {
		t.Errorf("EffectiveUserID() = %q, want the clerk_user_id claim %q", got, "user_2abc123")
	}

	c2 := &keycloakauth.Claims{Subject: "f47ac10b-...", ClerkUserID: ""}
	if got := c2.EffectiveUserID(); got != "f47ac10b-..." {
		t.Errorf("EffectiveUserID() = %q, want fallback to sub %q (post-cutover new users have no clerk_user_id)", got, "f47ac10b-...")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
		wantOK bool
	}{
		{"valid", "Bearer abc.def.ghi", "abc.def.ghi", true},
		{"missing header", "", "", false},
		{"wrong scheme", "Basic abc", "", false},
		{"case-insensitive scheme", "bearer abc.def.ghi", "abc.def.ghi", true},
		{"empty token", "Bearer ", "", false},
		{"surrounding whitespace", "Bearer   abc.def.ghi  ", "abc.def.ghi", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			got, ok := keycloakauth.BearerToken(r)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("BearerToken() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestClaimsFromContext_RoundTrip(t *testing.T) {
	want := &keycloakauth.Claims{Subject: "sub-1", ClerkUserID: "user_2abc"}
	ctx := keycloakauth.NewClaimsContext(context.Background(), want)

	got, ok := keycloakauth.ClaimsFromContext(ctx)
	if !ok || got != want {
		t.Errorf("ClaimsFromContext() = (%v, %v), want the claims stored by NewClaimsContext", got, ok)
	}

	if _, ok := keycloakauth.ClaimsFromContext(context.Background()); ok {
		t.Errorf("ClaimsFromContext() on a bare context reported ok=true, want false")
	}
}
