package database_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

func setupInMemoryDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create in-memory DB: %v", err)
	}

	schema := `
	CREATE TABLE users (
		user_id TEXT PRIMARY KEY,
		email TEXT,
		nameuser TEXT,
		daily_report_hour INTEGER,
		daily_report_minute INTEGER
	);
	CREATE TABLE webhooks_govdao (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT,
		url TEXT,
		type TEXT,
		last_checked_id INTEGER
	);
	CREATE TABLE webhooks_validator (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT,
		url TEXT,
		type TEXT
	);
	CREATE TABLE alert_contacts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT,
		moniker TEXT,
		namecontact TEXT,
		mention_tag TEXT
	);
	`
	_, err = db.Exec(schema)
	if err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	return db
}

func TestInsertAndGetUser(t *testing.T) {
	db := setupInMemoryDB(t)
	defer db.Close()

	err := database.InsertUser("user123", "test@example.com", "Alice", db)
	if err != nil {
		t.Fatalf("InsertUser failed: %v", err)
	}

	user, err := database.GetUserById(db, "user123")
	if err != nil {
		t.Fatalf("GetUserById failed: %v", err)
	}
	if user == nil || user.EMAIL != "test@example.com" {
		t.Errorf("Unexpected user result: %+v", user)
	}
}

func TestInsertWebhookAndList(t *testing.T) {
	db := setupInMemoryDB(t)
	defer db.Close()

	err := database.InsertWebhook("user123", "https://discord.com/hook", "test discord webhook", "discord", db)
	if err != nil {
		t.Fatalf("InsertWebhook failed: %v", err)
	}

	webhooks, err := database.ListWebhooks(db, "user123")
	if err != nil {
		t.Fatalf("ListWebhooks failed: %v", err)
	}
	if len(webhooks) != 1 {
		t.Errorf("Expected 1 webhook, got %d", len(webhooks))
	}
	if webhooks[0].URL != "https://discord.com/hook" {
		t.Errorf("Unexpected webhook URL: %s", webhooks[0].URL)
	}
}

func TestUpdateUser(t *testing.T) {
	db := setupInMemoryDB(t)
	defer db.Close()

	err := database.InsertUser("user123", "initial@example.com", "Initial", db)
	if err != nil {
		t.Fatalf("InsertUser failed: %v", err)
	}

	err = database.UpdateUser(db, "UpdatedName", "new@example.com", "user123")
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}

	user, err := database.GetUserById(db, "user123")
	if err != nil {
		t.Fatalf("GetUserById failed: %v", err)
	}
	if user.NAME != "UpdatedName" || user.EMAIL != "new@example.com" {
		t.Errorf("User not updated correctly: %+v", user)
	}
}
