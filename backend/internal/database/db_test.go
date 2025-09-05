package database_test

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestInsertAndGetUser(t *testing.T) {
	db := testoutils.NewTestDB(t)

	err := database.InsertUser("user123", "test@example.com", "Alice", db)
	if err != nil {
		t.Fatalf("InsertUser failed: %v", err)
	}

	user, err := database.GetUserById(db, "user123")
	if err != nil {
		t.Fatalf("GetUserById failed: %v", err)
	}
	if user == nil || user.Email != "test@example.com" {
		t.Errorf("Unexpected user result: %+v", user)
	}
}

func TestInsertWebhookAndList(t *testing.T) {
	db := testoutils.NewTestDB(t)

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
	db := testoutils.NewTestDB(t)

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
	if user.Name != "UpdatedName" || user.Email != "new@example.com" {
		t.Errorf("User not updated correctly: %+v", user)
	}
}
