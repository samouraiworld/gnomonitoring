package keycloakauth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth"
	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth/kctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastBackoff keeps the background retries of these tests short.
var fastBackoff = keycloakauth.Backoff{Initial: 10 * time.Millisecond, Max: 40 * time.Millisecond}

func discover(t *testing.T, issuer string) *keycloakauth.LazyVerifier {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	l, err := keycloakauth.Discover(ctx, issuer, []string{keycloakauth.PanelClientID}, fastBackoff)
	require.NoError(t, err)
	return l
}

func serve(l *keycloakauth.LazyVerifier, token string) *httptest.ResponseRecorder {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	l.Middleware(next).ServeHTTP(w, req)
	return w
}

func TestDiscover_ReachableIssuerIsReadyAtOnce(t *testing.T) {
	realm := kctest.NewRealm(t)

	l := discover(t, realm.Issuer)

	assert.True(t, l.Ready())
	assert.Equal(t, http.StatusOK, serve(l, realm.Token(t, "kc-sub-1", nil)).Code)
	assert.Equal(t, http.StatusUnauthorized, serve(l, "").Code)
	assert.Equal(t, http.StatusUnauthorized, serve(l, realm.TokenSignedByStranger(t, "kc-sub-1")).Code)
}

// An unreachable issuer must not fail Discover: the backend keeps running,
// answers 503 on authenticated routes, and enables them once a background
// retry reaches the issuer.
func TestDiscover_UnreachableIssuerDegradesThenRecovers(t *testing.T) {
	realm := kctest.NewRealm(t)
	token := realm.Token(t, "kc-sub-1", nil)
	realm.SetDown(true)

	l := discover(t, realm.Issuer)

	require.False(t, l.Ready())
	w := serve(l, token)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "an unchecked token is not an invalid one: 503, not 401")
	assert.NotEmpty(t, w.Header().Get("Retry-After"))
	_, err := l.VerifyToken(context.Background(), token)
	assert.True(t, errors.Is(err, keycloakauth.ErrNotReady))

	realm.SetDown(false)

	require.Eventually(t, l.Ready, 5*time.Second, 5*time.Millisecond, "background retry must reach the issuer")
	assert.Equal(t, http.StatusOK, serve(l, token).Code)
	claims, err := l.VerifyToken(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, "kc-sub-1", claims.Subject)
}

// An empty issuer is a configuration mistake no retry can fix.
func TestDiscover_EmptyIssuerIsAnError(t *testing.T) {
	_, err := keycloakauth.Discover(context.Background(), "  ", nil, fastBackoff)
	assert.Error(t, err)
}
