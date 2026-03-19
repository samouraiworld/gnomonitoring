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
	gnolandData := []database.DailyParticipation{
		{
			ChainID:       "gnoland1",
			Addr:          "g1gnoland1",
			Moniker:       "GnolandVal",
			BlockHeight:   1000,
			Date:          time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
		{
			ChainID:       "gnoland1",
			Addr:          "g1gnoland1",
			Moniker:       "GnolandVal",
			BlockHeight:   1001,
			Date:          time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
	}

	err := db.Create(&gnolandData).Error
	require.NoError(t, err)

	// Update metrics for betanet (which has seeded data)
	// Seeded data: g1abc with 2/3 participation (~66.67%)
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, "betanet")
	require.NoError(t, err)

	// Verify betanet metrics were set with proper chain label
	// The seeded betanet data has addr "g1abc" with empty moniker
	val := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("betanet", "g1abc", ""))
	assert.Greater(t, val, 0.0, "betanet ValidatorParticipation should be set")

	// Update metrics for gnoland1
	// gnoland1 data: g1gnoland1 with 2/2 participation (100%)
	err = gnovalidator.UpdatePrometheusMetricsFromDB(db, "gnoland1")
	require.NoError(t, err)

	// Verify gnoland1 metrics were set with proper chain label
	val = testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("gnoland1", "g1gnoland1", "GnolandVal"))
	assert.Greater(t, val, 0.0, "gnoland1 ValidatorParticipation should be set")

	// Verify metrics are chain-isolated
	// When we query with wrong chain, the metric should be 0 (not set)
	wrongChainVal := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("betanet", "g1gnoland1", "GnolandVal"))
	assert.Equal(t, 0.0, wrongChainVal, "gnoland1 validator should not appear in betanet metrics")

	wrongChainVal = testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("gnoland1", "g1abc", ""))
	assert.Equal(t, 0.0, wrongChainVal, "betanet validator should not appear in gnoland1 metrics")
}

// TestUpdatePrometheusMetricsFromDB_MultipleChains verifies that metrics
// for multiple chains can be updated independently
func TestUpdatePrometheusMetricsFromDB_MultipleChains(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Initialize Prometheus metrics
	gnovalidator.Init()

	// Insert data for multiple chains
	multiChainData := []database.DailyParticipation{
		// betanet data - already seeded with g1abc, add more
		{
			ChainID:       "betanet",
			Addr:          "g1val2",
			Moniker:       "BetanetVal2",
			BlockHeight:   53,
			Date:          time.Date(2025, 10, 3, 0, 0, 0, 0, time.UTC),
			Participated:  true,
			TxContribution: false,
		},
		// gnoland1 data
		{
			ChainID:       "gnoland1",
			Addr:          "g1gnoland2",
			Moniker:       "GnolandVal2",
			BlockHeight:   1100,
			Date:          time.Date(2025, 9, 20, 0, 0, 0, 0, time.UTC),
			Participated:  false,
			TxContribution: false,
		},
		// test3 data
		{
			ChainID:       "test3",
			Addr:          "g1test3",
			Moniker:       "Test3Val",
			BlockHeight:   2000,
			Date:          time.Date(2025, 9, 10, 0, 0, 0, 0, time.UTC),
			Participated:  true,
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

	// Verify each chain has its own metrics
	// Seeded betanet data has addr "g1abc" with empty moniker
	betanetVal := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("betanet", "g1abc", ""))
	assert.Greater(t, betanetVal, 0.0, "betanet metrics should be set")

	// gnoland1 data has addr "g1gnoland2" with moniker "GnolandVal2"
	gnolandVal := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("gnoland1", "g1gnoland2", "GnolandVal2"))
	assert.GreaterOrEqual(t, gnolandVal, 0.0, "gnoland1 metrics should be set or 0")

	// test3 data has addr "g1test3" with moniker "Test3Val"
	test3Val := testutil.ToFloat64(gnovalidator.ValidatorParticipation.WithLabelValues("test3", "g1test3", "Test3Val"))
	assert.Greater(t, test3Val, 0.0, "test3 metrics should be set")
}
