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

// GetAlertLog backs /latest_incidents and returns at most 10 rows for the WHOLE
// chain. A client showing one validator's incident history — a validator profile
// page — therefore sees few or none of that validator's incidents once any other
// validator has been noisy. These pin the filtered query, and pin that the
// unfiltered one keeps its existing behaviour for current consumers.

// seedAlerts inserts n distinct WARNING rows for addr with increasing end heights.
func seedAlerts(t *testing.T, db *gorm.DB, chain, addr string, n int, firstEnd int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		end := firstEnd + int64(i)
		require.NoError(t, database.InsertAlertlog(db, chain, addr, "mon-"+addr, "WARNING", end-5, end, true, time.Now(), ""))
	}
}

func TestGetAlertLogFiltered_ReturnsOnlyThatValidator(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-filter-addr"
	seedAlerts(t, db, chain, "g1target", 12, 100)
	seedAlerts(t, db, chain, "g1noisy", 15, 1000) // higher heights: would crowd g1target out of a chain-wide top 10

	alerts, err := database.GetAlertLogFiltered(db, chain, "all_time", database.AlertLogQuery{Addr: "g1target", Limit: 50})
	require.NoError(t, err)
	require.Len(t, alerts, 12, "every g1target incident, not a chain-wide top 10 it does not appear in")
	for _, a := range alerts {
		assert.Equal(t, "g1target", a.Addr)
	}
	assert.EqualValues(t, 111, alerts[0].EndHeight, "most recent first")
}

func TestGetAlertLogFiltered_LimitCapsRows(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-filter-limit"
	seedAlerts(t, db, chain, "g1target", 12, 100)

	alerts, err := database.GetAlertLogFiltered(db, chain, "all_time", database.AlertLogQuery{Addr: "g1target", Limit: 5})
	require.NoError(t, err)
	require.Len(t, alerts, 5)
	assert.EqualValues(t, 111, alerts[0].EndHeight)
	assert.EqualValues(t, 107, alerts[4].EndHeight)
}

func TestGetAlertLogFiltered_AddrIsChainScoped(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedAlerts(t, db, "chain-a", "g1target", 3, 100)
	seedAlerts(t, db, "chain-b", "g1target", 4, 100)

	alerts, err := database.GetAlertLogFiltered(db, "chain-a", "all_time", database.AlertLogQuery{Addr: "g1target", Limit: 50})
	require.NoError(t, err)
	assert.Len(t, alerts, 3, "the same address on another chain must not leak in")
}

func TestGetAlertLogFiltered_ZeroValueQueryKeepsTheDefault(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-filter-default"
	seedAlerts(t, db, chain, "g1a", 8, 100)
	seedAlerts(t, db, chain, "g1b", 8, 200)

	filtered, err := database.GetAlertLogFiltered(db, chain, "all_time", database.AlertLogQuery{})
	require.NoError(t, err)
	assert.Len(t, filtered, database.DefaultAlertLogLimit, "no addr and no limit is the existing chain-wide top rows")

	unfiltered, err := database.GetAlertLog(db, chain, "all_time")
	require.NoError(t, err)
	assert.Len(t, unfiltered, 10, "GetAlertLog, and so /latest_incidents without new params, is unchanged")
	assert.Equal(t, filtered, unfiltered)
}

