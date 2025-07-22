package gnovalidator

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Setup mock DB schema for tests
func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test DB: %v", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS daily_participation (
		date TEXT NOT NULL,
		block_height INTEGER NOT NULL,
		moniker TEXT NOT NULL,
		addr TEXT NOT NULL,
		participated BOOLEAN NOT NULL,
		PRIMARY KEY (date, block_height, moniker)
	);
	`

	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	return db
}

func TestSaveParticipation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

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
	row := db.QueryRow(`SELECT participated FROM daily_participation WHERE date = ? AND addr = ?`, today, "val1")

	var participated bool
	if err := row.Scan(&participated); err != nil {
		t.Errorf("expected record for val1, got error: %v", err)
	}
	if !participated {
		t.Errorf("expected val1 to have participated")
	}
}

func TestGetLastStoredHeight_Empty(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	height, err := GetLastStoredHeight(db)
	if err == nil {
		t.Errorf("expected error on empty DB, got height: %d", height)
	}
}
