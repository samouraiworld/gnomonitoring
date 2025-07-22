package gnovalidator_test

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
)

func setupTestDB(t *testing.T) *sql.DB {
	os.Remove("test_report.db") // Clean old file if any
	db, err := sql.Open("sqlite3", "test_report.db")
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	createTable := `
	CREATE TABLE IF NOT EXISTS daily_participation (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT NOT NULL,
		block_height INTEGER NOT NULL,
		addr TEXT NOT NULL,
		moniker TEXT NOT NULL,
		participated BOOLEAN NOT NULL
	);`
	_, err = db.Exec(createTable)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	return db
}

func insertParticipationData(db *sql.DB, date string, addr string, moniker string, participated []bool, startHeight int) error {
	for i, p := range participated {
		_, err := db.Exec(`
			INSERT INTO daily_participation (date, block_height, addr, moniker, participated)
			VALUES (?, ?, ?, ?, ?)`,
			date, startHeight+i, addr, moniker, p)
		if err != nil {
			return err
		}
	}
	return nil
}

func TestCalculateRate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	date := time.Now().Format("2006-01-02")

	err := insertParticipationData(db, date, "valoper1", "validator-one", []bool{true, false, true, true}, 100)
	if err != nil {
		t.Fatalf("Insert error: %v", err)
	}
	err = insertParticipationData(db, date, "valoper2", "validator-two", []bool{false, false, false, false}, 200)
	if err != nil {
		t.Fatalf("Insert error: %v", err)
	}

	rates, min, max := gnovalidator.CalculateRate(db, date)

	if len(rates) != 2 {
		t.Errorf("Expected 2 validators, got %d", len(rates))
	}

	if rate := rates["valoper1"]; rate != 75.0 {
		t.Errorf("Expected rate 75.0 for valoper1, got %.2f", rate)
	}
	if rate := rates["valoper2"]; rate != 0.0 {
		t.Errorf("Expected rate 0.0 for valoper2, got %.2f", rate)
	}

	if min != 100 {
		t.Errorf("Expected min block 100, got %d", min)
	}
	if max != 203 {
		t.Errorf("Expected max block 203, got %d", max)
	}
}
