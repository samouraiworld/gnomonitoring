package internal_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
)

// Helper to init DB with schema
func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS users (
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		user_id TEXT PRIMARY KEY,
		nameuser TEXT NOT NULL,
		email TEXT NOT NULL UNIQUE,
		daily_report_hour INTEGER DEFAULT 9,
		daily_report_minute INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS alert_contacts (
		created_at DEFAULT CURRENT_TIMESTAMP,
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		moniker TEXT NOT NULL,
		namecontact TEXT NOT NULL,
		mention_tag TEXT
	);

	CREATE TABLE IF NOT EXISTS webhooks_govdao (
		created_at DEFAULT CURRENT_TIMESTAMP,
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		url TEXT NOT NULL,
		type TEXT NOT NULL CHECK(type IN ('discord', 'slack')),
		last_checked_id INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS webhooks_validator (
		created_at DEFAULT CURRENT_TIMESTAMP,
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT  NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		url TEXT NOT NULL,
		type TEXT NOT NULL CHECK(type IN ('discord', 'slack'))
	);

	CREATE TABLE IF NOT EXISTS daily_participation (
		date TEXT NOT NULL,
		block_height INTEGER NOT NULL,
		moniker TEXT NOT NULL,
		addr TEXT NOT NULL,
		participated BOOLEAN NOT NULL,
		PRIMARY KEY (date, block_height, moniker)
	);
	`

	_, err = db.Exec(schema)
	if err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	return db
}
func TestInsertAndGetUser(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	err := internal.InsertUser("user_test", "test@example.com", "Alice", db)
	if err != nil {
		t.Fatalf("InsertUser failed: %v", err)
	}

	user, err := internal.GetUserById(db, "user_test")
	if err != nil {
		t.Fatalf("GetUserById failed: %v", err)
	}
	if user.NAME != "Alice" || user.EMAIL != "test@example.com" {
		t.Errorf("Unexpected user data: %+v", user)
	}
}
func TestInsertAndListWebhooks(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Required: insert user first
	err := internal.InsertUser("user_webhook", "web@example.com", "Bob", db)
	if err != nil {
		t.Fatalf("InsertUser failed: %v", err)
	}

	err = internal.InsertWebhook("user_webhook", "https://discord.com/xxx", "discord", db)
	if err != nil {
		t.Fatalf("InsertWebhook failed: %v", err)
	}

	list, err := internal.ListWebhooks(db)
	if err != nil {
		t.Fatalf("ListWebhooks failed: %v", err)
	}
	if len(list) != 1 || list[0].URL != "https://discord.com/xxx" {
		t.Errorf("Webhook not inserted properly: %+v", list)
	}
}
func InsertAlertContact(db *sql.DB, user_id, moniker, name, mention string) error {
	_, err := db.Exec(`
		INSERT INTO alert_contacts (user_id, moniker, namecontact, mention_tag)
		VALUES (?, ?, ?, ?)`, user_id, moniker, name, mention)
	return err
}
func TestInsertAndListAlertContact(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	err := internal.InsertUser("user_alert", "alert@example.com", "Charlie", db)
	if err != nil {
		t.Fatalf("InsertUser failed: %v", err)
	}

	err = InsertAlertContact(db, "user_alert", "validatorX", "John", "@john")
	if err != nil {
		t.Fatalf("InsertAlertContact failed: %v", err)
	}

	contacts, err := internal.ListAlertContacts(db, "user_alert")
	if err != nil {
		t.Fatalf("ListAlertContacts failed: %v", err)
	}
	if len(contacts) != 1 || contacts[0].MENTIONTAG != "@john" {
		t.Errorf("Unexpected alert contact: %+v", contacts)
	}
}
