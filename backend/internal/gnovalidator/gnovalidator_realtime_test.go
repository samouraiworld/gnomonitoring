package gnovalidator_test

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
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
		"val1": "Validator One",
		"val2": "Validator Two",
	}

	err := gnovalidator.SaveParticipation(db, 123, mockParticipation, mockMonikers, blockTime)
	if err != nil {
		t.Fatalf("SaveParticipation failed: %v", err)
	}

	// today := time.Now().Format("2006-01-02")
	participation := database.DailyParticipation{}
	err = db.Model(&participation).Where("DATE(date) = ? AND addr = ? AND block_height = 123", blockTime, "val1").First(&participation).Error
	require.NoError(t, err)

	if !participation.Participated {
		t.Errorf("expected val1 to have participated")
	}
}

func TestGetLastStoredHeight_Empty(t *testing.T) {
	db := testoutils.NewTestDB(t)

	height, err := gnovalidator.GetLastStoredHeight(db)
	require.NoError(t, err)
	require.Equal(t, int64(0), height)
}
