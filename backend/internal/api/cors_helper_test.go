package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/stretchr/testify/assert"
)

var dummyOKHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// TestCorsThenAuth_OptionsPreflight_NoAuthHeader reproduces the production bug:
// a CORS preflight OPTIONS request never carries an Authorization header, so
// clerkhttp.RequireHeaderAuthorization alone would return 403 with no CORS
// headers, before the wrapped handler's own OPTIONS branch ever runs.
func TestCorsThenAuth_OptionsPreflight_NoAuthHeader(t *testing.T) {
	internal.Config.DevMode = false
	internal.Config.AllowedOrigins = []string{"https://app.example.com"}
	defer func() {
		internal.Config.DevMode = false
		internal.Config.AllowedOrigins = nil
	}()

	protected := clerkhttp.RequireHeaderAuthorization()
	handler := corsThenAuth(dummyOKHandler, protected)

	req := httptest.NewRequest(http.MethodOptions, "/users", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "https://app.example.com", res.Header.Get("Access-Control-Allow-Origin"))
}

// TestCorsThenAuth_NonOptionsWithoutAuth_StillRejected verifies the fix doesn't
// bypass auth: a real request without an Authorization header is still
// rejected by clerkhttp, but the response still carries CORS headers.
func TestCorsThenAuth_NonOptionsWithoutAuth_StillRejected(t *testing.T) {
	internal.Config.DevMode = false
	internal.Config.AllowedOrigins = []string{"https://app.example.com"}
	defer func() {
		internal.Config.DevMode = false
		internal.Config.AllowedOrigins = nil
	}()

	protected := clerkhttp.RequireHeaderAuthorization()
	handler := corsThenAuth(dummyOKHandler, protected)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusForbidden, res.StatusCode)
	assert.Equal(t, "https://app.example.com", res.Header.Get("Access-Control-Allow-Origin"))
}
