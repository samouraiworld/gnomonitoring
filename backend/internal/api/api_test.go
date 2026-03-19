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

// ---------- TEST GetChainIDFromRequest validation ----------

func TestGetChainIDFromRequest_InvalidChain(t *testing.T) {
	// Setup chains config
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

	// Request with non-existent chain should return 400
	req := httptest.NewRequest(http.MethodGet, "/alerts?period=all_time&chain=nonexistent", nil)
	w := httptest.NewRecorder()

	api.Getlastincident(w, req, testoutils.NewTestDB(t))
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusBadRequest, res.StatusCode)
}

// ---------- TEST /alerts cross-chain isolation ----------

func TestGetAlerts_CrossChainIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Setup config with multiple chains
	internal.Config.Chains = map[string]*internal.ChainConfig{
		"betanet": {
			RPCEndpoint:     "http://localhost:26657",
			GraphqlEndpoint: "http://localhost:8080/graphql/query",
			GnowebEndpoint:  "http://localhost:8080",
			Enabled:         true,
		},
		"gnoland1": {
			RPCEndpoint:     "http://localhost:26658",
			GraphqlEndpoint: "http://localhost:8080/graphql/query",
			GnowebEndpoint:  "http://localhost:8080",
			Enabled:         true,
		},
	}
	internal.EnabledChains = []string{"betanet", "gnoland1"}
	internal.Config.DefaultChain = "betanet"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

	// Insert alerts for different chains
	betanetAlert := database.AlertLog{
		ChainID:     "betanet",
		Addr:        "g1abc",
		Moniker:     "BetanetValidator",
		Level:       "CRITICAL",
		StartHeight: 100,
		EndHeight:   150,
		SentAt:      time.Now(),
	}
	gnolandAlert := database.AlertLog{
		ChainID:     "gnoland1",
		Addr:        "g1xyz",
		Moniker:     "GnolandValidator",
		Level:       "WARNING",
		StartHeight: 200,
		EndHeight:   250,
		SentAt:      time.Now(),
	}

	db.Create(&betanetAlert)
	db.Create(&gnolandAlert)

	// Query alerts for betanet
	req := httptest.NewRequest(http.MethodGet, "/alerts?period=all_time&chain=betanet", nil)
	w := httptest.NewRecorder()

	api.Getlastincident(w, req, db)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	// Should contain betanet validator
	assert.Contains(t, bodyStr, "BetanetValidator")
	// Should NOT contain gnoland1 validator
	assert.NotContains(t, bodyStr, "GnolandValidator")
}

// ---------- TEST /uptime chain isolation ----------

// TestGetUptime_ChainIsolation seeds participation rows for two chains and
// verifies that /uptime?chain=X returns only the validators belonging to
// chain X, with no cross-contamination from chain Y.
func TestGetUptime_ChainIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	internal.Config.Chains = map[string]*internal.ChainConfig{
		"chainAlpha": {
			RPCEndpoint:     "http://localhost:26657",
			GraphqlEndpoint: "http://localhost:8080/graphql/query",
			GnowebEndpoint:  "http://localhost:8080",
			Enabled:         true,
		},
		"chainBeta": {
			RPCEndpoint:     "http://localhost:26658",
			GraphqlEndpoint: "http://localhost:8080/graphql/query",
			GnowebEndpoint:  "http://localhost:8080",
			Enabled:         true,
		},
	}
	internal.EnabledChains = []string{"chainAlpha", "chainBeta"}
	internal.Config.DefaultChain = "chainAlpha"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

	now := time.Now()

	// Seed 5 participation rows for chainAlpha (unique address).
	for i := range 5 {
		db.Create(&database.DailyParticipation{
			ChainID:      "chainAlpha",
			Addr:         "g1alpha",
			Moniker:      "AlphaValidator",
			BlockHeight:  int64(1000 + i),
			Date:         now,
			Participated: true,
		})
	}

	// Seed 5 participation rows for chainBeta (unique address).
	for i := range 5 {
		db.Create(&database.DailyParticipation{
			ChainID:      "chainBeta",
			Addr:         "g1beta",
			Moniker:      "BetaValidator",
			BlockHeight:  int64(2000 + i),
			Date:         now,
			Participated: false,
		})
	}

	// Query /uptime for chainAlpha.
	reqA := httptest.NewRequest(http.MethodGet, "/uptime?chain=chainAlpha", nil)
	wA := httptest.NewRecorder()
	api.GetUptime(wA, reqA, db)
	resA := wA.Result()
	defer resA.Body.Close()

	assert.Equal(t, http.StatusOK, resA.StatusCode)
	bodyA, _ := io.ReadAll(resA.Body)
	bodyAStr := string(bodyA)

	assert.Contains(t, bodyAStr, "AlphaValidator", "chainAlpha response must contain AlphaValidator")
	assert.NotContains(t, bodyAStr, "BetaValidator", "chainAlpha response must not contain BetaValidator")

	// Query /uptime for chainBeta.
	reqB := httptest.NewRequest(http.MethodGet, "/uptime?chain=chainBeta", nil)
	wB := httptest.NewRecorder()
	api.GetUptime(wB, reqB, db)
	resB := wB.Result()
	defer resB.Body.Close()

	assert.Equal(t, http.StatusOK, resB.StatusCode)
	bodyB, _ := io.ReadAll(resB.Body)
	bodyBStr := string(bodyB)

	assert.Contains(t, bodyBStr, "BetaValidator", "chainBeta response must contain BetaValidator")
	assert.NotContains(t, bodyBStr, "AlphaValidator", "chainBeta response must not contain AlphaValidator")
}

