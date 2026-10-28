package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	clerk "github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/keycloakauth"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAccountErasureHTTPContractAndMigratedIdentity(t *testing.T) {
	db := testoutils.NewTestDB(t)
	oldDev := internal.Config.DevMode
	internal.Config.DevMode = false
	t.Cleanup(func() { internal.Config.DevMode = oldDev })
	const userID = "user_clerk_original"
	req := func(method, path, body, provider string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if provider == "clerk" {
			r = r.WithContext(clerk.ContextWithSessionClaims(r.Context(), &clerk.SessionClaims{RegisteredClaims: clerk.RegisteredClaims{Subject: userID}}))
		}
		if provider == "keycloak" {
			r = r.WithContext(keycloakauth.NewClaimsContext(r.Context(), &keycloakauth.Claims{Subject: "different-keycloak-subject", ClerkUserID: userID}))
		}
		return r
	}
	erase := func(provider string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		EraseAccountHandler(w, req(http.MethodPost, "/users/erase", "", provider), db)
		return w
	}
	require.NoError(t, database.InsertUser(userID, "a@example.test", "A", db))
	require.Equal(t, http.StatusNoContent, erase("clerk").Code)
	require.Equal(t, http.StatusNoContent, erase("keycloak").Code)
	var n int64
	require.NoError(t, db.Model(&database.AccountErasure{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
	handlers := []struct {
		name, method, path, body string
		call                     func(http.ResponseWriter, *http.Request, *gorm.DB)
	}{
		{"provision", http.MethodPost, "/users", `{"name":"A","email":"a@example.test"}`, CreateUserhandler},
		{"user update", http.MethodPut, "/users", `{"name":"A","email":"a@example.test"}`, UpdateUserHandler},
		{"validator webhook", http.MethodPost, "/webhooks/validator", `{}`, CreateMonitoringWebhookHandler},
		{"govdao webhook", http.MethodPost, "/webhooks/govdao", `{}`, CreateWebhookHandler},
		{"contact", http.MethodPost, "/alert-contacts", `{}`, InsertAlertContactHandler},
		{"contact update", http.MethodPut, "/alert-contacts", `{}`, UpdateAlertContactHandler},
		{"schedule", http.MethodPut, "/usersH", `{}`, UpdateReportHourHandler},
	}
	for _, h := range handlers {
		t.Run(h.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.call(w, req(h.method, h.path, h.body, "keycloak"), db)
			require.Equal(t, http.StatusGone, w.Code, w.Body.String())
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			var body map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, "account_erased", body["code"])
		})
	}
	w := httptest.NewRecorder()
	DeleteUserHandler(w, req(http.MethodDelete, "/users", "", "clerk"), db)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, http.StatusNoContent, erase("clerk").Code)
	require.Equal(t, http.StatusUnauthorized, erase("").Code)
	adminResponse := httptest.NewRecorder()
	handlePutSchedule(adminResponse, httptest.NewRequest(http.MethodPut, "/admin/schedules", strings.NewReader(`{"hour":12,"minute":0,"timezone":"UTC"}`)), db, userID)
	require.Equal(t, http.StatusGone, adminResponse.Code)

	w = httptest.NewRecorder()
	EraseAccountHandler(w, req(http.MethodGet, "/users/erase", "", "clerk"), db)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestAccountErasureRouteRequiresAuthentication(t *testing.T) {
	mux := http.NewServeMux()
	registerAccountErasureRoute(mux, nil, clerkhttp.RequireHeaderAuthorization())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/erase", nil))
	require.Equal(t, http.StatusForbidden, w.Code) // middleware stops before DB access
}
