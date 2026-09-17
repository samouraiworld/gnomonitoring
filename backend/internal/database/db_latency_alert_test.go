package database_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetLatencyAlertStats(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "lat-alert-stats"
	now := time.Now().UTC()
	i64 := func(v int64) *int64 { return &v }
	b := func(v bool) *bool { return &v }

	rows := []database.DailyParticipation{
		{ChainID: chainID, Addr: "g1a", BlockHeight: 10, Date: now.Add(-10 * time.Minute), Participated: true, PrecommitLagMs: i64(10), LateForQuorum: b(false)},
		{ChainID: chainID, Addr: "g1a", BlockHeight: 11, Date: now.Add(-9 * time.Minute), Participated: true, PrecommitLagMs: i64(30), LateForQuorum: b(true)},
		{ChainID: chainID, Addr: "g1a", BlockHeight: 12, Date: now.Add(-8 * time.Minute), Participated: false},
		{ChainID: chainID, Addr: "g1a", BlockHeight: 13, Date: now.Add(-90 * time.Minute), Participated: true, PrecommitLagMs: i64(9999), LateForQuorum: b(true)},
		{ChainID: "lat-alert-other", Addr: "g1a", BlockHeight: 14, Date: now.Add(-5 * time.Minute), Participated: true, PrecommitLagMs: i64(8888), LateForQuorum: b(true)},
	}
	require.NoError(t, db.Create(&rows).Error)

	got, err := database.GetLatencyAlertStats(db, chainID, 60)
	require.NoError(t, err)
	require.Len(t, got, 1)

	s := got[0]
	assert.Equal(t, "g1a", s.Addr)
	assert.Equal(t, "g1a", s.Moniker, "with no stored moniker the address is used")
	assert.Equal(t, 2, s.Samples, "unsigned blocks and rows outside the window are not samples")
	assert.InDelta(t, 20.0, s.P50, 0.001)
	assert.InDelta(t, 28.0, s.P90, 0.001)
	require.NotNil(t, s.LateRatio)
	assert.InDelta(t, 0.5, *s.LateRatio, 0.001)
	assert.Equal(t, int64(10), s.MinHeight)
	assert.Equal(t, int64(11), s.MaxHeight)
}

func TestGetLatencyAlertStates(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "lat-alert-state"
	now := time.Now().UTC()

	require.NoError(t, db.Create(&[]database.AlertLog{
		{ChainID: chainID, Addr: "g1a", Moniker: "A", Level: "LATENCY", SentAt: now.Add(-2 * time.Hour)},
		{ChainID: chainID, Addr: "g1a", Moniker: "A", Level: "LATENCY_RESOLVED", SentAt: now.Add(-1 * time.Hour)},
		{ChainID: chainID, Addr: "g1b", Moniker: "B", Level: "LATENCY", SentAt: now.Add(-30 * time.Minute)},
		{ChainID: chainID, Addr: "g1c", Moniker: "C", Level: "WARNING", SentAt: now.Add(-5 * time.Minute)},
		{ChainID: "lat-alert-state-other", Addr: "g1b", Moniker: "B", Level: "LATENCY", SentAt: now},
	}).Error)

	got, err := database.GetLatencyAlertStates(db, chainID)
	require.NoError(t, err)

	require.Contains(t, got, "g1a")
	assert.Equal(t, "LATENCY_RESOLVED", got["g1a"].Level, "the most recent latency row wins")
	require.Contains(t, got, "g1b")
	assert.Equal(t, "LATENCY", got["g1b"].Level)
	assert.NotContains(t, got, "g1c", "missed-block levels are not latency state")
	assert.Len(t, got, 2)
}