// ---------- TEST /info/blockheight chain isolation ----------

// TestGetBlockHeight_ChainIsolation seeds blocks at different heights for two
// chains and verifies that /info/blockheight?chain=X returns the correct
// maximum height for that chain only.
func TestGetBlockHeight_ChainIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	internal.Config.Chains = map[string]*internal.ChainConfig{
		"net1": {
			RPCEndpoint:     "http://localhost:26657",
			GraphqlEndpoint: "http://localhost:8080/graphql/query",
			GnowebEndpoint:  "http://localhost:8080",
			Enabled:         true,
		},
		"net2": {
			RPCEndpoint:     "http://localhost:26658",
			GraphqlEndpoint: "http://localhost:8080/graphql/query",
			GnowebEndpoint:  "http://localhost:8080",
			Enabled:         true,
		},
	}
	internal.EnabledChains = []string{"net1", "net2"}
	internal.Config.DefaultChain = "net1"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

	now := time.Now()

	// net1: max block height 9999.
	db.Create(&database.DailyParticipation{
		ChainID: "net1", Addr: "g1net1", Moniker: "Net1Val",
		BlockHeight: 9999, Date: now, Participated: true,
	})
	db.Create(&database.DailyParticipation{
		ChainID: "net1", Addr: "g1net1", Moniker: "Net1Val",
		BlockHeight: 5000, Date: now, Participated: true,
	})

	// net2: max block height 1234 (well below net1).
	db.Create(&database.DailyParticipation{
		ChainID: "net2", Addr: "g1net2", Moniker: "Net2Val",
		BlockHeight: 1234, Date: now, Participated: true,
	})

	// Verify /info/blockheight for net1 returns last_stored = MAX-1 = 9998.
	req1 := httptest.NewRequest(http.MethodGet, "/info/blockheight?chain=net1", nil)
	w1 := httptest.NewRecorder()
	api.Getblockheight(w1, req1, db)
	res1 := w1.Result()
	defer res1.Body.Close()

	assert.Equal(t, http.StatusOK, res1.StatusCode)
	var resp1 map[string]int64
	err := json.NewDecoder(res1.Body).Decode(&resp1)
	require.NoError(t, err)
	assert.Equal(t, int64(9998), resp1["last_stored"],
		"net1 last_stored should be MAX(block_height)-1 = 9998")

	// Verify /info/blockheight for net2 returns last_stored = 1233, not 9998.
	req2 := httptest.NewRequest(http.MethodGet, "/info/blockheight?chain=net2", nil)
	w2 := httptest.NewRecorder()
	api.Getblockheight(w2, req2, db)
	res2 := w2.Result()
	defer res2.Body.Close()

	assert.Equal(t, http.StatusOK, res2.StatusCode)
	var resp2 map[string]int64
	err = json.NewDecoder(res2.Body).Decode(&resp2)
	require.NoError(t, err)
	assert.Equal(t, int64(1233), resp2["last_stored"],
		"net2 last_stored should be MAX(block_height)-1 = 1233, not net1's value")
}
