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

// legacyDaysQuery and legacyAggregateDayQuery are the aggregator queries as they
// were before the day predicates became half-open timestamp ranges. They are
// kept here only as an oracle: the current AggregateChain must aggregate exactly
// the same days and produce exactly the same rows.
const legacyDaysQuery = `
	SELECT DISTINCT date::date AS d
	FROM daily_participations
	WHERE chain_id = ? AND date::date > ? AND date::date < CURRENT_DATE
	ORDER BY d ASC`

const legacyAggregateDayQuery = `
	INSERT INTO daily_participation_agregas
	  (chain_id, addr, block_date, moniker,
	   participated_count, missed_count, tx_contribution_count, proposed_count,
	   total_blocks, first_block_height, last_block_height,
	   late_for_quorum_count, late_for_quorum_samples,
	   precommit_lag_p50_ms, precommit_lag_p90_ms)
	SELECT
	  chain_id,
	  addr,
	  date::date                                            AS block_date,
	  MAX(moniker)                                          AS moniker,
	  SUM(CASE WHEN participated     THEN 1 ELSE 0 END)    AS participated_count,
	  SUM(CASE WHEN NOT participated THEN 1 ELSE 0 END)    AS missed_count,
	  SUM(CASE WHEN tx_contribution  THEN 1 ELSE 0 END)    AS tx_contribution_count,
	  SUM(CASE WHEN proposed         THEN 1 ELSE 0 END)    AS proposed_count,
	  COUNT(*)                                             AS total_blocks,
	  MIN(block_height)                                    AS first_block_height,
	  MAX(block_height)                                    AS last_block_height,
	  SUM(CASE WHEN late_for_quorum  THEN 1 ELSE 0 END)    AS late_for_quorum_count,
	  COUNT(late_for_quorum)                               AS late_for_quorum_samples,
	  percentile_cont(0.5) WITHIN GROUP (ORDER BY precommit_lag_ms) AS precommit_lag_p50_ms,
	  percentile_cont(0.9) WITHIN GROUP (ORDER BY precommit_lag_ms) AS precommit_lag_p90_ms
	FROM daily_participations
	WHERE chain_id = ? AND date::date = ?
	GROUP BY chain_id, addr, date::date`

func utcMidnight(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func agregaSnapshot(t *testing.T, db *gorm.DB, chainID string) []database.DailyParticipationAgrega {
	t.Helper()
	var rows []database.DailyParticipationAgrega
	require.NoError(t, db.Where("chain_id = ?", chainID).Order("block_date, addr").Find(&rows).Error)
	return rows
}

// boundaryRows seeds rows on and around UTC day boundaries, up to and including
// today's first instant (which must never be aggregated).
func boundaryRows() []database.DailyParticipation {
	today := utcMidnight(time.Now())
	base := today.AddDate(0, 0, -3)
	lastMicro := 24*time.Hour - time.Microsecond
	lag := func(v int64) *int64 { return &v }
	late := func(v bool) *bool { return &v }

	return []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 10, Date: base.AddDate(0, 0, -1).Add(lastMicro), Participated: true, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 11, Date: base, Participated: false, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1bbb", BlockHeight: 12, Date: base.Add(12 * time.Hour), Participated: true, Proposed: true, Moniker: "Bob", PrecommitLagMs: lag(40), LateForQuorum: late(false)},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 13, Date: base.Add(lastMicro), Participated: true, TxContribution: true, Moniker: "Alice", PrecommitLagMs: lag(120), LateForQuorum: late(true)},
		{ChainID: testChain, Addr: "g1bbb", BlockHeight: 13, Date: base.Add(lastMicro), Participated: true, Moniker: "Bob", PrecommitLagMs: lag(60), LateForQuorum: late(false)},
		{ChainID: testChain, Addr: "g1bbb", BlockHeight: 14, Date: base.AddDate(0, 0, 1), Participated: false, Moniker: "Bob"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 15, Date: today.Add(-time.Microsecond), Participated: true, Moniker: "Alice"},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 16, Date: today, Participated: true, Moniker: "Alice"},
		{ChainID: "otherchain", Addr: "g1zzz", BlockHeight: 12, Date: base.Add(time.Hour), Participated: true, Moniker: "Zed"},
	}
}

// TestAggregateChain_MatchesLegacyDayCasts proves the range-based predicates
// aggregate the same days and produce the same rows as the old date::date casts,
// with rows sitting exactly on UTC day boundaries.
func TestAggregateChain_MatchesLegacyDayCasts(t *testing.T) {
	db := testoutils.NewTestDB(t)
	require.NoError(t, db.Exec(`DELETE FROM daily_participations`).Error)
	require.NoError(t, db.Create(boundaryRows()).Error)

	var days []string
	require.NoError(t, db.Raw(legacyDaysQuery, testChain, "0001-01-01").Scan(&days).Error)
	require.Len(t, days, 4, "legacy oracle should see 4 complete days")
	for _, d := range days {
		require.NoError(t, db.Exec(legacyAggregateDayQuery, testChain, d).Error)
	}
	want := agregaSnapshot(t, db, testChain)
	require.NotEmpty(t, want)

	require.NoError(t, db.Exec(`DELETE FROM daily_participation_agregas`).Error)
	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	require.Equal(t, want, agregaSnapshot(t, db, testChain))
	require.Empty(t, agregaSnapshot(t, db, "otherchain"), "other chain must not be aggregated")

	// A second run starts from the watermark (yesterday) and must not touch today.
	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	require.Equal(t, want, agregaSnapshot(t, db, testChain))
}

// TestAggregateChain_UTCDaysIndependentOfSessionTimeZone pins the aggregator to
// UTC calendar days even when the session TimeZone is not UTC. InitDB forces
// TimeZone=UTC, so production semantics are unchanged; this guards against a
// DSN that overrides it.
func TestAggregateChain_UTCDaysIndependentOfSessionTimeZone(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)                                                // keep the SET on the one connection every query uses
	require.NoError(t, db.Exec(`SET TIME ZONE 'Pacific/Kiritimati'`).Error) // UTC+14

	today := utcMidnight(time.Now())
	yesterday := today.AddDate(0, 0, -1)
	base := today.AddDate(0, 0, -3)
	seedRaw(t, db, []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 1, Date: base.Add(23 * time.Hour), Participated: true},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 2, Date: yesterday.Add(11 * time.Hour), Participated: true},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 3, Date: today.Add(time.Minute), Participated: true},
	})

	require.NoError(t, gnovalidator.AggregateChain(db, testChain))

	got := agregaSnapshot(t, db, testChain)
	require.Len(t, got, 2)
	require.Equal(t, base.Format("2006-01-02"), got[0].BlockDate)
	require.Equal(t, int64(1), got[0].FirstBlockHeight)
	require.Equal(t, yesterday.Format("2006-01-02"), got[1].BlockDate)
	require.Equal(t, int64(2), got[1].FirstBlockHeight)
}
