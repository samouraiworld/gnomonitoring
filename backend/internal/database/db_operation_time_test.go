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
// validator never missed a block, and never negative.
func TestOperationTimeMetricsaddr(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainA, chainB = "test-optime-a", "test-optime-b"
	loc := sessionLocation(t, db)

	// Aggregated days 2026-03-01..03 put the watermark at 2026-03-04; raw rows
	// sit on 2026-03-05, a day not aggregated yet.
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
		// Same address on chain B, never down there.
		agrega(chainB, "g1down", "2026-03-01", 10, 0),
		agrega(chainB, "g1onlyb", "2026-03-01", 3, 7),
	}).Error)

	at := func(hour, minute int) time.Time { return time.Date(2026, 3, 5, hour, minute, 0, 0, time.UTC) }
	var height int64
	raw := func(chain, addr string, date time.Time, participated bool) database.DailyParticipation {
		height++
		return database.DailyParticipation{
			ChainID: chain, Addr: addr, Moniker: addr, BlockHeight: height,
			Date: date, Participated: participated,
		}
	}
	require.NoError(t, db.Create([]database.DailyParticipation{
		raw(chainA, "g1down", at(10, 0), true),
		raw(chainA, "g1down", at(10, 1), false),
		raw(chainA, "g1down", at(10, 2), false),
		raw(chainA, "g1recovered", at(6, 0), false),
		raw(chainA, "g1recovered", at(18, 0), true),
		raw(chainA, "g1neverdown", at(12, 0), true),
		raw(chainB, "g1down", at(12, 0), true),
	}).Error)

	day := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, loc) }

	a := operationTimeByAddr(t, db, chainA)
	require.Len(t, a, 5, "chain A must list its own five validators only")

	// Latest block is a miss: down now, so 0, not negative.
	assert.Equal(t, 0.0, a["g1down"].DaysDiff)
	assert.NotEmpty(t, a["g1down"].LastDownDate)
	// Down on its last aggregated day, with no later row: still 0.
	assert.Equal(t, 0.0, a["g1downagg"].DaysDiff)

	// Missed at 06:00 today, signed until 18:00: exact timestamps, half a day.
	assert.Equal(t, 0.5, a["g1recovered"].DaysDiff)
	// Missed on 03-01, seen until the end of 03-03: day bounds, two days.
	assert.Equal(t, daysBetween(day(2), day(4)), a["g1recoveredagg"].DaysDiff)

	// Never missed a block: measured from first seen (start of 03-01).
	assert.Equal(t, daysBetween(day(1), at(12, 0)), a["g1neverdown"].DaysDiff)
	assert.Greater(t, a["g1neverdown"].DaysDiff, 4.0)
	assert.Empty(t, a["g1neverdown"].LastDownDate, "a never-down validator has no last down")

	for addr, r := range a {
		assert.GreaterOrEqual(t, r.DaysDiff, 0.0, "%s must never be negative", addr)
	}

	// Chain B: the same address is not down there, and chain A rows do not leak.
	b := operationTimeByAddr(t, db, chainB)
	require.Len(t, b, 2, "chain B must list its own two validators only")
	assert.Equal(t, daysBetween(day(1), at(12, 0)), b["g1down"].DaysDiff)
	assert.Equal(t, 0.0, b["g1onlyb"].DaysDiff)
}
