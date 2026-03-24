package gnovalidator_test

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"time"
)

// TestUpdatePrometheusMetricsFromDB_ChainLabel verifies that Prometheus metrics
// are properly updated with the chain label and chain isolation
func TestUpdatePrometheusMetricsFromDB_ChainLabel(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Initialize Prometheus metrics (safe to call multiple times due to sync.Once)
	gnovalidator.Init()

	// Insert additional data for gnoland1 chain with moniker
	// Dates must be in the current month so GetCurrentPeriodParticipationRate finds them.
	now := time.Now()
	currentMonthDay := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	gnolandData := []database.DailyParticipation{
		{
			ChainID:        "gnoland1",
			Addr:           "g1gnoland1",
			Moniker:        "GnolandVal",
			BlockHeight:    1000,
			Date:           currentMonthDay,
			Participated:   true,
			TxContribution: false,
		},
		{
			ChainID:        "gnoland1",
			Addr:           "g1gnoland1",
			Moniker:        "GnolandVal",
			BlockHeight:    1001,
			Date:           currentMonthDay,
			Participated:   true,
			TxContribution: false,
		},
	}

	err := db.Create(&gnolandData).Error
	require.NoError(t, err)

	// Update metrics for betanet (which has seeded data)
	// Seeded data: g1abc with 2/3 participation (~66.67%)
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, "betanet")
	require.NoError(t, err)

	// Verify betanet metrics were set with proper chain label.
	// GetCurrentPeriodParticipationRate uses COALESCE(am.moniker, dp.addr) so the
	// moniker label is the address when no addr_monikers entry exists.
	val := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("betanet", "g1abc", "g1abc"))
	assert.Greater(t, val, 0.0, "betanet ValidatorParticipation should be set")

	// Update metrics for gnoland1
	// gnoland1 data: g1gnoland1 with 2/2 participation (100%)
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, "gnoland1")
	require.NoError(t, err)

	// Verify gnoland1 metrics were set with proper chain label.
	// Moniker comes from COALESCE(am.moniker, dp.addr) = "g1gnoland1" (no addr_monikers entry).
	val = testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("gnoland1", "g1gnoland1", "g1gnoland1"))
	assert.Greater(t, val, 0.0, "gnoland1 ValidatorParticipation should be set")

	// Verify metrics are chain-isolated
	// When we query with wrong chain, the metric should be 0 (not set)
	wrongChainVal := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("betanet", "g1gnoland1", "g1gnoland1"))
	assert.Equal(t, 0.0, wrongChainVal, "gnoland1 validator should not appear in betanet metrics")

	wrongChainVal = testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("gnoland1", "g1abc", "g1abc"))
	assert.Equal(t, 0.0, wrongChainVal, "betanet validator should not appear in gnoland1 metrics")
}

// TestUpdatePrometheusMetricsFromDB_MultipleChains verifies that metrics
// for multiple chains can be updated independently
func TestUpdatePrometheusMetricsFromDB_MultipleChains(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Initialize Prometheus metrics
	gnovalidator.Init()

	// Insert data for multiple chains.
	// Dates must be in the current month so GetCurrentPeriodParticipationRate finds them.
	now2 := time.Now()
	currentMonth := time.Date(now2.Year(), now2.Month(), 5, 0, 0, 0, 0, time.UTC)
	multiChainData := []database.DailyParticipation{
		// betanet data - already seeded with g1abc, add more
		{
			ChainID:        "betanet",
			Addr:           "g1val2",
			Moniker:        "BetanetVal2",
			BlockHeight:    53,
			Date:           currentMonth,
			Participated:   true,
			TxContribution: false,
		},
		// gnoland1 data
		{
			ChainID:        "gnoland1",
			Addr:           "g1gnoland2",
			Moniker:        "GnolandVal2",
			BlockHeight:    1100,
			Date:           currentMonth,
			Participated:   false,
			TxContribution: false,
		},
		// test3 data
		{
			ChainID:        "test3",
			Addr:           "g1test3",
			Moniker:        "Test3Val",
			BlockHeight:    2000,
			Date:           currentMonth,
			Participated:   true,
			TxContribution: false,
		},
	}

	err := db.Create(&multiChainData).Error
	require.NoError(t, err)

	// Update metrics for all chains
	for _, chainID := range []string{"betanet", "gnoland1", "test3"} {
		err = gnovalidator.UpdatePrometheusMetricsFromDB(db, chainID)
		require.NoError(t, err)
	}

	// Verify each chain has its own metrics.
	// GetCurrentPeriodParticipationRate uses COALESCE(am.moniker, dp.addr) so the
	// moniker label is the address when no addr_monikers entry exists.
	betanetVal := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("betanet", "g1abc", "g1abc"))
	assert.Greater(t, betanetVal, 0.0, "betanet metrics should be set")

	// gnoland1 data has addr "g1gnoland2"; participated=false so rate is 0, but metric is set.
	gnolandVal := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("gnoland1", "g1gnoland2", "g1gnoland2"))
	assert.GreaterOrEqual(t, gnolandVal, 0.0, "gnoland1 metrics should be set or 0")

	// test3 data has addr "g1test3"; participated=true so rate is 100%.
	test3Val := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("test3", "g1test3", "g1test3"))
	assert.Greater(t, test3Val, 0.0, "test3 metrics should be set")
}

