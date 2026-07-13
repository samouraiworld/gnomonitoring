package gnovalidator_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"

	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/require"
)

func TestSaveParticipation(t *testing.T) {
	db := testoutils.NewTestDB(t)
	blockTime := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)

	mockParticipation := map[string]gnovalidator.Participation{
		"addr1": {Participated: true, Timestamp: blockTime, TxContribution: true},
		"addr2": {Participated: false, Timestamp: blockTime, TxContribution: false},
	}

	mockMonikers := map[string]string{
		"addr1": "Validator One",
		"addr2": "Validator Two",
	}

	err := gnovalidator.SaveParticipation(db, "testchain", 123, mockParticipation, mockMonikers, blockTime)
	if err != nil {
		t.Fatalf("SaveParticipation failed: %v", err)
	}

	participation := database.DailyParticipation{}
	err = db.Model(&participation).Where("date = ? AND addr = ? AND block_height = 123", blockTime, "addr1").First(&participation).Error
	require.NoError(t, err)

	if !participation.Participated {
		t.Errorf("expected val1 to have participated")
	}
}

func TestSaveParticipationBatch(t *testing.T) {
	db := testoutils.NewTestDB(t)
	blockTime := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)

	participating := map[string]gnovalidator.Participation{
		"addrA": {Participated: true, Timestamp: blockTime, TxContribution: true},
		// addrB missing => participated=false, but addrB has never had a true
		// participation recorded (first_active_block unknown), so this row is
		// skipped rather than written as a phantom pre-activation row.
	}
	monikers := map[string]string{"addrA": "Val A", "addrB": "Val B"}

	err := gnovalidator.SaveParticipation(db, "testchain", 500, participating, monikers, blockTime)
	require.NoError(t, err)

	var rows []database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ? AND block_height = 500", "testchain").
		Order("addr").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "addrA", rows[0].Addr)
	require.True(t, rows[0].Participated)
	require.True(t, rows[0].TxContribution)

	// Once addrB has a genuine true participation (activation), a later real
	// miss for it must be recorded normally.
	require.NoError(t, gnovalidator.SaveParticipation(db, "testchain", 501,
		map[string]gnovalidator.Participation{"addrB": {Participated: true, Timestamp: blockTime}},
		monikers, blockTime))
	require.NoError(t, gnovalidator.SaveParticipation(db, "testchain", 502,
		map[string]gnovalidator.Participation{"addrA": {Participated: true, Timestamp: blockTime, TxContribution: true}},
		monikers, blockTime))

	var missRow database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ? AND block_height = 502 AND addr = ?", "testchain", "addrB").
		First(&missRow).Error)
	require.False(t, missRow.Participated, "a real miss after activation must be recorded, not skipped")
}

func TestSetFirstActiveBlockIfEarlier(t *testing.T) {
	chainID := "test-set-fab-if-earlier"
	addr := "g1val"

	// Unset (-1) -> any height is recorded, and the change is reported.
	require.True(t, gnovalidator.SetFirstActiveBlockIfEarlier(chainID, addr, 50))
	require.Equal(t, int64(50), gnovalidator.GetFirstActiveBlock(chainID, addr))

	// A later height must not overwrite an already-recorded earlier one.
	require.False(t, gnovalidator.SetFirstActiveBlockIfEarlier(chainID, addr, 60))
	require.Equal(t, int64(50), gnovalidator.GetFirstActiveBlock(chainID, addr))

	// An earlier height must lower the recorded value and report the change.
	require.True(t, gnovalidator.SetFirstActiveBlockIfEarlier(chainID, addr, 40))
	require.Equal(t, int64(40), gnovalidator.GetFirstActiveBlock(chainID, addr))

	// The exact same height is not an improvement — no-op, no change reported.
	require.False(t, gnovalidator.SetFirstActiveBlockIfEarlier(chainID, addr, 40))
}

func TestGetLastStoredHeight_Empty(t *testing.T) {
	db := testoutils.NewTestDB(t)

	height, err := gnovalidator.GetLastStoredHeight(db, testoutils.TestChainID)
	require.NoError(t, err)
	require.Equal(t, int64(51), height)
}

func TestReplaceMonikerMap(t *testing.T) {
	chainID := "test-replace-moniker"

	gnovalidator.ReplaceMonikerMap(chainID, map[string]string{
		"g1old":  "Old Validator",
		"g1stay": "Still Here",
	})
	require.Equal(t, "Old Validator", gnovalidator.GetMonikerMap(chainID)["g1old"])

	// A second replace with a different set must DROP g1old entirely, not
	// merge it in — this is the behavior that stops SaveParticipation from
	// writing rows for validators that have left the valset forever.
	gnovalidator.ReplaceMonikerMap(chainID, map[string]string{
		"g1stay": "Still Here",
		"g1new":  "New Validator",
	})
	got := gnovalidator.GetMonikerMap(chainID)
	require.Len(t, got, 2)
	require.Equal(t, "Still Here", got["g1stay"])
	require.Equal(t, "New Validator", got["g1new"])
	_, stillPresent := got["g1old"]
	require.False(t, stillPresent, "g1old must be pruned after ReplaceMonikerMap, not accumulated")
}
