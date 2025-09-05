package gnovalidator

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testutils"
	"github.com/stretchr/testify/require"
)

func TestSaveParticipation(t *testing.T) {
	db := testutils.NewTestDB(t)
	//defer db.Close()

	mockParticipation := map[string]bool{
		"val1": true,
		"val2": false,
	}

	mockMonikers := map[string]string{
		"val1": "Validator One",
		"val2": "Validator Two",
	}

	err := SaveParticipation(db, 123, mockParticipation, mockMonikers)
	if err != nil {
		t.Fatalf("SaveParticipation failed: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	//row := db.QueryRow(`SELECT participated FROM daily_participation WHERE date = ? AND addr = ?`, today, "val1")
	var participation database.DailyParticipation
	err = db.Model(&database.DailyParticipation{}).Where("addr = ? AND date = ?", "val1", today).First(&participation).Error
	require.NoError(t, err)

	if !participation.Participated {
		t.Errorf("expected val1 to have participated")
	}
}

func TestGetLastStoredHeight_Empty(t *testing.T) {
	db := testutils.NewTestDB(t)

	height, err := GetLastStoredHeight(db)
	require.NoError(t, err)
	require.Equal(t, int64(0), height)
}