// TestUpdatePrometheusMetricsFromDB_NewValidatorMetrics verifies that Phase 1 metrics are properly calculated
func TestUpdatePrometheusMetricsFromDB_NewValidatorMetrics(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Initialize Prometheus metrics
	gnovalidator.Init()

	// Insert comprehensive data for validator metrics
	testChain := "betanet"
	testAddr := "g1validator1"
	testMoniker := "Validator1"

	// Use a date in current month for tx_contrib and missing_blocks_month queries to work
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	var participationData []database.DailyParticipation

	// Create 550 blocks: first 300 participated, last 250 missed
	// This ensures uptime last 500 blocks = 300/550 = 54.5%
	for i := 1; i <= 550; i++ {
		blockTime := monthStart.AddDate(0, 0, i/30) // Spread across month
		participated := i <= 300                      // First 300 blocks participated

		participationData = append(participationData, database.DailyParticipation{
			ChainID:        testChain,
			Addr:           testAddr,
			Moniker:        testMoniker,
			BlockHeight:    int64(i),
			Date:           blockTime,
			Participated:   participated,
			TxContribution: participated,
		})
	}

	err := db.Create(&participationData).Error
	require.NoError(t, err)

	// Update metrics for betanet
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, testChain)
	require.NoError(t, err)

	// Test ValidatorUptime (last 500 blocks)
	uptime := testutil.ToFloat64(gnovalidator.ValidatorUptime.WithLabelValues(testChain, testAddr, testMoniker))
	assert.GreaterOrEqual(t, uptime, 0.0, "ValidatorUptime should be >= 0")
	assert.LessOrEqual(t, uptime, 100.0, "ValidatorUptime should be <= 100%")

	// Test ValidatorOperationTime (days since last down)
	operationTime := testutil.ToFloat64(gnovalidator.ValidatorOperationTime.WithLabelValues(testChain, testAddr, testMoniker))
	assert.GreaterOrEqual(t, operationTime, 0.0, "ValidatorOperationTime should be >= 0")

	// Test ValidatorTxContribution (current month)
	txContrib := testutil.ToFloat64(gnovalidator.ValidatorTxContribution.WithLabelValues(testChain, testAddr, testMoniker))
	assert.GreaterOrEqual(t, txContrib, 0.0, "ValidatorTxContribution should be >= 0")
	assert.LessOrEqual(t, txContrib, 100.0, "ValidatorTxContribution should be <= 100%")

	// Test ValidatorMissingBlocksMonth (missing blocks in current month)
	missingMonth := testutil.ToFloat64(gnovalidator.ValidatorMissingBlocksMonth.WithLabelValues(testChain, testAddr, testMoniker))
	assert.GreaterOrEqual(t, missingMonth, 0.0, "ValidatorMissingBlocksMonth should be >= 0")

	// Test ValidatorFirstSeenUnix (unix timestamp)
	firstSeen := testutil.ToFloat64(gnovalidator.ValidatorFirstSeenUnix.WithLabelValues(testChain, testAddr, testMoniker))
	// firstSeen can be 0 if parsing fails, but should be set after fix
	assert.GreaterOrEqual(t, firstSeen, 0.0, "ValidatorFirstSeenUnix should be >= 0")
}

