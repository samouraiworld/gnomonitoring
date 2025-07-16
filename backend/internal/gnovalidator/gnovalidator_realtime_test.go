package gnovalidator_test

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
)

func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS daily_participation (
		date TEXT,
		block_height INTEGER,
		moniker TEXT,
		addr TEXT,
		participated BOOLEAN,
		PRIMARY KEY(date, block_height, addr)
	);
	`
	_, err = db.Exec(schema)
	if err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	return db
}

func TestSaveParticipation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	monikerMap := map[string]string{
		"validator1": "MonikerOne",
		"validator2": "MonikerTwo",
	}
	participating := map[string]bool{
		"validator1": true,
	}

	err := gnovalidator.SaveParticipation(db, 12345, participating, monikerMap)
	if err != nil {
		t.Fatalf("SaveParticipation failed: %v", err)
	}

	rows, err := db.Query(`SELECT addr, participated FROM daily_participation WHERE block_height = 12345`)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	results := make(map[string]bool)
	for rows.Next() {
		var addr string
		var participated bool
		err := rows.Scan(&addr, &participated)
		if err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		results[addr] = participated
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 entries, got %d", len(results))
	}
	if !results["validator1"] {
		t.Errorf("validator1 should have participated")
	}
	if results["validator2"] {
		t.Errorf("validator2 should not have participated")
	}
}
func TestWatchValidatorAlerts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert test data: validator missed 3 blocks
	today := time.Now().Format("2006-01-02")
	stmt := `
	INSERT INTO daily_participation(date, block_height, moniker, addr, participated) VALUES (?, ?, ?, ?, ?)
	`
	for i := 1; i <= 3; i++ {
		_, err := db.Exec(stmt, today, i, "MonikerTest", "validator1", false)
		if err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Sauvegarde les fonctions originales
	originalDiscord := internal.SendDiscordAlertValidator
	originalSlack := internal.SendSlackAlertValidator
	defer func() {
		internal.SendDiscordAlertValidator = originalDiscord
		internal.SendSlackAlertValidator = originalSlack
	}()

	// Override alert functions for testing
	alertCalled := false
	internal.SendDiscordAlertValidator = func(msg string, db *sql.DB) error {
		alertCalled = true
		t.Logf("Discord alert sent: %s", msg)
		return nil
	}
	internal.SendSlackAlertValidator = func(msg string, db *sql.DB) error {
		t.Logf("Slack alert sent: %s", msg)
		return nil
	}

	// Call WatchValidatorAlerts with short delay
	go gnovalidator.WatchValidatorAlerts(db, 500*time.Millisecond)

	// Wait for a bit to let the goroutine run
	time.Sleep(2 * time.Second)

	if !alertCalled {
		t.Errorf("Expected alert to be sent but it was not")
	}
}
