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
func FakeData(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Insert
	rows := []database.DailyParticipation{
		{Addr: "g1abc", Date: mustParseDate("2025-09-15 18:14:06"), Participated: true},
		{Addr: "g1abc", Date: mustParseDate("2025-10-01 18:14:06"), Participated: true},
		{Addr: "g1abc", Date: mustParseDate("2025-10-02 18:14:06"), Participated: false},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}
}
func mustParseDate(value string) time.Time {
	layout := "2006-01-02 15:04:05"
	t, err := time.Parse(layout, value)
	if err != nil {
		panic(err) // ok pour des seeds/tests
	}
	return t
}
