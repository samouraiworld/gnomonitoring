package testutils

import (
	"os"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// NewTestDB creates a temporary SQLite database for testing
// and ensures it is cleaned up after the test finishes.
func NewTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	// create a temp file for the sqlite db
	tmpfile, err := os.CreateTemp("", "gormtest-*.db")
	if err != nil {
		t.Fatalf("failed to create temp db file: %v", err)
	}
	tmpfile.Close()

	// remove file after test ends
	t.Cleanup(func() {
		os.Remove(tmpfile.Name())
	})

	// connect gorm to sqlite temp file
	db, err := gorm.Open(sqlite.Open(tmpfile.Name()), &gorm.Config{})
	require.NoError(t, err)

	err = database.Migrate(db)
	require.NoError(t, err)

	return db
}
