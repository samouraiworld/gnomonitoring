package testoutils

import (
	"os"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// NewTestDB creates a temporary SQLite database for testing
// and ensures it is cleaned up after the test finishes.
func NewTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	// create a temp file for the sqlite db
	tmpfile, err := os.CreateTemp("", "gormtest-*.db")
	require.NoError(t, err)

	tmpfile.Close()

	// remove file after test ends
	t.Cleanup(func() {
		os.Remove(tmpfile.Name())
	})

	// connect gorm to sqlite temp file
	db, err := database.InitDB(tmpfile.Name())
	require.NoError(t, err)
	FakeData(t, db)

	return db
}
// TestChainID is the chainID used by FakeData seeds and should be used in
// tests that query seeded rows.
const TestChainID = "betanet"

func FakeData(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Dates anchored to the current month so queries scoped to "current_month"
	// (e.g. GetCurrentPeriodParticipationRate) can find the seeded rows.
	now := time.Now().UTC()
	day1 := time.Date(now.Year(), now.Month(), 1, 18, 14, 6, 0, time.UTC)
	day2 := time.Date(now.Year(), now.Month(), 2, 18, 14, 6, 0, time.UTC)
	day3 := time.Date(now.Year(), now.Month(), 3, 18, 14, 6, 0, time.UTC)

	// Insert
	rows := []database.DailyParticipation{
		{ChainID: TestChainID, Addr: "g1abc", BlockHeight: 50, Date: day1, Participated: true, TxContribution: false},
		{ChainID: TestChainID, Addr: "g1abc", BlockHeight: 51, Date: day2, Participated: true, TxContribution: true},
		{ChainID: TestChainID, Addr: "g1abc", BlockHeight: 52, Date: day3, Participated: false, TxContribution: false},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}
}
