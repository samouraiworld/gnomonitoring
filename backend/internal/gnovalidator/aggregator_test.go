package gnovalidator_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testChain = "betanet"

// seedRaw clears daily_participations for testChain then inserts the given rows.
// This avoids interference from FakeData rows inserted by NewTestDB.
func seedRaw(t *testing.T, db *gorm.DB, rows []database.DailyParticipation) {
	t.Helper()
	require.NoError(t, db.Exec(`DELETE FROM daily_participations WHERE chain_id = ?`, testChain).Error)
	require.NoError(t, db.Create(&rows).Error)
}

// countAgrega returns the number of rows in daily_participation_agregas for a chain.
func countAgrega(t *testing.T, db *gorm.DB, chainID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM daily_participation_agregas WHERE chain_id = ?`, chainID,
	).Scan(&n).Error)
	return n
}

// TestAggregateChain_Basic verifies totals for a single past day with two validators.
func TestAggregateChain_Basic(t *testing.T) {
	db := testoutils.NewTestDB(t)

	past := time.Now().UTC().AddDate(0, 0, -3) // 3 days ago — definitely a complete day
	day := time.Date(past.Year(), past.Month(), past.Day(), 12, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 100, Date: day, Participated: true, TxContribution: true, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 101, Date: day, Participated: false, TxContribution: false, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1bbb", BlockHeight: 100, Date: day, Participated: true, TxContribution: false, Moniker: "Bob"},
		{ChainID: testChain, Addr: "g1bbb", BlockHeight: 101, Date: day, Participated: true, TxContribution: true, Moniker: "Bob"},
	})

	require.NoError(t, gnovalidator.AggregateChain(db, testChain))

	// Two validators → two aggregate rows.
	require.Equal(t, int64(2), countAgrega(t, db, testChain))

	type row struct {
		Addr                string
		ParticipatedCount   int
		MissedCount         int
		TxContributionCount int
		TotalBlocks         int
		FirstBlockHeight    int64
		LastBlockHeight     int64
	}
	var results []row
	require.NoError(t, db.Raw(
		`SELECT addr, participated_count, missed_count, tx_contribution_count,
		        total_blocks, first_block_height, last_block_height
		 FROM daily_participation_agregas WHERE chain_id = ? ORDER BY addr`,
		testChain,
	).Scan(&results).Error)
	require.Len(t, results, 2)

	alice := results[0]
	require.Equal(t, "g1aaa", alice.Addr)
	require.Equal(t, 1, alice.ParticipatedCount)
	require.Equal(t, 1, alice.MissedCount)
	require.Equal(t, 1, alice.TxContributionCount)
	require.Equal(t, 2, alice.TotalBlocks)
	require.Equal(t, int64(100), alice.FirstBlockHeight)
	require.Equal(t, int64(101), alice.LastBlockHeight)

	bob := results[1]
	require.Equal(t, "g1bbb", bob.Addr)
	require.Equal(t, 2, bob.ParticipatedCount)
	require.Equal(t, 0, bob.MissedCount)
	require.Equal(t, 1, bob.TxContributionCount)
	require.Equal(t, 2, bob.TotalBlocks)
}

// TestAggregateChain_TodayExcluded verifies that today's rows are not aggregated
// (the day is incomplete).
func TestAggregateChain_TodayExcluded(t *testing.T) {
	db := testoutils.NewTestDB(t)

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 200, Date: today, Participated: true, Moniker: "Alice"},
	})

	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	require.Equal(t, int64(0), countAgrega(t, db, testChain))
}

// TestAggregateChain_Idempotent verifies that running aggregation twice produces
// the same result (ON CONFLICT DO UPDATE is safe to call multiple times).
func TestAggregateChain_Idempotent(t *testing.T) {
	db := testoutils.NewTestDB(t)

	past := time.Now().UTC().AddDate(0, 0, -5)
	day := time.Date(past.Year(), past.Month(), past.Day(), 8, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 300, Date: day, Participated: true, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 301, Date: day, Participated: false, Moniker: "Alice"},
	})

	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	require.NoError(t, gnovalidator.AggregateChain(db, testChain)) // second run

	require.Equal(t, int64(1), countAgrega(t, db, testChain))

	var total int
	require.NoError(t, db.Raw(
		`SELECT total_blocks FROM daily_participation_agregas WHERE chain_id = ? AND addr = ?`,
		testChain, "g1aaa",
	).Scan(&total).Error)
	require.Equal(t, 2, total)
}

// TestAggregateChain_MultipleDays verifies that each distinct past day gets its
// own aggregate row per validator.
func TestAggregateChain_MultipleDays(t *testing.T) {
	db := testoutils.NewTestDB(t)

	base := time.Now().UTC()
	day1 := base.AddDate(0, 0, -10).Truncate(24*time.Hour).Add(12 * time.Hour)
	day2 := base.AddDate(0, 0, -9).Truncate(24*time.Hour).Add(12 * time.Hour)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 400, Date: day1, Participated: true, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 500, Date: day2, Participated: false, Moniker: "Alice"},
	})

	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	// One row per (addr, day) → 2 rows total.
	require.Equal(t, int64(2), countAgrega(t, db, testChain))
}

// TestAggregateChain_NeverRevisitsAggregatedDay documents the gap
// ReaggregateDateRange exists to close: once a day has an agrega row,
// AggregateChain's "> lastDate" scan skips it forever, even if more raw rows
// for that exact day arrive afterward (e.g. a late BackfillParallel run).
func TestAggregateChain_NeverRevisitsAggregatedDay(t *testing.T) {
	db := testoutils.NewTestDB(t)

	past := time.Now().UTC().AddDate(0, 0, -4)
	day := time.Date(past.Year(), past.Month(), past.Day(), 12, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 600, Date: day, Participated: true, Moniker: "Alice"},
	})
	require.NoError(t, gnovalidator.AggregateChain(db, testChain))

	var total int
	require.NoError(t, db.Raw(
		`SELECT total_blocks FROM daily_participation_agregas WHERE chain_id = ? AND addr = ?`,
		testChain, "g1aaa",
	).Scan(&total).Error)
	require.Equal(t, 1, total, "one raw row aggregated so far")

	// A late backfill writes one more row for the SAME already-aggregated day.
	require.NoError(t, db.Create(&database.DailyParticipation{
		ChainID: testChain, Addr: "g1aaa", BlockHeight: 601, Date: day, Participated: true, Moniker: "Alice",
	}).Error)

	require.NoError(t, gnovalidator.AggregateChain(db, testChain))

	require.NoError(t, db.Raw(
		`SELECT total_blocks FROM daily_participation_agregas WHERE chain_id = ? AND addr = ?`,
		testChain, "g1aaa",
	).Scan(&total).Error)
	require.Equal(t, 1, total, "AggregateChain must NOT pick up the late row — it never revisits an already-aggregated day")
}

// TestReaggregateDateRange_PicksUpLateBackfilledDay verifies the fix: forcing
// a re-aggregation pass over the day backfill touched correctly folds in rows
// that arrived after that day was already aggregated.
func TestReaggregateDateRange_PicksUpLateBackfilledDay(t *testing.T) {
	db := testoutils.NewTestDB(t)

	past := time.Now().UTC().AddDate(0, 0, -4)
	day := time.Date(past.Year(), past.Month(), past.Day(), 12, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 600, Date: day, Participated: true, Moniker: "Alice"},
	})
	require.NoError(t, gnovalidator.AggregateChain(db, testChain))

	// Late backfill writes one more row for the same already-aggregated day.
	require.NoError(t, db.Create(&database.DailyParticipation{
		ChainID: testChain, Addr: "g1aaa", BlockHeight: 601, Date: day, Participated: true, Moniker: "Alice",
	}).Error)

	require.NoError(t, gnovalidator.ReaggregateDateRange(db, testChain, day, day))

	var total int
	require.NoError(t, db.Raw(
		`SELECT total_blocks FROM daily_participation_agregas WHERE chain_id = ? AND addr = ?`,
		testChain, "g1aaa",
	).Scan(&total).Error)
	require.Equal(t, 2, total, "ReaggregateDateRange must fold in the late-arriving row")
}

// TestReaggregateDateRange_ClampsToday verifies "to" is clamped to yesterday
// so an in-progress day is never frozen as final by a backfill that catches
// all the way up to the present.
func TestReaggregateDateRange_ClampsToday(t *testing.T) {
	db := testoutils.NewTestDB(t)

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 700, Date: today, Participated: true, Moniker: "Alice"},
	})

	require.NoError(t, gnovalidator.ReaggregateDateRange(db, testChain, today, today))
	require.Equal(t, int64(0), countAgrega(t, db, testChain), "today must never be force-aggregated, same rule as AggregateChain")
}

// TestAggregateChain_ProposedCount verifies that proposed blocks are summed
// into proposed_count for the day.
func TestAggregateChain_ProposedCount(t *testing.T) {
	db := testoutils.NewTestDB(t)

	past := time.Now().UTC().AddDate(0, 0, -1)
	day := time.Date(past.Year(), past.Month(), past.Day(), 12, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 10, Date: day, Participated: true, TxContribution: false, Proposed: true, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 11, Date: day, Participated: true, TxContribution: false, Proposed: false, Moniker: "Alice"},
	})

	require.NoError(t, gnovalidator.AggregateChain(db, testChain))

	var proposedCount int
	require.NoError(t, db.Raw(
		`SELECT proposed_count FROM daily_participation_agregas WHERE chain_id = ? AND addr = ?`,
		testChain, "g1aaa",
	).Scan(&proposedCount).Error)
	require.Equal(t, 1, proposedCount)
}

// TestPruneRawData verifies that rows older than 7 days are deleted and recent
// rows are preserved.
func TestPruneRawData(t *testing.T) {
	db := testoutils.NewTestDB(t)

	now := time.Now().UTC()
	old := time.Date(now.Year(), now.Month(), now.Day()-10, 12, 0, 0, 0, time.UTC)
	recent := time.Date(now.Year(), now.Month(), now.Day()-2, 12, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 600, Date: old, Participated: true, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 700, Date: recent, Participated: true, Moniker: "Alice"},
	})

	require.NoError(t, gnovalidator.PruneRawData(db, testChain))

	var remaining int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM daily_participations WHERE chain_id = ?`, testChain,
	).Scan(&remaining).Error)

	// Only the recent row should survive (plus the 3 rows from FakeData which
	// are in the current month so they are within 7 days for day1/day2/day3 of
	// this month — however if today is day 1-7 of the month some seed rows
	// could also be old; we only assert that the explicitly-old row is gone).
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM daily_participations WHERE chain_id = ? AND date = ?`,
		testChain, old,
	).Scan(&remaining).Error)
	require.Equal(t, int64(0), remaining, "old row should have been pruned")

	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM daily_participations WHERE chain_id = ? AND date = ?`,
		testChain, recent,
	).Scan(&remaining).Error)
	require.Equal(t, int64(1), remaining, "recent row should be kept")
}
