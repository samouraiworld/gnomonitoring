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
		// addrB missing => participated=false expected
	}
	monikers := map[string]string{"addrA": "Val A", "addrB": "Val B"}

	err := gnovalidator.SaveParticipation(db, "testchain", 500, participating, monikers, blockTime)
	require.NoError(t, err)

	var rows []database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ? AND block_height = 500", "testchain").
		Order("addr").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, "addrA", rows[0].Addr)
	require.True(t, rows[0].Participated)
	require.True(t, rows[0].TxContribution)
	require.Equal(t, "addrB", rows[1].Addr)
	require.False(t, rows[1].Participated)
	require.False(t, rows[1].TxContribution)
}

func TestGetLastStoredHeight_Empty(t *testing.T) {
	db := testoutils.NewTestDB(t)

	height, err := gnovalidator.GetLastStoredHeight(db, testoutils.TestChainID)
	require.NoError(t, err)
	require.Equal(t, int64(51), height)
}
