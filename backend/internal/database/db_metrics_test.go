package database_test

import (
	"testing"
	"time"

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

	// Use recent dates so they fall within the 30-day window used by UptimeMetricsaddr.
	recentDate := time.Now().UTC().AddDate(0, 0, -1)

	// Insert participation data for multiple chains
	// betanet: 3 blocks, 2 participated -> ~66.67% uptime
	betanetData := []database.DailyParticipation{
		{
			ChainID:        "betanet",
			Addr:           "g1val1",
			Moniker:        "BetanetVal1",
			BlockHeight:    100,
			Date:           recentDate,
			Participated:   true,
			TxContribution: false,
		},
		{
			ChainID:        "betanet",
			Addr:           "g1val1",
			Moniker:        "BetanetVal1",
			BlockHeight:    101,
			Date:           recentDate,
			Participated:   true,
			TxContribution: false,
		},
		{
			ChainID:        "betanet",
			Addr:           "g1val1",
			Moniker:        "BetanetVal1",
			BlockHeight:    102,
			Date:           recentDate,
			Participated:   false,
			TxContribution: false,
		},
	}

	// gnoland1: different validator with different uptime
	gnolandData := []database.DailyParticipation{
		{
			ChainID:        "gnoland1",
			Addr:           "g1val2",
			Moniker:        "GnolandVal2",
			BlockHeight:    200,
			Date:           recentDate,
			Participated:   true,
			TxContribution: false,
		},
		{
			ChainID:        "gnoland1",
			Addr:           "g1val2",
			Moniker:        "GnolandVal2",
			BlockHeight:    201,
			Date:           recentDate,
			Participated:   false,
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

// TestGetMissedBlocksWindow verifies that missed blocks are counted correctly per time window.
func TestGetMissedBlocksWindow(t *testing.T) {
	db := testoutils.NewTestDB(t)

	chainID := "betanet"
	now := time.Now().UTC()

	// Insert blocks: 2 missed in last 1h, 1 more missed between 1h and 24h, 1 more missed between 24h and 7d.
	data := []database.DailyParticipation{
		// Within 1h window — missed
		{ChainID: chainID, Addr: "g1win", Moniker: "WinVal", BlockHeight: 1000, Date: now.Add(-10 * time.Minute), Participated: false, TxContribution: false},
		{ChainID: chainID, Addr: "g1win", Moniker: "WinVal", BlockHeight: 1001, Date: now.Add(-30 * time.Minute), Participated: false, TxContribution: false},
		// Within 24h but outside 1h — missed
		{ChainID: chainID, Addr: "g1win", Moniker: "WinVal", BlockHeight: 1002, Date: now.Add(-2 * time.Hour), Participated: false, TxContribution: false},
		// Within 7d but outside 24h — missed
		{ChainID: chainID, Addr: "g1win", Moniker: "WinVal", BlockHeight: 1003, Date: now.Add(-48 * time.Hour), Participated: false, TxContribution: false},
		// Within 1h — participated (should not be counted)
		{ChainID: chainID, Addr: "g1win", Moniker: "WinVal", BlockHeight: 1004, Date: now.Add(-5 * time.Minute), Participated: true, TxContribution: false},
	}

	err := db.Create(&data).Error
	require.NoError(t, err)

	// 1h window: 2 missed blocks
	results1h, err := database.GetMissedBlocksWindow(db, chainID, now.Add(-time.Hour))
	require.NoError(t, err)
	var found bool
	for _, r := range results1h {
		if r.Addr == "g1win" {
			found = true
			assert.Equal(t, 2, r.MissingBlock, "1h window should count 2 missed blocks")
		}
	}
	assert.True(t, found, "g1win should appear in 1h results")

	// 24h window: 3 missed blocks
	results24h, err := database.GetMissedBlocksWindow(db, chainID, now.Add(-24*time.Hour))
	require.NoError(t, err)
	found = false
	for _, r := range results24h {
		if r.Addr == "g1win" {
			found = true
			assert.Equal(t, 3, r.MissingBlock, "24h window should count 3 missed blocks")
		}
	}
	assert.True(t, found, "g1win should appear in 24h results")

	// 7d window: 4 missed blocks
	results7d, err := database.GetMissedBlocksWindow(db, chainID, now.Add(-7*24*time.Hour))
	require.NoError(t, err)
	found = false
	for _, r := range results7d {
		if r.Addr == "g1win" {
			found = true
			assert.Equal(t, 4, r.MissingBlock, "7d window should count 4 missed blocks")
		}
	}
	assert.True(t, found, "g1win should appear in 7d results")
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

// TestGetMissedBlocksLast24h_PrefersAddrMonikers verifies that the missed-blocks
// query resolves the display moniker from addr_monikers (kept current by the
// metrics updater) rather than the moniker frozen into each daily_participations
// row at insert time. Without the join, a validator whose name was "unknown"
// when its blocks were recorded keeps showing "unknown" even after addr_monikers
// is updated — the bug this guards against.
func TestGetMissedBlocksLast24h_PrefersAddrMonikers(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now()

	// Validator A: stale "unknown" frozen in daily_participations, but a fresh
	// name now present in addr_monikers.
	rows := []database.DailyParticipation{
		{ChainID: "test13", Addr: "g1signer", Moniker: "unknown", BlockHeight: 100, Date: now.Add(-1 * time.Hour), Participated: false},
		{ChainID: "test13", Addr: "g1signer", Moniker: "unknown", BlockHeight: 101, Date: now.Add(-2 * time.Hour), Participated: false},
		// Validator B: no addr_monikers entry, falls back to the dp moniker.
		{ChainID: "test13", Addr: "g1other", Moniker: "OtherName", BlockHeight: 102, Date: now.Add(-3 * time.Hour), Participated: false},
		// Participated row must be ignored.
		{ChainID: "test13", Addr: "g1signer", Moniker: "unknown", BlockHeight: 103, Date: now.Add(-30 * time.Minute), Participated: true},
		// Older than 24h must be ignored.
		{ChainID: "test13", Addr: "g1signer", Moniker: "unknown", BlockHeight: 104, Date: now.Add(-48 * time.Hour), Participated: false},
	}
	require.NoError(t, db.Create(&rows).Error)

	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: "test13", Addr: "g1signer", Moniker: "samourai-crew-1"}).Error)

	result, err := database.GetMissedBlocksLast24h(db, "test13")
	require.NoError(t, err)

	got := map[string]database.MissedBlockCount{}
	for _, r := range result {
		got[r.Addr] = r
	}

	assert.Equal(t, "samourai-crew-1", got["g1signer"].Moniker, "should resolve from addr_monikers, not stale dp.moniker")
	assert.Equal(t, 2, got["g1signer"].Missed, "should count only missed blocks within the last 24h")
	assert.Equal(t, "OtherName", got["g1other"].Moniker, "should fall back to dp.moniker when no addr_monikers row exists")
	assert.Equal(t, 1, got["g1other"].Missed)
}

// TestGetMissedWindows_JoinsAddrMonikersAndDoesNotFragment verifies the alert
// detection query (1) resolves the moniker from addr_monikers rather than the
// value frozen into daily_participations, and (2) treats a contiguous missed
// streak as ONE window even when the per-row moniker changes mid-streak (it
// must group by addr only, not by (addr, moniker)).
func TestGetMissedWindows_JoinsAddrMonikersAndDoesNotFragment(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now()

	// One contiguous missed streak whose frozen moniker flips "unknown" ->
	// "samourai-crew-1" partway through. Old code keyed on (addr, moniker) and
	// would split this into two windows.
	rows := []database.DailyParticipation{
		{ChainID: "test13", Addr: "g1signer", Moniker: "unknown", BlockHeight: 10, Date: now.Add(-20 * time.Minute), Participated: true},
		{ChainID: "test13", Addr: "g1signer", Moniker: "unknown", BlockHeight: 11, Date: now.Add(-19 * time.Minute), Participated: false},
		{ChainID: "test13", Addr: "g1signer", Moniker: "unknown", BlockHeight: 12, Date: now.Add(-18 * time.Minute), Participated: false},
		{ChainID: "test13", Addr: "g1signer", Moniker: "samourai-crew-1", BlockHeight: 13, Date: now.Add(-17 * time.Minute), Participated: false},
		{ChainID: "test13", Addr: "g1signer", Moniker: "samourai-crew-1", BlockHeight: 14, Date: now.Add(-16 * time.Minute), Participated: false},
		{ChainID: "test13", Addr: "g1signer", Moniker: "samourai-crew-1", BlockHeight: 15, Date: now.Add(-15 * time.Minute), Participated: false},
		// Second validator: no addr_monikers entry -> falls back to dp moniker.
		{ChainID: "test13", Addr: "g1other", Moniker: "OtherName", BlockHeight: 20, Date: now.Add(-12 * time.Minute), Participated: false},
		{ChainID: "test13", Addr: "g1other", Moniker: "OtherName", BlockHeight: 21, Date: now.Add(-11 * time.Minute), Participated: false},
		{ChainID: "test13", Addr: "g1other", Moniker: "OtherName", BlockHeight: 22, Date: now.Add(-10 * time.Minute), Participated: false},
	}
	require.NoError(t, db.Create(&rows).Error)
	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: "test13", Addr: "g1signer", Moniker: "samourai-crew-1"}).Error)

	windows, err := database.GetMissedWindows(db, "test13", 3)
	require.NoError(t, err)

	got := map[string]database.MissedWindow{}
	for _, w := range windows {
		got[w.Addr] = w
	}

	// g1signer: a single un-fragmented window of 5 missed, moniker from addr_monikers.
	signer, ok := got["g1signer"]
	require.True(t, ok, "expected a window for g1signer")
	assert.Equal(t, 5, signer.Missed, "contiguous streak must not fragment on moniker change")
	assert.Equal(t, "samourai-crew-1", signer.Moniker, "moniker must come from addr_monikers")
	assert.Equal(t, int64(11), signer.StartHeight)
	assert.Equal(t, int64(15), signer.EndHeight)

	// g1other: 3 missed, moniker falls back to the dp value.
	other, ok := got["g1other"]
	require.True(t, ok, "expected a window for g1other")
	assert.Equal(t, 3, other.Missed)
	assert.Equal(t, "OtherName", other.Moniker)
}

