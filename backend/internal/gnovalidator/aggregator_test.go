package gnovalidator_test

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
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

	now := time.Now().UTC()
	day1 := time.Date(now.Year(), now.Month(), now.Day()-10, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(now.Year(), now.Month(), now.Day()-9, 12, 0, 0, 0, time.UTC)

	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 400, Date: day1, Participated: true, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 500, Date: day2, Participated: false, Moniker: "Alice"},
	})

	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	// One row per (addr, day) → 2 rows total.
	require.Equal(t, int64(2), countAgrega(t, db, testChain))
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
