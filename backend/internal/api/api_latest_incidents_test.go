package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// /latest_incidents gains two optional query parameters:
//   - addr:  only this validator's incidents (lowercase letters and digits
//     only, at most 64; the chain-wide pseudo-addresses `all` and `rpc` are
//     refused)
//   - limit: 1..100 rows (default unchanged: 10)
//
// Without them the endpoint behaves exactly as before. Either parameter
// present but empty is a client error, never a fallback to the default.

func withIncidentChain(t *testing.T) {
	t.Helper()
	internal.Config.Chains = map[string]*internal.ChainConfig{
		"test12": {
			RPCEndpoints:     []string{"http://localhost:26657"},
			GraphqlEndpoints: []string{"http://localhost:8080/graphql/query"},
			GnowebEndpoints:  []string{"http://localhost:8080"},
			Enabled:          true,
		},
	}
	internal.EnabledChains = []string{"test12"}
	internal.Config.DefaultChain = "test12"
	t.Cleanup(func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	})
}

func seedIncidents(t *testing.T, db *gorm.DB, addr string, n int, firstEnd int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		end := firstEnd + int64(i)
		require.NoError(t, database.InsertAlertlog(db, "test12", addr, "mon-"+addr, "WARNING", end-5, end, true, time.Now(), ""))
	}
}

func getIncidents(t *testing.T, db *gorm.DB, query string) (*httptest.ResponseRecorder, []database.AlertSummary) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/latest_incidents?"+query, nil)
	rec := httptest.NewRecorder()
	Getlastincident(rec, req, db)
	var out []database.AlertSummary
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec, out
}

func TestGetlastincident_FiltersByAddr(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withIncidentChain(t)
	seedIncidents(t, db, "g1target", 12, 100)
	seedIncidents(t, db, "g1noisy", 15, 1000)

	rec, alerts := getIncidents(t, db, "period=all_time&chain=test12&addr=g1target&limit=50")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, alerts, 12)
	for _, a := range alerts {
		assert.Equal(t, "g1target", a.Addr)
	}
}

func TestGetlastincident_WithoutNewParamsIsUnchanged(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withIncidentChain(t)
	seedIncidents(t, db, "g1target", 12, 100)
	seedIncidents(t, db, "g1noisy", 15, 1000)

	rec, alerts := getIncidents(t, db, "period=all_time&chain=test12")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, alerts, 10)
}

func TestGetlastincident_RejectsOutOfRangeLimit(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withIncidentChain(t)

	for _, limit := range []string{"0", "-1", "101", "ten", "1.5"} {
		rec, _ := getIncidents(t, db, "period=all_time&chain=test12&limit="+limit)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "limit=%s must be rejected, not clamped silently", limit)
	}
	rec, _ := getIncidents(t, db, "period=all_time&chain=test12&limit=100")
	assert.Equal(t, http.StatusOK, rec.Code, "the maximum itself is allowed")
}

func TestGetlastincident_RejectsMalformedAddr(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withIncidentChain(t)

	tooLong := "g1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 70 chars, over the 64 cap
	for _, addr := range []string{"G1UPPER", "g1%27%20OR%201%3D1", "g1has-dash", tooLong} {
		rec, _ := getIncidents(t, db, "period=all_time&chain=test12&addr="+addr)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "addr=%s must be rejected", addr)
	}
}

func TestGetlastincident_RejectsReservedAddr(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withIncidentChain(t)
	seedIncidents(t, db, "all", 3, 0)
	seedIncidents(t, db, "rpc", 3, 0)

	// `all` and `rpc` rows carry chain stagnation and RPC outage messages, which
	// embed raw client errors and endpoint names. They must not be reachable
	// through a per-validator query on this unauthenticated endpoint.
	for _, addr := range []string{"all", "rpc"} {
		rec, _ := getIncidents(t, db, "period=all_time&chain=test12&addr="+addr)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "addr=%s is reserved and must be rejected", addr)
	}
}

func TestGetlastincident_RejectsEmptyParams(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withIncidentChain(t)
	seedIncidents(t, db, "g1target", 12, 100)
	seedIncidents(t, db, "g1noisy", 15, 1000)

	// `addr=` must not fall back to the chain-wide list: a client building the
	// URL before the address is loaded would render other validators' incidents
	// as this validator's history.
	rec, _ := getIncidents(t, db, "period=all_time&chain=test12&addr=")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "an empty addr must be rejected, not treated as absent")

	rec, _ = getIncidents(t, db, "period=all_time&chain=test12&limit=")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "an empty limit must be rejected, not treated as absent")
}