// TestGetAlertLog_PrefersAddrMonikers verifies the alert-history query exposed
// over the REST API resolves the current moniker from addr_monikers rather than
// the one frozen into alert_logs when the alert fired.
func TestGetAlertLog_PrefersAddrMonikers(t *testing.T) {
	db := testoutils.NewTestDB(t)

	require.NoError(t, db.Create(&database.AlertLog{
		ChainID: "test13", Addr: "g1signer", Moniker: "unknown", Level: "WARNING",
		StartHeight: 100, EndHeight: 130, SentAt: time.Now(),
	}).Error)
	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: "test13", Addr: "g1signer", Moniker: "samourai-crew-1"}).Error)

	alerts, err := database.GetAlertLog(db, "test13", "current_year")
	require.NoError(t, err)
	require.NotEmpty(t, alerts)

	var found bool
	for _, a := range alerts {
		if a.Addr == "g1signer" {
			found = true
			assert.Equal(t, "samourai-crew-1", a.Moniker, "should resolve from addr_monikers, not frozen alert_logs.moniker")
		}
	}
	assert.True(t, found, "expected the g1signer alert in the result")
}

// ── Moniker frozen-fallback (the 2026-07-08 blank-names incident) ─────────────
//
// /Participation resolved names as COALESCE(am.moniker, addr): whenever
// addr_monikers was empty or stale for a chain, EVERY validator name collapsed
// to a bare address — even though the correct name was frozen into the
// participation rows themselves. These tests pin the symmetric fallback chain:
// live addr_monikers ('unknown' excluded) → frozen row moniker → address last.