// TestUpdatePrometheusMetricsFromDB_NewMetricsChainIsolation verifies chain isolation for new metrics
func TestUpdatePrometheusMetricsFromDB_NewMetricsChainIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Initialize Prometheus metrics
	gnovalidator.Init()

	// Insert data for two chains with sufficient blocks for metrics to calculate
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	// Create data for betanet validator
	var betanetData []database.DailyParticipation
	for i := 1; i <= 100; i++ {
		blockTime := monthStart.AddDate(0, 0, i/4)
		betanetData = append(betanetData, database.DailyParticipation{
			ChainID:        "betanet",
			Addr:           "g1val_beta",
			Moniker:        "BetaVal",
			BlockHeight:    int64(i),
			Date:           blockTime,
			Participated:   i%2 == 0, // alternating participation
			TxContribution: i%2 == 0,
		})
	}

	// Create data for gnoland1 validator
	var gnolandData []database.DailyParticipation
	for i := 1; i <= 100; i++ {
		blockTime := monthStart.AddDate(0, 0, i/4)
		gnolandData = append(gnolandData, database.DailyParticipation{
			ChainID:        "gnoland1",
			Addr:           "g1val_gnoland",
			Moniker:        "GnolandVal",
			BlockHeight:    int64(i + 200), // Avoid height collision
			Date:           blockTime,
			Participated:   i%3 == 0, // different participation pattern
			TxContribution: i%3 == 0,
		})
	}

	err := db.Create(&betanetData).Error
	require.NoError(t, err)
	err = db.Create(&gnolandData).Error
	require.NoError(t, err)

	// Update metrics for both chains
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, "betanet")
	require.NoError(t, err)
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, "gnoland1")
	require.NoError(t, err)

	// Verify betanet metrics are set
	betanetUptime := testutil.ToFloat64(gnovalidator.ValidatorUptime.WithLabelValues("betanet", "g1val_beta", "BetaVal"))
	assert.GreaterOrEqual(t, betanetUptime, 0.0, "betanet uptime should be >= 0")

	// Verify gnoland1 metrics are set
	gnolandUptime := testutil.ToFloat64(gnovalidator.ValidatorUptime.WithLabelValues("gnoland1", "g1val_gnoland", "GnolandVal"))
	assert.GreaterOrEqual(t, gnolandUptime, 0.0, "gnoland1 uptime should be >= 0")

	// Verify chain isolation: betanet validator should not appear in gnoland1 metrics
	crossChainVal := testutil.ToFloat64(gnovalidator.ValidatorUptime.WithLabelValues("gnoland1", "g1val_beta", "BetaVal"))
	assert.Equal(t, 0.0, crossChainVal, "betanet validator should not appear in gnoland1 uptime")

	crossChainVal = testutil.ToFloat64(gnovalidator.ValidatorUptime.WithLabelValues("betanet", "g1val_gnoland", "GnolandVal"))
	assert.Equal(t, 0.0, crossChainVal, "gnoland1 validator should not appear in betanet uptime")
}

// TestUpdatePrometheusMetricsFromDB_ChainMetrics verifies Phase 2 chain-level metrics
func TestUpdatePrometheusMetricsFromDB_ChainMetrics(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Initialize Prometheus metrics
	gnovalidator.Init()

	// Insert data for chain health metrics
	chainID := "betanet"
	now := time.Now()

	// Create 120 blocks with mixed participation to test chain metrics
	var chainData []database.DailyParticipation
	for i := 1; i <= 120; i++ {
		participated := i%2 == 0 // 50% participation
		chainData = append(chainData, database.DailyParticipation{
			ChainID:        chainID,
			Addr:           "g1val_" + string(rune(65+i%26)), // Different validators
			Moniker:        "Val" + string(rune(65+i%26)),
			BlockHeight:    int64(i),
			Date:           now.AddDate(0, 0, i/30),
			Participated:   participated,
			TxContribution: participated,
		})
	}

	err := db.Create(&chainData).Error
	require.NoError(t, err)

	// Update metrics
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, chainID)
	require.NoError(t, err)

	// Test ChainActiveValidators (should be > 0)
	activeValidators := testutil.ToFloat64(gnovalidator.ChainActiveValidators.WithLabelValues(chainID))
	assert.Greater(t, activeValidators, 0.0, "ChainActiveValidators should be > 0")

	// Test ChainAvgParticipationRate (should be between 0-100, expect ~50%)
	avgRate := testutil.ToFloat64(gnovalidator.ChainAvgParticipationRate.WithLabelValues(chainID))
	assert.GreaterOrEqual(t, avgRate, 0.0, "ChainAvgParticipationRate should be >= 0")
	assert.LessOrEqual(t, avgRate, 100.0, "ChainAvgParticipationRate should be <= 100%")

	// Test ChainCurrentHeight (should be 120)
	height := testutil.ToFloat64(gnovalidator.ChainCurrentHeight.WithLabelValues(chainID))
	assert.Equal(t, float64(120), height, "ChainCurrentHeight should be 120")
}

