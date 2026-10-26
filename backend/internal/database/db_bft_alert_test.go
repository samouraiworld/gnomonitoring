package database_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedBFTRows(t *testing.T, db *gorm.DB, chainID, addr string, from, to int64, signed func(h int64) bool) {
	t.Helper()
	rows := make([]database.DailyParticipation, 0, to-from+1)
	for h := from; h <= to; h++ {
		rows = append(rows, database.DailyParticipation{
			ChainID: chainID, Addr: addr, Moniker: addr, BlockHeight: h,
			Date: time.Now().UTC(), Participated: signed(h),
		})
	}
	require.NoError(t, db.Create(&rows).Error)
}

func TestGetBFTWindow(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "bft-window"
	for addr, vp := range map[string]int64{"g1a": 10, "g1b": 10, "g1c": 11, "g1d": 5, "g1gone": 0} {
		require.NoError(t, db.Create(&database.AddrMoniker{ChainID: chain, Addr: addr, Moniker: "m-" + addr, VotingPower: vp}).Error)
	}
	all := func(int64) bool { return true }
	seedBFTRows(t, db, chain, "g1a", 1, 200, all)
	// g1b stopped signing at height 170: 30 of the last 100 blocks missed.
	seedBFTRows(t, db, chain, "g1b", 1, 200, func(h int64) bool { return h <= 170 })
	seedBFTRows(t, db, chain, "g1gone", 1, 200, all)
	// g1d signed nothing in the window: every row is a trailing miss.
	seedBFTRows(t, db, chain, "g1d", 101, 200, func(int64) bool { return false })
	// g1c holds voting power but has no row in the window at all.
	seedBFTRows(t, db, chain, "g1c", 1, 50, all)
	// Another chain must not leak into the window.
	seedBFTRows(t, db, "other", "g1a", 1, 500, func(int64) bool { return false })

	w, err := database.GetBFTWindow(db, chain, 100)
	require.NoError(t, err)

	assert.Equal(t, int64(101), w.MinHeight)
	assert.Equal(t, int64(200), w.MaxHeight)
	assert.Equal(t, int64(100), w.Heights)
	assert.WithinDuration(t, time.Now(), w.LastBlockTime, time.Minute)
	byAddr := map[string]database.BFTWindowEntry{}
	for _, e := range w.Entries {
		byAddr[e.Addr] = e
	}
	assert.Len(t, byAddr, 4, "only voting-power holders are returned")
	assert.Equal(t, int64(100), byAddr["g1d"].TrailingMissed)
	assert.Equal(t, database.BFTWindowEntry{Addr: "g1a", Moniker: "m-g1a", VotingPower: 10, Blocks: 100, Signed: 100}, byAddr["g1a"])
	assert.Equal(t, int64(70), byAddr["g1b"].Signed)
	assert.Equal(t, int64(30), byAddr["g1b"].TrailingMissed)
	assert.Equal(t, int64(0), byAddr["g1a"].TrailingMissed)
	assert.Equal(t, int64(0), byAddr["g1c"].Blocks)
	assert.Equal(t, int64(0), byAddr["g1c"].Signed)
}

func TestGetBFTWindow_EmptyChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	w, err := database.GetBFTWindow(db, "bft-empty", 100)
	require.NoError(t, err)
	assert.Equal(t, int64(0), w.MaxHeight)
	assert.Empty(t, w.Entries)
}

func TestGetBFTAlertState(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "bft-state"
	base := time.Now().UTC().Add(-time.Hour)

	st, err := database.GetBFTAlertState(db, chain)
	require.NoError(t, err)
	assert.Equal(t, "", st.Level, "no row yet")

	require.NoError(t, database.InsertAlertlog(db, chain, "bft", "bft", "BFT_WARNING", 1, 100, false, base, ""))
	require.NoError(t, database.InsertAlertlog(db, chain, "bft", "bft", "BFT_CRITICAL", 1, 100, false, base.Add(10*time.Minute), ""))
	// Later rows of other families or chains must not mask the BFT state.
	require.NoError(t, database.InsertAlertlog(db, chain, "bft", "bft", "RESOLVED", 1, 100, false, base.Add(20*time.Minute), ""))
	require.NoError(t, database.InsertAlertlog(db, chain, "rpc", "rpc", "CRITICAL", 0, 0, false, base.Add(30*time.Minute), ""))
	require.NoError(t, database.InsertAlertlog(db, "other", "bft", "bft", "BFT_RESOLVED", 1, 100, false, base.Add(40*time.Minute), ""))

	st, err = database.GetBFTAlertState(db, chain)
	require.NoError(t, err)
	assert.Equal(t, "BFT_CRITICAL", st.Level)
	assert.WithinDuration(t, base.Add(10*time.Minute), st.SentAt, time.Second)
}

// TestBFTRowsExcludedFromValidatorQueries pins that BFT-margin rows never
// show up as a fake validator: not in the score, not in the last-alert
// times, not in the active missed-block alert count. Rows are seeded with
// both the dedicated levels and the plain ones, so the addr exclusion is
// exercised on its own.
func TestBFTRowsExcludedFromValidatorQueries(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "bft-excl"
	now := time.Now().UTC()
	inMonth := time.Date(now.Year(), now.Month(), 2, 12, 0, 0, 0, time.UTC)

	seedScoreAlert(t, db, chain, "g1aaa", "CRITICAL", 100, 130, inMonth)
	seedScoreAlert(t, db, chain, "bft", "BFT_CRITICAL", 1, 999, inMonth.Add(time.Hour))
	seedScoreAlert(t, db, chain, "bft", "CRITICAL", 1, 999, inMonth.Add(2*time.Hour))
	seedScoreAlert(t, db, chain, "bft", "WARNING", 1, 999, inMonth.Add(3*time.Hour))

	rows, err := database.GetValidatorScores(db, chain, "current_month")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "g1aaa", rows[0].Addr)

	last, err := database.GetLastAlertTimes(db, chain)
	require.NoError(t, err)
	_, hasBFT := last["bft"]
	assert.False(t, hasBFT, "addr 'bft' must be excluded from GetLastAlertTimes")

	// Only the dedicated level reaches the active-alert CTE in production;
	// it must not count as an open missed-block alert.
	db2 := testoutils.NewTestDB(t)
	require.NoError(t, database.InsertAlertlog(db2, chain, "bft", "bft", "BFT_CRITICAL", 1, 100, false, now, ""))
	n, err := database.GetActiveAlertCount(db2, chain, "CRITICAL")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestGetBFTWindow_CountsCollectedHeights(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "bft-gaps"
	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: chain, Addr: "g1a", Moniker: "a", VotingPower: 10}).Error)
	// Heights 101..200 with every other one missing (fetch failures).
	seedBFTRows(t, db, chain, "g1a", 101, 200, func(int64) bool { return true })
	require.NoError(t, db.Exec(`DELETE FROM daily_participations WHERE chain_id = ? AND block_height % 2 = 1`, chain).Error)

	w, err := database.GetBFTWindow(db, chain, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(50), w.Heights)
	require.Len(t, w.Entries, 1)
	assert.Equal(t, int64(50), w.Entries[0].Blocks)
}