func TestGetCurrentPeriodParticipationRate_MonikerFrozenFallback(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain, addr, frozen = "test-13", "g15sysvwpves", "gfanton-1"

	// Today's raw rows carry the moniker; addr_monikers has NO row at all.
	rows := []database.DailyParticipation{
		{ChainID: chain, Addr: addr, Moniker: frozen, BlockHeight: 1, Date: time.Now(), Participated: true},
		{ChainID: chain, Addr: addr, Moniker: frozen, BlockHeight: 2, Date: time.Now(), Participated: true},
	}
	require.NoError(t, db.Create(&rows).Error)

	rates, err := database.GetCurrentPeriodParticipationRate(db, chain, "all_time")
	require.NoError(t, err)
	require.Len(t, rates, 1)
	assert.Equal(t, frozen, rates[0].Moniker,
		"empty addr_monikers must fall back to the frozen row moniker, NEVER the address")

	// A persisted 'unknown' placeholder must not beat the frozen moniker either.
	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: chain, Addr: addr, Moniker: "unknown"}).Error)
	rates, err = database.GetCurrentPeriodParticipationRate(db, chain, "all_time")
	require.NoError(t, err)
	require.Len(t, rates, 1)
	assert.Equal(t, frozen, rates[0].Moniker, "'unknown' in addr_monikers must not mask the frozen moniker")

	// A REAL addr_monikers row still wins (live resolution stays authoritative).
	require.NoError(t, db.Model(&database.AddrMoniker{}).
		Where("chain_id = ? AND addr = ?", chain, addr).
		Update("moniker", "gfanton-live").Error)
	rates, err = database.GetCurrentPeriodParticipationRate(db, chain, "all_time")
	require.NoError(t, err)
	require.Len(t, rates, 1)
	assert.Equal(t, "gfanton-live", rates[0].Moniker, "a real live moniker takes precedence over the frozen one")
}

