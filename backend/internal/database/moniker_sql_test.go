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

// resolveMoniker evaluates the shared MonikerSQL expression for one
// (chain, addr) pair, the way every moniker-returning query embeds it.
func resolveMoniker(t *testing.T, db *gorm.DB, chain, addr string, fallback database.MonikerFallback) string {
	t.Helper()
	var moniker string
	q := `SELECT ` + database.MonikerSQL("v.chain_id", "v.addr", fallback) + `
		FROM (VALUES (?::text, ?::text)) AS v(chain_id, addr)`
	require.NoError(t, db.Raw(q, chain, addr).Scan(&moniker).Error)
	return moniker
}

func seedFrozenParticipation(t *testing.T, db *gorm.DB, chain, addr, moniker string, height int64) {
	t.Helper()
	require.NoError(t, db.Create(&database.DailyParticipation{
		ChainID: chain, Addr: addr, Moniker: moniker, BlockHeight: height,
		Date: time.Now().UTC(), Participated: true,
	}).Error)
}

func seedFrozenAgrega(t *testing.T, db *gorm.DB, chain, addr, moniker, day string) {
	t.Helper()
	require.NoError(t, db.Create(&database.DailyParticipationAgrega{
		ChainID: chain, Addr: addr, BlockDate: day, Moniker: moniker,
		TotalBlocks: 1, ParticipatedCount: 1,
	}).Error)
}

// #110 5a: the frozen fallback must be the most recent name, not the
// lexically largest one MAX(moniker) used to return.
func TestMonikerSQL_FallbackPicksMostRecentFrozenName(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-moniker-recent"

	seedFrozenParticipation(t, db, chain, "g1raw", "zeta-old", 10)
	seedFrozenParticipation(t, db, chain, "g1raw", "alpha-new", 20)
	assert.Equal(t, "alpha-new", resolveMoniker(t, db, chain, "g1raw", database.MonikerFallbackEmpty))

	// Raw rows are pruned after the retention window: the aggregated days
	// carry the name, and the latest named day wins there too.
	seedFrozenAgrega(t, db, chain, "g1agg", "zeta-old", "2026-01-01")
	seedFrozenAgrega(t, db, chain, "g1agg", "alpha-new", "2026-01-02")
	assert.Equal(t, "alpha-new", resolveMoniker(t, db, chain, "g1agg", database.MonikerFallbackEmpty))
}

func TestMonikerSQL_AddrMonikersWinsOverFrozenName(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-moniker-am"

	seedFrozenParticipation(t, db, chain, "g1val", "frozen-name", 10)
	seedFrozenAgrega(t, db, chain, "g1val", "frozen-name", "2026-01-01")
	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1val", "current-name"))
	// Same addr renamed on another chain: must not leak across chains.
	require.NoError(t, database.UpsertAddrMoniker(db, "other-chain", "g1val", "other-chain-name"))

	assert.Equal(t, "current-name", resolveMoniker(t, db, chain, "g1val", database.MonikerFallbackEmpty))
}

func TestMonikerSQL_UnknownIsIgnored(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-moniker-unknown"

	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1val", "unknown"))
	seedFrozenParticipation(t, db, chain, "g1val", "unknown", 20)
	seedFrozenAgrega(t, db, chain, "g1val", "unknown", "2026-01-03")
	seedFrozenAgrega(t, db, chain, "g1val", "named", "2026-01-02")
	assert.Equal(t, "named", resolveMoniker(t, db, chain, "g1val", database.MonikerFallbackEmpty))

	// Nothing but "unknown" and empty names: the per-site last resort applies.
	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1none", "unknown"))
	seedFrozenParticipation(t, db, chain, "g1none", "", 20)
	assert.Equal(t, "", resolveMoniker(t, db, chain, "g1none", database.MonikerFallbackEmpty))
	assert.Equal(t, "g1none", resolveMoniker(t, db, chain, "g1none", database.MonikerFallbackAddr))
}

// #110 5a end to end, through one of the two queries the issue names.
func TestGetMissedBlocksLast24h_UsesMostRecentFrozenName(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-moniker-missed24h"

	for h, name := range map[int64]string{10: "zeta-old", 20: "alpha-new"} {
		require.NoError(t, db.Create(&database.DailyParticipation{
			ChainID: chain, Addr: "g1val", Moniker: name, BlockHeight: h,
			Date: time.Now().UTC(), Participated: false,
		}).Error)
	}

	rows, err := database.GetMissedBlocksLast24h(db, chain)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "alpha-new", rows[0].Moniker)
	assert.Equal(t, 2, rows[0].Missed)
}

// Step 4: an address that only exists in alert_logs (the chain-wide 'all' and
// 'rpc' rows) takes its latest named alert row, and latency rows, which
// record the address itself when the name was unresolved, never count.
func TestMonikerSQL_AlertLogsStep(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-moniker-alertlogs"
	now := time.Now()

	require.NoError(t, database.InsertAlertlog(db, chain, "rpc", "rpc-old", "CRITICAL", 0, 0, false, now.Add(-time.Hour), ""))
	require.NoError(t, database.InsertAlertlog(db, chain, "rpc", "rpc", "RESOLVED", 0, 1, false, now, ""))
	assert.Equal(t, "rpc", resolveMoniker(t, db, chain, "rpc", database.MonikerFallbackEmpty))

	require.NoError(t, database.InsertAlertlog(db, chain, "g1lat", "g1lat", "LATENCY_RESOLVED", 0, 0, false, now, ""))
	assert.Equal(t, "", resolveMoniker(t, db, chain, "g1lat", database.MonikerFallbackEmpty))
}

// Every frozen source is read for the enclosing row's chain only: names
// recorded for the same address on another chain never leak.
func TestMonikerSQL_FrozenSourcesAreChainScoped(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain, other = "chain-moniker-scoped", "chain-moniker-other"

	seedFrozenParticipation(t, db, other, "g1val", "other-raw", 10)
	seedFrozenAgrega(t, db, other, "g1val", "other-agg", "2026-01-01")
	require.NoError(t, database.InsertAlertlog(db, other, "g1val", "other-alert", "WARNING", 1, 5, true, time.Now(), ""))

	assert.Equal(t, "", resolveMoniker(t, db, chain, "g1val", database.MonikerFallbackEmpty))
	assert.Equal(t, "g1val", resolveMoniker(t, db, chain, "g1val", database.MonikerFallbackAddr))
}
