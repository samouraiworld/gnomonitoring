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

// legacyAggregate runs the legacy queries over every unaggregated day, or only
// the first maxDays of them when maxDays > 0, and returns the days it saw.
func legacyAggregate(t *testing.T, db *gorm.DB, maxDays int) []string {
	t.Helper()
	var days []string
	require.NoError(t, db.Raw(legacyDaysQuery, testChain, "0001-01-01").Scan(&days).Error)
	if maxDays > 0 && len(days) > maxDays {
		days = days[:maxDays]
	}
	for _, d := range days {
		require.NoError(t, db.Exec(legacyAggregateDayQuery, testChain, d).Error)
	}
	return days
}

// requireMatchesLegacy seeds rows on and around day boundaries and requires
// AggregateChain to produce exactly the rows the legacy queries produce, both
// from scratch and when continuing from a watermark the legacy code wrote.
func requireMatchesLegacy(t *testing.T, db *gorm.DB, rows []database.DailyParticipation) {
	t.Helper()
	require.NoError(t, db.Exec(`DELETE FROM daily_participations`).Error)
	require.NoError(t, db.Create(rows).Error)

	days := legacyAggregate(t, db, 0)
	require.GreaterOrEqual(t, len(days), 2, "legacy oracle should see several complete days")
	want := agregaSnapshot(t, db, testChain)
	require.NotEmpty(t, want)

	// From scratch.
	require.NoError(t, db.Exec(`DELETE FROM daily_participation_agregas`).Error)
	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	require.Equal(t, want, agregaSnapshot(t, db, testChain))
	require.Empty(t, agregaSnapshot(t, db, "otherchain"), "other chain must not be aggregated")

	// A second run starts from the watermark (yesterday) and must not touch today.
	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	require.Equal(t, want, agregaSnapshot(t, db, testChain))

	// Upgrade continuity: the legacy code aggregated the first day, the new
	// code takes over from that watermark.
	require.NoError(t, db.Exec(`DELETE FROM daily_participation_agregas`).Error)
	legacyAggregate(t, db, 1)
	require.NoError(t, gnovalidator.AggregateChain(db, testChain))
	require.Equal(t, want, agregaSnapshot(t, db, testChain))
}

// TestAggregateChain_MatchesLegacyDayCasts proves the range-based predicates
// aggregate the same days and produce the same rows as the old date::date
// casts, with rows sitting exactly on UTC day boundaries, under the UTC
// session InitDB sets.
func TestAggregateChain_MatchesLegacyDayCasts(t *testing.T) {
	db := testoutils.NewTestDB(t)
	requireMatchesLegacy(t, db, boundaryRows())
	require.Len(t, agregaSnapshot(t, db, testChain), 5, "4 complete days, two validators on one of them")
}

// TestAggregateChain_MatchesLegacyUnderSessionTimeZones repeats the comparison
// with a session TimeZone that a DSN can still set explicitly (InitDB only adds
// TimeZone=UTC when none is given): days are then local to that zone, exactly
// as the date::date casts bucketed them.
func TestAggregateChain_MatchesLegacyUnderSessionTimeZones(t *testing.T) {
	for _, zone := range []string{"Pacific/Kiritimati", "America/Sao_Paulo", "Europe/Paris"} {
		t.Run(zone, func(t *testing.T) {
			db := testoutils.NewTestDB(t)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1) // keep the SET on the one connection every query uses
			require.NoError(t, db.Exec(`SET TIME ZONE '`+zone+`'`).Error)
			requireMatchesLegacy(t, db, boundaryRows())
		})
	}
}

// TestAggregateChain_MatchesLegacyOnAmbiguousMidnight covers a DST fall-back
// at local midnight: in America/Havana on 2018-11-04 the clocks went from
// 01:00 -04 back to 00:00 -05, so 00:30 happened twice and "2018-11-04"
// resolves to 00:00 -05, after the first local half hour of that day.
func TestAggregateChain_MatchesLegacyOnAmbiguousMidnight(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.Exec(`SET TIME ZONE 'America/Havana'`).Error)

	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return v
	}
	rows := []database.DailyParticipation{
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 1, Date: at("2018-11-03T23:30:00-04:00"), Participated: true},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 2, Date: at("2018-11-04T00:30:00-04:00"), Participated: true},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 3, Date: at("2018-11-04T00:30:00-05:00"), Participated: false},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 4, Date: at("2018-11-04T23:30:00-05:00"), Participated: true},
		{ChainID: testChain, Addr: "g1aaa", BlockHeight: 5, Date: at("2018-11-05T00:10:00-05:00"), Participated: true},
	}
	requireMatchesLegacy(t, db, rows)

	got := agregaSnapshot(t, db, testChain)
	require.Len(t, got, 3)
	require.Equal(t, "2018-11-04", got[1].BlockDate)
	require.Equal(t, int64(2), got[1].FirstBlockHeight, "first local hour belongs to 2018-11-04")
	require.Equal(t, 3, got[1].TotalBlocks)
}