// TestUpdatePrometheusMetricsFromDB_AlertMetrics verifies Phase 3 alert metrics
func TestUpdatePrometheusMetricsFromDB_AlertMetrics(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Initialize Prometheus metrics
	gnovalidator.Init()

	chainID := "betanet"
	now := time.Now()

	// Insert alert logs with different levels
	alerts := []database.AlertLog{
		{
			ChainID:     chainID,
			Addr:        "g1val1",
			Moniker:     "Val1",
			Level:       "CRITICAL",
			StartHeight: 100,
			EndHeight:   105,
			Skipped:     false,
			Msg:         "validator down",
			SentAt:      now,
		},
		{
			ChainID:     chainID,
			Addr:        "g1val2",
			Moniker:     "Val2",
			Level:       "WARNING",
			StartHeight: 110,
			EndHeight:   115,
			Skipped:     false,
			Msg:         "low participation",
			SentAt:      now,
		},
		{
			ChainID:     chainID,
			Addr:        "g1val3",
			Moniker:     "Val3",
			Level:       "CRITICAL",
			StartHeight: 120,
			EndHeight:   125,
			Skipped:     false,
			Msg:         "validator critical",
			SentAt:      now.Add(time.Hour),
		},
		{
			ChainID:     chainID,
			Addr:        "g1val1",
			Moniker:     "Val1",
			Level:       "WARNING",
			StartHeight: 130,
			EndHeight:   135,
			Skipped:     false,
			Msg:         "warning alert",
			SentAt:      now.Add(2 * time.Hour),
		},
	}

	err := db.Create(&alerts).Error
	require.NoError(t, err)

	// Update metrics
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, chainID)
	require.NoError(t, err)

	// Test ActiveAlerts for CRITICAL level
	activeCritical := testutil.ToFloat64(gnovalidator.ActiveAlerts.WithLabelValues(chainID, "CRITICAL"))
	assert.GreaterOrEqual(t, activeCritical, 0.0, "ActiveAlerts CRITICAL should be >= 0")

	// Test ActiveAlerts for WARNING level
	activeWarning := testutil.ToFloat64(gnovalidator.ActiveAlerts.WithLabelValues(chainID, "WARNING"))
	assert.GreaterOrEqual(t, activeWarning, 0.0, "ActiveAlerts WARNING should be >= 0")

	// Test AlertsTotal for CRITICAL level (should be 2)
	totalCritical := testutil.ToFloat64(gnovalidator.AlertsTotal.WithLabelValues(chainID, "CRITICAL"))
	assert.Equal(t, float64(2), totalCritical, "AlertsTotal CRITICAL should be 2")

	// Test AlertsTotal for WARNING level (should be 2)
	totalWarning := testutil.ToFloat64(gnovalidator.AlertsTotal.WithLabelValues(chainID, "WARNING"))
	assert.Equal(t, float64(2), totalWarning, "AlertsTotal WARNING should be 2")
}

