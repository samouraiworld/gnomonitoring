package database_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetPrecommitLatencyMultiWindow(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "latency-window-test"
	now := time.Now().UTC()
	i64 := func(v int64) *int64 { return &v }
	b := func(v bool) *bool { return &v }

	rows := []database.DailyParticipation{
		{ChainID: chainID, Addr: "g1aaa", BlockHeight: 1, Date: now.Add(-10 * time.Minute), Participated: true, PrecommitLagMs: i64(10), LateForQuorum: b(false)},
		{ChainID: chainID, Addr: "g1aaa", BlockHeight: 2, Date: now.Add(-9 * time.Minute), Participated: true, PrecommitLagMs: i64(20), LateForQuorum: b(true)},
		{ChainID: chainID, Addr: "g1aaa", BlockHeight: 3, Date: now.Add(-5 * time.Minute), Participated: false},
		{ChainID: chainID, Addr: "g1aaa", BlockHeight: 4, Date: now.Add(-2 * time.Hour), Participated: true, PrecommitLagMs: i64(100), LateForQuorum: b(true)},
		{ChainID: chainID, Addr: "g1aaa", BlockHeight: 5, Date: now.Add(-48 * time.Hour), Participated: true, PrecommitLagMs: i64(1000)},
		{ChainID: chainID, Addr: "g1old", BlockHeight: 6, Date: now.Add(-8 * 24 * time.Hour), Participated: true, PrecommitLagMs: i64(5), LateForQuorum: b(false)},
		{ChainID: "latency-window-other", Addr: "g1aaa", BlockHeight: 1, Date: now.Add(-10 * time.Minute), Participated: true, PrecommitLagMs: i64(9999), LateForQuorum: b(true)},
	}
	require.NoError(t, db.Create(&rows).Error)

	got, err := database.GetPrecommitLatencyMultiWindow(db, chainID)
	require.NoError(t, err)
	require.Len(t, got, 1, "only g1aaa has samples within 7d on this chain")

	s := got[0]
	assert.Equal(t, "g1aaa", s.Addr)
	assert.Equal(t, "g1aaa", s.Moniker, "no stored moniker: the moniker label falls back to the address")

	requireFloat := func(name string, p *float64, want float64) {
		t.Helper()
		require.NotNil(t, p, name)
		assert.InDelta(t, want, *p, 0.001, name)
	}
	requireFloat("p50 1h", s.LagP50_1h, 15)
	requireFloat("p90 1h", s.LagP90_1h, 19)
	requireFloat("ratio 1h", s.LateRatio1h, 0.5)
	requireFloat("p50 24h", s.LagP50_24h, 20)
	requireFloat("p90 24h", s.LagP90_24h, 84)
	requireFloat("ratio 24h", s.LateRatio24h, 2.0/3.0)
	requireFloat("p50 7d", s.LagP50_7d, 60)
	requireFloat("p90 7d", s.LagP90_7d, 730)
	requireFloat("ratio 7d", s.LateRatio7d, 2.0/3.0)
}

func TestGetPrecommitLatencyMultiWindow_EmptyWindowIsNil(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "latency-window-empty"
	lag := int64(50)
	require.NoError(t, db.Create(&database.DailyParticipation{
		ChainID: chainID, Addr: "g1aaa", BlockHeight: 1, Date: time.Now().UTC().Add(-3 * time.Hour),
		Participated: true, PrecommitLagMs: &lag,
	}).Error)

	got, err := database.GetPrecommitLatencyMultiWindow(db, chainID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Nil(t, got[0].LagP50_1h)
	assert.Nil(t, got[0].LateRatio1h)
	assert.Nil(t, got[0].LateRatio24h, "no late_for_quorum sample: ratio is NULL, not 0")
	require.NotNil(t, got[0].LagP50_24h)
	assert.InDelta(t, 50.0, *got[0].LagP50_24h, 0.001)
}
