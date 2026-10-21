package gnovalidator_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These pin the queries that resolve monikers through database.MonikerSQL in
// this package: a live addr_monikers name wins over the frozen row moniker,
// and the 'unknown' placeholder falls through to the most recent frozen name.

func seedRows(t *testing.T, db *gorm.DB, chain, addr, moniker string, from, to int64, participated bool) {
	t.Helper()
	now := time.Now().UTC()
	for h := from; h <= to; h++ {
		require.NoError(t, db.Create(&database.DailyParticipation{
			ChainID: chain, Addr: addr, Moniker: moniker, BlockHeight: h,
			Date: now, Participated: participated,
		}).Error)
	}
}

func TestSendResolveAlerts_UsesResolvedMoniker(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-resolve-moniker"

	// g1val: live name, two pending alerts; only the latest one is resolved.
	seedRows(t, db, chain, "g1val", "frozen-name", 5, 14, false)
	seedRows(t, db, chain, "g1val", "frozen-name", 15, 15, true)
	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1val", "live-name"))
	require.NoError(t, database.InsertAlertlog(db, chain, "g1val", "", "WARNING", 5, 8, true, time.Now().Add(-time.Minute), ""))
	require.NoError(t, database.InsertAlertlog(db, chain, "g1val", "", "WARNING", 10, 14, true, time.Now(), ""))

	// g1unk: placeholder in addr_monikers, renamed while down: the most recent
	// frozen name wins, not MAX(moniker).
	seedRows(t, db, chain, "g1unk", "zeta-old", 20, 24, false)
	seedRows(t, db, chain, "g1unk", "alpha-new", 25, 25, true)
	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1unk", "unknown"))
	require.NoError(t, database.InsertAlertlog(db, chain, "g1unk", "", "WARNING", 20, 24, true, time.Now(), ""))

	gnovalidator.SendResolveAlerts(db, chain)

	var resolved []database.AlertLog
	require.NoError(t, db.Where("chain_id = ? AND level = 'RESOLVED'", chain).Order("addr").Find(&resolved).Error)
	require.Len(t, resolved, 2, "one RESOLVED per validator")
	assert.Equal(t, "g1unk", resolved[0].Addr)
	assert.Equal(t, "alpha-new", resolved[0].Moniker)
	assert.Equal(t, "g1val", resolved[1].Addr)
	assert.Equal(t, "live-name", resolved[1].Moniker)
	assert.EqualValues(t, 10, resolved[1].StartHeight, "latest pending alert is the one resolved")
	assert.EqualValues(t, 14, resolved[1].EndHeight)
}

func TestCalculateConsecutiveMissedBlocks_UsesResolvedMoniker(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-streak-moniker"

	seedRows(t, db, chain, "g1live", "frozen-live", 1, 3, false)
	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1live", "live-name"))
	seedRows(t, db, chain, "g1unk", "older-name", 1, 2, true)
	seedRows(t, db, chain, "g1unk", "newer-name", 3, 3, false)
	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1unk", "unknown"))

	stats, err := gnovalidator.CalculateConsecutiveMissedBlocks(db, chain)
	require.NoError(t, err)
	byAddr := map[string]gnovalidator.ConsecutiveMissedStat{}
	for _, s := range stats {
		byAddr[s.Address] = s
	}
	require.Len(t, byAddr, 2)
	assert.Equal(t, "live-name", byAddr["g1live"].Moniker)
	assert.Equal(t, 3, byAddr["g1live"].Count)
	assert.Equal(t, "newer-name", byAddr["g1unk"].Moniker)
	assert.Equal(t, 1, byAddr["g1unk"].Count)
}

func TestCalculateRecentValidatorStatus_UsesResolvedMoniker(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chain = "chain-recent-moniker"

	seedRows(t, db, chain, "g1live", "frozen-live", 1, 4, true)
	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1live", "live-name"))
	seedRows(t, db, chain, "g1unk", "zeta-old", 1, 2, true)
	seedRows(t, db, chain, "g1unk", "alpha-new", 3, 4, true)
	require.NoError(t, database.UpsertAddrMoniker(db, chain, "g1unk", "unknown"))

	rates, _, _, err := gnovalidator.CalculateRecentValidatorStatus(db, chain, 10)
	require.NoError(t, err)
	assert.Equal(t, "live-name", rates["g1live"].Moniker)
	assert.Equal(t, "alpha-new", rates["g1unk"].Moniker, "most recent frozen name, not MAX(moniker)")
}