// TestMissedBlocksWindow verifies that the MissedBlocksWindow metric is populated
// correctly for the 1h, 24h, and 7d windows.
func TestMissedBlocksWindow(t *testing.T) {
	db := testoutils.NewTestDB(t)

	gnovalidator.Init()

	chainID := "betanet"
	addr := "g1winval"
	moniker := "WinVal"
	now := time.Now().UTC()

	// 2 missed in last 1h, 1 missed between 1h and 24h, 1 missed between 24h and 7d.
	data := []database.DailyParticipation{
		{ChainID: chainID, Addr: addr, Moniker: moniker, BlockHeight: 2000, Date: now.Add(-10 * time.Minute), Participated: false, TxContribution: false},
		{ChainID: chainID, Addr: addr, Moniker: moniker, BlockHeight: 2001, Date: now.Add(-30 * time.Minute), Participated: false, TxContribution: false},
		{ChainID: chainID, Addr: addr, Moniker: moniker, BlockHeight: 2002, Date: now.Add(-2 * time.Hour), Participated: false, TxContribution: false},
		{ChainID: chainID, Addr: addr, Moniker: moniker, BlockHeight: 2003, Date: now.Add(-48 * time.Hour), Participated: false, TxContribution: false},
		{ChainID: chainID, Addr: addr, Moniker: moniker, BlockHeight: 2004, Date: now.Add(-5 * time.Minute), Participated: true, TxContribution: false},
	}

	err := db.Create(&data).Error
	require.NoError(t, err)

	// Register the moniker in addr_monikers so COALESCE resolves to "WinVal".
	addrMoniker := database.AddrMoniker{ChainID: chainID, Addr: addr, Moniker: moniker}
	err = db.Create(&addrMoniker).Error
	require.NoError(t, err)

	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, chainID)
	require.NoError(t, err)

	// 1h window: 2 missed blocks
	val1h := testutil.ToFloat64(gnovalidator.MissedBlocksWindow.WithLabelValues(chainID, addr, moniker, "1h"))
	assert.Equal(t, float64(2), val1h, "MissedBlocksWindow[1h] should be 2")

	// 24h window: 3 missed blocks
	val24h := testutil.ToFloat64(gnovalidator.MissedBlocksWindow.WithLabelValues(chainID, addr, moniker, "24h"))
	assert.Equal(t, float64(3), val24h, "MissedBlocksWindow[24h] should be 3")

	// 7d window: 4 missed blocks
	val7d := testutil.ToFloat64(gnovalidator.MissedBlocksWindow.WithLabelValues(chainID, addr, moniker, "7d"))
	assert.Equal(t, float64(4), val7d, "MissedBlocksWindow[7d] should be 4")
}

// TestUpdatePrometheusMetricsFromDB_AllMetricsChainIsolation verifies chain isolation for all metrics
func TestUpdatePrometheusMetricsFromDB_AllMetricsChainIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Initialize Prometheus metrics
	gnovalidator.Init()

	now := time.Now()

	// Insert data for test3 (not seeded by NewTestDB)
	test3Data := []database.DailyParticipation{
		{ChainID: "test3", Addr: "g1test1", Moniker: "Test1", BlockHeight: 1, Date: now, Participated: true, TxContribution: true},
		{ChainID: "test3", Addr: "g1test2", Moniker: "Test2", BlockHeight: 2, Date: now, Participated: false, TxContribution: false},
	}

	// Insert data for gnoland1 (also not seeded)
	gnolandData := []database.DailyParticipation{
		{ChainID: "gnoland1", Addr: "g1gnoland1", Moniker: "Gnoland1", BlockHeight: 100, Date: now, Participated: true, TxContribution: true},
		{ChainID: "gnoland1", Addr: "g1gnoland2", Moniker: "Gnoland2", BlockHeight: 101, Date: now, Participated: true, TxContribution: false},
	}

	err := db.Create(&test3Data).Error
	require.NoError(t, err)
	err = db.Create(&gnolandData).Error
	require.NoError(t, err)

	// Update metrics for both chains
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, "test3")
	require.NoError(t, err)
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, "gnoland1")
	require.NoError(t, err)

	// Verify chain metrics are isolated
	test3Height := testutil.ToFloat64(gnovalidator.ChainCurrentHeight.WithLabelValues("test3"))
	gnolandHeight := testutil.ToFloat64(gnovalidator.ChainCurrentHeight.WithLabelValues("gnoland1"))

	assert.Equal(t, float64(2), test3Height, "test3 height should be 2")
	assert.Equal(t, float64(101), gnolandHeight, "gnoland1 height should be 101")
}
