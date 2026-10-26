package database_test

import (
	"math"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// sessionLocation returns the Postgres session TimeZone, which sets the day
// bounds of agrega rows (see OperationTimeMetricsaddr).
func sessionLocation(t *testing.T, db *gorm.DB) *time.Location {
	t.Helper()
	var tz string
	require.NoError(t, db.Raw(`SELECT current_setting('TimeZone')`).Scan(&tz).Error)
	loc, err := time.LoadLocation(tz)
	require.NoError(t, err)
	return loc
}

func operationTimeByAddr(t *testing.T, db *gorm.DB, chain string) map[string]database.OperationTimeMetrics {
	t.Helper()
	aggregatedThrough, err := database.GetAggregatedThrough(db, chain)
	require.NoError(t, err)
	rows, err := database.OperationTimeMetricsaddr(db, chain, aggregatedThrough)
	require.NoError(t, err)
	byAddr := make(map[string]database.OperationTimeMetrics, len(rows))
	for _, r := range rows {
		byAddr[r.Addr] = r
	}
	return byAddr
}

func daysBetween(from, to time.Time) float64 {
	return math.Round(to.Sub(from).Hours()/24*10) / 10
}

// TestOperationTimeMetricsaddr covers issue #159: the value is last_seen -
// last_down in days, 0 while down, measured from first seen when the
// validator never missed a block, never negative, and validators that left
// the valset are dropped.
func TestOperationTimeMetricsaddr(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainA, chainB = "test-optime-a", "test-optime-b"
	loc := sessionLocation(t, db)

	// Chain A: aggregated days 2026-03-01..03 put the watermark at 2026-03-04;
	// raw rows from 2026-03-04 on are not aggregated yet.
	agrega := func(chain, addr, day string, participated, missed int) database.DailyParticipationAgrega {
		return database.DailyParticipationAgrega{
			ChainID: chain, Addr: addr, Moniker: addr, BlockDate: day,
			ParticipatedCount: participated, MissedCount: missed, TotalBlocks: participated + missed,
		}
	}
	require.NoError(t, db.Create([]database.DailyParticipationAgrega{
		agrega(chainA, "g1down", "2026-03-01", 10, 0),
		agrega(chainA, "g1recovered", "2026-03-01", 5, 5),
		agrega(chainA, "g1recovered", "2026-03-02", 10, 0),
		agrega(chainA, "g1recoveredagg", "2026-03-01", 5, 5),
		agrega(chainA, "g1recoveredagg", "2026-03-03", 10, 0),
		agrega(chainA, "g1downagg", "2026-03-02", 10, 0),
		agrega(chainA, "g1downagg", "2026-03-03", 3, 7),
		agrega(chainA, "g1neverdown", "2026-03-01", 10, 0),
		// Last seen at the end of 03-02, more than a day before the chain's
		// latest block: it left the valset.
		agrega(chainA, "g1departed", "2026-03-02", 10, 0),
		// Same address on chain B, never down there.
		agrega(chainB, "g1down", "2026-03-01", 10, 0),
		agrega(chainB, "g1onlyb", "2026-03-01", 3, 7),
	}).Error)

	at := func(d, hour, minute int) time.Time { return time.Date(2026, 3, d, hour, minute, 0, 0, time.UTC) }
	var height int64
	raw := func(chain, addr string, date time.Time, participated bool) database.DailyParticipation {
		height++
		return database.DailyParticipation{
			ChainID: chain, Addr: addr, Moniker: addr, BlockHeight: height,
			Date: date, Participated: participated,
		}
	}
	require.NoError(t, db.Create([]database.DailyParticipation{
		raw(chainA, "g1down", at(4, 10, 0), true),
		raw(chainA, "g1down", at(4, 10, 1), false),
		raw(chainA, "g1down", at(4, 10, 2), false),
		raw(chainA, "g1recovered", at(4, 6, 0), false),
		raw(chainA, "g1recovered", at(4, 18, 0), true),
		raw(chainA, "g1neverdown", at(4, 12, 0), true),
		// A raw miss on an aggregated day: the agrega row (0 missed) wins.
		raw(chainA, "g1recoveredagg", at(3, 1, 0), false),
		// A raw miss before the watermark on a day never aggregated: outside
		// the raw-fallback window.
		raw(chainA, "g1neverdown", time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC), false),
		raw(chainB, "g1down", at(4, 12, 0), true),
		raw(chainB, "g1onlyb", at(4, 11, 0), false),
	}).Error)

	day := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, loc) }
	rfc := func(ts time.Time) string { return ts.UTC().Format(time.RFC3339) }

	a := operationTimeByAddr(t, db, chainA)
	require.Len(t, a, 5, "chain A must list its five current validators only")
	assert.NotContains(t, a, "g1departed", "a validator that left the valset must be dropped")

	// Latest block is a miss: down now, so 0, not negative.
	assert.Equal(t, 0.0, a["g1down"].DaysDiff)
	assert.Equal(t, rfc(at(4, 10, 2)), a["g1down"].LastDownDate)
	assert.Equal(t, rfc(at(4, 10, 2)), a["g1down"].LastUpDate)
	// Down on its last aggregated day, with no later row: still 0.
	assert.Equal(t, 0.0, a["g1downagg"].DaysDiff)
	assert.Equal(t, rfc(day(4)), a["g1downagg"].LastDownDate)

	// Missed at 06:00, signed until 18:00: exact timestamps, half a day.
	assert.Equal(t, 0.5, a["g1recovered"].DaysDiff)
	assert.Equal(t, rfc(at(4, 6, 0)), a["g1recovered"].LastDownDate)
	assert.Equal(t, rfc(at(4, 18, 0)), a["g1recovered"].LastUpDate)
	// Missed on 03-01, seen until the end of 03-03: day bounds, two days.
	assert.Equal(t, daysBetween(day(2), day(4)), a["g1recoveredagg"].DaysDiff)
	assert.Equal(t, rfc(day(2)), a["g1recoveredagg"].LastDownDate)
	assert.Equal(t, rfc(day(4)), a["g1recoveredagg"].LastUpDate)

	// Never missed a block: measured from first seen (start of 03-01).
	assert.Equal(t, daysBetween(day(1), at(4, 12, 0)), a["g1neverdown"].DaysDiff)
	assert.Greater(t, a["g1neverdown"].DaysDiff, 3.0)
	assert.Empty(t, a["g1neverdown"].LastDownDate, "a never-down validator has no last down")

	for addr, r := range a {
		assert.GreaterOrEqual(t, r.DaysDiff, 0.0, "%s must never be negative", addr)
	}

	// With no watermark the raw window is unbounded, so only the anti-join
	// keeps the raw miss on aggregated day 03-03 out.
	unbounded, err := database.OperationTimeMetricsaddr(db, chainA, time.Time{})
	require.NoError(t, err)
	var recoveredAgg *database.OperationTimeMetrics
	for i := range unbounded {
		if unbounded[i].Addr == "g1recoveredagg" {
			recoveredAgg = &unbounded[i]
		}
	}
	require.NotNil(t, recoveredAgg)
	assert.Equal(t, a["g1recoveredagg"], *recoveredAgg, "raw rows of an aggregated day must not override the agrega row")

	// Chain B: the same address is not down there, and chain A rows do not leak.
	b := operationTimeByAddr(t, db, chainB)
	require.Len(t, b, 2, "chain B must list its own two validators only")
	assert.Equal(t, daysBetween(day(1), at(4, 12, 0)), b["g1down"].DaysDiff)
	assert.Empty(t, b["g1down"].LastDownDate)
	assert.Equal(t, 0.0, b["g1onlyb"].DaysDiff)
}