// Rows sharing an end_height are the common case for a per-validator history: a
// RESOLVED row repeats the heights of the alert it closes, and every RPC row has
// end_height 0. Ordering on end_height alone left their relative order undefined,
// so LIMIT could keep an older row and drop a newer one (#137).
func TestGetAlertLogFiltered_EndHeightTiesKeepNewestRows(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-filter-ties"
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	// Insert the newest rows first so that physical order disagrees with sent_at.
	for i := 11; i >= 0; i-- {
		level := "WARNING"
		if i%2 == 1 {
			level = "RESOLVED"
		}
		require.NoError(t, database.InsertAlertlog(db, chain, "g1tied", "mon", level, 495, 500, true, base.Add(time.Duration(i)*time.Minute), ""))
	}
	// A row with a higher end_height still comes first: end_height stays the primary key.
	require.NoError(t, database.InsertAlertlog(db, chain, "g1tied", "mon", "CRITICAL", 495, 501, true, base, ""))

	q := database.AlertLogQuery{Addr: "g1tied", Limit: 5}
	alerts, err := database.GetAlertLogFiltered(db, chain, "all_time", q)
	require.NoError(t, err)
	require.Len(t, alerts, 5)
	assert.EqualValues(t, 501, alerts[0].EndHeight)
	for i, a := range alerts[1:] {
		want := base.Add(time.Duration(11-i) * time.Minute)
		assert.True(t, a.SentAt.Equal(want), "row %d: sentAt %v, want %v (newest tied rows, newest first)", i+1, a.SentAt, want)
	}

	again, err := database.GetAlertLogFiltered(db, chain, "all_time", q)
	require.NoError(t, err)
	assert.Equal(t, alerts, again, "two identical requests return the same rows in the same order")
}

// Period bounds are UTC and half-open (#139): a row sent exactly at the first
// instant of the next period belongs to that period only, and the week starts on
// Monday 00:00 UTC whatever the process's local timezone is.
func TestGetAlertLogFiltered_PeriodBoundsAreUTCAndHalfOpen(t *testing.T) {
	prevLocal := time.Local
	time.Local = time.FixedZone("UTC-5", -5*3600)
	t.Cleanup(func() { time.Local = prevLocal })

	db := testoutils.NewTestDB(t)
	const chain = "chain-filter-bounds"
	now := time.Now().UTC()

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonth := monthStart.AddDate(0, 1, 0)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	weekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -weekday+1)

	insert := func(addr string, sent time.Time) {
		require.NoError(t, database.InsertAlertlog(db, chain, addr, "mon", "WARNING", 1, 2, true, sent, ""))
	}
	insert("g1monthstart", monthStart)
	insert("g1nextmonth", nextMonth)
	insert("g1weekstart", weekStart)
	insert("g1beforeweek", weekStart.Add(-time.Second))
	insert("g1nextweek", weekStart.AddDate(0, 0, 7))

	addrs := func(period string) []string {
		alerts, err := database.GetAlertLogFiltered(db, chain, period, database.AlertLogQuery{Limit: 50})
		require.NoError(t, err)
		out := make([]string, 0, len(alerts))
		for _, a := range alerts {
			out = append(out, a.Addr)
		}
		return out
	}

	month := addrs("current_month")
	assert.Contains(t, month, "g1monthstart")
	assert.NotContains(t, month, "g1nextmonth", "the first instant of next month is not in current_month")

	week := addrs("current_week")
	assert.Contains(t, week, "g1weekstart", "the week starts on Monday 00:00 UTC, not local midnight")
	assert.NotContains(t, week, "g1beforeweek")
	assert.NotContains(t, week, "g1nextweek", "the first instant of next week is not in current_week")

	assert.Len(t, addrs("all_time"), 5)
}

func TestGetAlertLogFiltered_AllTimeOnEmptyChain(t *testing.T) {
	db := testoutils.NewTestDB(t)
	alerts, err := database.GetAlertLogFiltered(db, "chain-filter-empty", "all_time", database.AlertLogQuery{})
	require.NoError(t, err)
	assert.Empty(t, alerts)
}

func TestGetAlertLogFiltered_RejectsUnknownPeriod(t *testing.T) {
	db := testoutils.NewTestDB(t)
	for _, period := range []string{"last_24h", "bogus", ""} {
		_, err := database.GetAlertLogFiltered(db, "chain-filter-period", period, database.AlertLogQuery{})
		assert.Error(t, err, "period %q", period)
	}
}
