package database_test

import (
	"errors"
	"testing"

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

// TestUpdateAlertContact_NotFound covers the RowsAffected backstop: a WHERE
// that matches no row is not a gorm error, so without this check the caller
// could not tell a real update from a no-op. The API handler rejects unknown
// ids earlier, which is why this is asserted at the database layer.
func TestUpdateAlertContact_NotFound(t *testing.T) {
	db := testoutils.NewTestDB(t)

	err := database.UpdateAlertContact(db, 999999, "user123", "val1", "On-call", "123456789012345678", database.NoWebhookLinked)
	if !errors.Is(err, database.ErrAlertContactNotFound) {
		t.Fatalf("err = %v, want ErrAlertContactNotFound", err)
	}
}

// TestUpdateAlertContact_OtherUsersContact pins that the user_id scoping in the
// WHERE clause makes a cross-user update a no-op rather than a silent success.
func TestUpdateAlertContact_OtherUsersContact(t *testing.T) {
	db := testoutils.NewTestDB(t)

	if err := database.InsertAlertContact(db, "owner", "val1", "On-call", "123456789012345678", database.NoWebhookLinked); err != nil {
		t.Fatalf("InsertAlertContact failed: %v", err)
	}
	contacts, err := database.ListAlertContacts(db, "owner")
	if err != nil || len(contacts) != 1 {
		t.Fatalf("ListAlertContacts failed: %v (n=%d)", err, len(contacts))
	}

	err = database.UpdateAlertContact(db, contacts[0].ID, "attacker", "hijacked", "Attacker", "999", database.NoWebhookLinked)
	if !errors.Is(err, database.ErrAlertContactNotFound) {
		t.Fatalf("err = %v, want ErrAlertContactNotFound", err)
	}

	after, err := database.GetAlertContact(db, contacts[0].ID, "owner")
	if err != nil || after == nil {
		t.Fatalf("reload contact: %v (nil=%t)", err, after == nil)
	}
	if after.Moniker != "val1" || after.NameContact != "On-call" {
		t.Fatalf("contact was modified across users: %+v", after)
	}
}