func TestGetCurrentPeriodParticipationRate_MonikerFrozenFallback_AgregaLeg(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain, addr, frozen = "test-13", "g1agregaaddr", "agrega-val"

	// A past complete day lives only in the aggregate table (fast path).
	agrega := database.DailyParticipationAgrega{
		ChainID: chain, Addr: addr, Moniker: frozen,
		BlockDate:         time.Now().AddDate(0, 0, -2).Format("2006-01-02"),
		ParticipatedCount: 10, TotalBlocks: 10,
	}
	require.NoError(t, db.Create(&agrega).Error)

	rates, err := database.GetCurrentPeriodParticipationRate(db, chain, "all_time")
	require.NoError(t, err)
	require.Len(t, rates, 1)
	assert.Equal(t, frozen, rates[0].Moniker, "the agrega leg must thread its frozen moniker too")
}

func TestUptimeAndMissingBlock_MonikerFrozenFallback(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain, addr, frozen = "test-13", "g1uptimeaddr", "uptime-val"

	rows := []database.DailyParticipation{
		{ChainID: chain, Addr: addr, Moniker: frozen, BlockHeight: 1, Date: time.Now(), Participated: true},
		{ChainID: chain, Addr: addr, Moniker: frozen, BlockHeight: 2, Date: time.Now(), Participated: false},
	}
	require.NoError(t, db.Create(&rows).Error)

	up, err := database.UptimeMetricsaddr(db, chain)
	require.NoError(t, err)
	require.Len(t, up, 1)
	assert.Equal(t, frozen, up[0].Moniker, "uptime must use the frozen moniker when addr_monikers is empty")

	missing, err := database.MissingBlock(db, chain, "all_time")
	require.NoError(t, err)
	require.Len(t, missing, 1)
	assert.Equal(t, frozen, missing[0].Moniker, "missing-block must use the frozen moniker when addr_monikers is empty")
}
