package database_test

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetAlertLog_CrossChainIsolation verifies that alert logs are properly isolated by chain_id
func TestGetAlertLog_CrossChainIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

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

	err := db.Create(&betanetAlert).Error
	require.NoError(t, err)
	err = db.Create(&gnolandAlert).Error
	require.NoError(t, err)

	// Query alerts for betanet only
	alerts, err := database.GetAlertLog(db, "betanet", "all_time")
	require.NoError(t, err)

	// Verify we only get betanet alerts
	assert.NotEmpty(t, alerts, "Should find at least one betanet alert")
	for _, alert := range alerts {
		assert.Equal(t, "BetanetValidator", alert.Moniker, "Should only contain betanet alerts")
	}

	// Query alerts for gnoland1 only
	alerts, err = database.GetAlertLog(db, "gnoland1", "all_time")
	require.NoError(t, err)

	// Verify we only get gnoland1 alerts
	assert.NotEmpty(t, alerts, "Should find at least one gnoland1 alert")
	for _, alert := range alerts {
		assert.Equal(t, "GnolandValidator", alert.Moniker, "Should only contain gnoland1 alerts")
	}
}

// TestGetCurrentPeriodParticipationRate_CrossChain verifies participation rates are isolated by chain
func TestGetCurrentPeriodParticipationRate_CrossChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert participation data for multiple chains
	betanetData := []database.DailyParticipation{
		{
			ChainID:       "betanet",
			Addr:          "g1val1",
			Moniker:       "BetanetVal1",
			BlockHeight:   100,
			Date:          time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
		{
			ChainID:       "betanet",
			Addr:          "g1val1",
			Moniker:       "BetanetVal1",
			BlockHeight:   101,
			Date:          time.Date(2025, 10, 2, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
	}
	gnolandData := []database.DailyParticipation{
		{
			ChainID:       "gnoland1",
			Addr:          "g1val2",
			Moniker:       "GnolandVal1",
			BlockHeight:   200,
			Date:          time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
			Participated:  false,
			TxContribution: false,
		},
		{
			ChainID:       "gnoland1",
			Addr:          "g1val2",
			Moniker:       "GnolandVal1",
			BlockHeight:   201,
			Date:          time.Date(2025, 10, 2, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
	}

	err := db.Create(&betanetData).Error
	require.NoError(t, err)
	err = db.Create(&gnolandData).Error
	require.NoError(t, err)

	// Query betanet participation rate - should include seeded data and new data
	betanetRates, err := database.GetCurrentPeriodParticipationRate(db, "betanet", "all_time")
	require.NoError(t, err)
	assert.NotEmpty(t, betanetRates)

	// Verify no gnoland1 validators are in betanet results
	for _, rate := range betanetRates {
		assert.NotEqual(t, "g1val2", rate.Addr, "gnoland1 validator should not appear in betanet results")
	}

	// Query gnoland1 participation rate
	gnolandRates, err := database.GetCurrentPeriodParticipationRate(db, "gnoland1", "all_time")
	require.NoError(t, err)
	assert.NotEmpty(t, gnolandRates)

	// Verify we only see gnoland1 validators, not betanet
	for _, rate := range gnolandRates {
		assert.NotEqual(t, "g1abc", rate.Addr, "betanet seeded validator should not appear in gnoland1 results")
		assert.NotEqual(t, "g1val1", rate.Addr, "betanet validator should not appear in gnoland1 results")
	}
}

// TestUptimeMetricsaddr_CrossChain verifies uptime metrics are isolated by chain
func TestUptimeMetricsaddr_CrossChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert participation data for multiple chains
	// betanet: 3 blocks, 2 participated -> ~66.67% uptime
	betanetData := []database.DailyParticipation{
		{
			ChainID:       "betanet",
			Addr:          "g1val1",
			Moniker:       "BetanetVal1",
			BlockHeight:   100,
			Date:          time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
		{
			ChainID:       "betanet",
			Addr:          "g1val1",
			Moniker:       "BetanetVal1",
			BlockHeight:   101,
			Date:          time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
		{
			ChainID:       "betanet",
			Addr:          "g1val1",
			Moniker:       "BetanetVal1",
			BlockHeight:   102,
			Date:          time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC),
			Participated:  false,
			TxContribution: false,
		},
	}

	// gnoland1: different validator with different uptime
	gnolandData := []database.DailyParticipation{
		{
			ChainID:       "gnoland1",
			Addr:          "g1val2",
			Moniker:       "GnolandVal2",
			BlockHeight:   200,
			Date:          time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
		{
			ChainID:       "gnoland1",
			Addr:          "g1val2",
			Moniker:       "GnolandVal2",
			BlockHeight:   201,
			Date:          time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC),
			Participated:  false,
			TxContribution: false,
		},
	}

	err := db.Create(&betanetData).Error
	require.NoError(t, err)
	err = db.Create(&gnolandData).Error
	require.NoError(t, err)

	// Query betanet uptime
	betanetUptime, err := database.UptimeMetricsaddr(db, "betanet")
	require.NoError(t, err)
	assert.NotEmpty(t, betanetUptime)

	// Verify no gnoland1 validators in betanet uptime
	for _, metric := range betanetUptime {
		assert.NotEqual(t, "g1val2", metric.Addr, "gnoland1 validator should not appear in betanet uptime")
		assert.Greater(t, metric.Uptime, 0.0)
	}

	// Query gnoland1 uptime
	gnolandUptime, err := database.UptimeMetricsaddr(db, "gnoland1")
	require.NoError(t, err)
	assert.NotEmpty(t, gnolandUptime)

	// Verify we only see gnoland1 validators, not betanet
	for _, metric := range gnolandUptime {
		assert.NotEqual(t, "g1abc", metric.Addr, "betanet seeded validator should not appear in gnoland1 uptime")
		assert.NotEqual(t, "g1val1", metric.Addr, "betanet validator should not appear in gnoland1 uptime")
	}
}

// TestMissingBlock_CrossChain verifies missing blocks are isolated by chain
func TestMissingBlock_CrossChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert participation data for multiple chains
	betanetData := []database.DailyParticipation{
		{
			ChainID:       "betanet",
			Addr:          "g1val1",
			Moniker:       "BetanetVal1",
			BlockHeight:   100,
			Date:          time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
			Participated:  false,
			TxContribution: false,
		},
		{
			ChainID:       "betanet",
			Addr:          "g1val1",
			Moniker:       "BetanetVal1",
			BlockHeight:   101,
			Date:          time.Date(2025, 10, 2, 0, 0, 0, 0, time.UTC),
			Participated:  false,
			TxContribution: false,
		},
	}
	gnolandData := []database.DailyParticipation{
		{
			ChainID:       "gnoland1",
			Addr:          "g1val2",
			Moniker:       "GnolandVal1",
			BlockHeight:   200,
			Date:          time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
	}

	err := db.Create(&betanetData).Error
	require.NoError(t, err)
	err = db.Create(&gnolandData).Error
	require.NoError(t, err)

	// Query betanet missing blocks
	betanetMissing, err := database.MissingBlock(db, "betanet", "all_time")
	require.NoError(t, err)
	assert.NotEmpty(t, betanetMissing)

	// Verify no gnoland1 validators in betanet missing blocks
	for _, metric := range betanetMissing {
		assert.NotEqual(t, "g1val2", metric.Addr, "gnoland1 validator should not appear in betanet missing blocks")
	}

	// Query gnoland1 missing blocks
	gnolandMissing, err := database.MissingBlock(db, "gnoland1", "all_time")
	require.NoError(t, err)

	// Verify we only see gnoland1 validators, not betanet
	for _, metric := range gnolandMissing {
		assert.NotEqual(t, "g1abc", metric.Addr, "betanet seeded validator should not appear in gnoland1 missing blocks")
		assert.NotEqual(t, "g1val1", metric.Addr, "betanet validator should not appear in gnoland1 missing blocks")
	}
}

// TestGetFirstSeen_CrossChain verifies first seen metrics are isolated by chain
func TestGetFirstSeen_CrossChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert participation data for multiple chains
	betanetData := []database.DailyParticipation{
		{
			ChainID:       "betanet",
			Addr:          "g1val1",
			Moniker:       "BetanetVal1",
			BlockHeight:   100,
			Date:          time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
	}
	gnolandData := []database.DailyParticipation{
		{
			ChainID:       "gnoland1",
			Addr:          "g1val2",
			Moniker:       "GnolandVal1",
			BlockHeight:   200,
			Date:          time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
	}

	err := db.Create(&betanetData).Error
	require.NoError(t, err)
	err = db.Create(&gnolandData).Error
	require.NoError(t, err)

	// Query betanet first seen - should include seeded data and new data
	betanetFirstSeen, err := database.GetFirstSeen(db, "betanet")
	require.NoError(t, err)
	assert.NotEmpty(t, betanetFirstSeen)

	// Verify no gnoland1 validators are in betanet results
	for _, metric := range betanetFirstSeen {
		assert.NotEqual(t, "g1val2", metric.Addr, "gnoland1 validator should not appear in betanet results")
	}

	// Query gnoland1 first seen
	gnolandFirstSeen, err := database.GetFirstSeen(db, "gnoland1")
	require.NoError(t, err)
	assert.NotEmpty(t, gnolandFirstSeen)

	// Verify we only see gnoland1 validators, not betanet
	for _, metric := range gnolandFirstSeen {
		assert.NotEqual(t, "g1abc", metric.Addr, "betanet seeded validator should not appear in gnoland1 results")
		assert.NotEqual(t, "g1val1", metric.Addr, "betanet validator should not appear in gnoland1 results")
	}
}

// TestGetMoniker_CrossChain verifies moniker lookups are isolated by chain
func TestGetMoniker_CrossChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert monikers for different chains
	betanetMoniker := database.AddrMoniker{
		ChainID: "betanet",
		Addr:    "g1val1",
		Moniker: "BetanetValidator",
	}
	gnolandMoniker := database.AddrMoniker{
		ChainID: "gnoland1",
		Addr:    "g1val2",
		Moniker: "GnolandValidator",
	}

	err := db.Create(&betanetMoniker).Error
	require.NoError(t, err)
	err = db.Create(&gnolandMoniker).Error
	require.NoError(t, err)

	// Query betanet monikers
	betanetMap, err := database.GetMoniker(db, "betanet")
	require.NoError(t, err)
	assert.NotEmpty(t, betanetMap)
	// Should only contain betanet validators
	assert.Contains(t, betanetMap, "g1val1")
	assert.Equal(t, "BetanetValidator", betanetMap["g1val1"])
	// Should not contain gnoland validator
	assert.NotContains(t, betanetMap, "g1val2")

	// Query gnoland1 monikers
	gnolandMap, err := database.GetMoniker(db, "gnoland1")
	require.NoError(t, err)
	assert.NotEmpty(t, gnolandMap)
	// Should only contain gnoland1 validators
	assert.Contains(t, gnolandMap, "g1val2")
	assert.Equal(t, "GnolandValidator", gnolandMap["g1val2"])
	// Should not contain betanet validator
	assert.NotContains(t, gnolandMap, "g1val1")
}
