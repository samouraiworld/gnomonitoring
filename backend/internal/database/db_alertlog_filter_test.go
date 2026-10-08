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
