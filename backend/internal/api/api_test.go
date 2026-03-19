package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/api"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- TEST /webhooks ----------

func TestGetWebhooks(t *testing.T) {
	db := testoutils.NewTestDB(t)
	userID := "test"
	err := db.Create(&database.WebhookGovDAO{UserID: userID, Description: "Test", URL: "http://localhost", Type: "discord"}).Error
	require.NoError(t, err)

	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	req := httptest.NewRequest(http.MethodGet, "/webhooks", nil)
	req.Header.Set("X-Debug-UserID", userID)
	w := httptest.NewRecorder()

	api.ListWebhooksHandler(w, req, db)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Contains(t, string(body), "http://localhost")
}

// ---------- TEST /alerts ----------

func TestGetAlerts(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Setup config with test chain
	internal.Config.Chains = map[string]*internal.ChainConfig{
		"test12": {
			RPCEndpoint:     "http://localhost:26657",
			GraphqlEndpoint: "http://localhost:8080/graphql/query",
			GnowebEndpoint:  "http://localhost:8080",
			Enabled:         true,
		},
	}
	internal.EnabledChains = []string{"test12"}
	internal.Config.DefaultChain = "test12"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

	db.Create(&database.AlertLog{
		ChainID:     "test12",
		Moniker:     "Validator1",
		Level:       "CRITICAL",
		StartHeight: 100,
		EndHeight:   200,
		SentAt:      time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/alerts?period=current_month&chain=test12", nil)
	w := httptest.NewRecorder()

	api.Getlastincident(w, req, db)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Contains(t, string(body), "Validator1")
}

// ---------- TEST /reports ----------

func TestGetHourReports(t *testing.T) {
	db := testoutils.NewTestDB(t)
	db.Create(&database.HourReport{
		DailyReportHour:   13,
		DailyReportMinute: 0,
		UserID:            "moniker1",
		Timezone:          "UTC",
	})

	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	req := httptest.NewRequest(http.MethodGet, "/reports", nil)
	req.Header.Set("X-Debug-UserID", "moniker1")
	w := httptest.NewRecorder()

	api.GetReportHourHandler(w, req, db)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Contains(t, string(body), "moniker1")
}

// ---------- TEST /info/blockheight ----------

func TestGetBlockHeight(t *testing.T) {
	// Setup config with test chain
	internal.Config.Chains = map[string]*internal.ChainConfig{
		"test12": {
			RPCEndpoint:     "http://localhost:26657",
			GraphqlEndpoint: "http://localhost:8080/graphql/query",
			GnowebEndpoint:  "http://localhost:8080",
			Enabled:         true,
		},
	}
	internal.EnabledChains = []string{"test12"}
	internal.Config.DefaultChain = "test12"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

	req := httptest.NewRequest(http.MethodGet, "/info/blockheight", nil)
	w := httptest.NewRecorder()

	api.Getblockheight(w, req, testoutils.NewTestDB(t))
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)

	var response map[string]int64
	err := json.NewDecoder(res.Body).Decode(&response)
	assert.NoError(t, err)
	_, exists := response["last_stored"]

	assert.True(t, exists)
}
