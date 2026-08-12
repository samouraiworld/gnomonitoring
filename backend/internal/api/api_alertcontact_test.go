package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

// TestUpdateAlertContactHandler_RejectsOtherUsersWebhook pins the F7 guard
// that was present on InsertAlertContactHandler (api.go:654-664) but missing
// on UpdateAlertContactHandler: without it, a PUT could silently attach a
// contact to a webhook owned by a different user.
func TestUpdateAlertContactHandler_RejectsOtherUsersWebhook(t *testing.T) {
	db := testoutils.NewTestDB(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	if err := database.InsertMonitoringWebhook("user-a", "https://discord.com/api/webhooks/1/abc", "a's webhook", "discord", "", db); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	webhooksA, err := database.ListMonitoringWebhooks(db, "user-a")
	if err != nil || len(webhooksA) != 1 {
		t.Fatalf("list webhooks for user-a: %v (n=%d)", err, len(webhooksA))
	}
	otherUsersWebhookID := webhooksA[0].ID

	body := fmt.Sprintf(`{"id":1,"moniker":"val1","namecontact":"On-call","mention_tag":"123456789012345678","id_webhook":%d}`, otherUsersWebhookID)
	req := httptest.NewRequest(http.MethodPut, "/alert-contacts", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-UserID", "user-b")
	rec := httptest.NewRecorder()

	UpdateAlertContactHandler(rec, req, db)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Webhook not found") {
		t.Fatalf("body = %q, want it to mention the webhook was not found", rec.Body.String())
	}
}

// TestUpdateAlertContactHandler_RejectsEmptyRequiredFields pins the
// required-field check that was present on InsertAlertContactHandler
// (api.go:641-644) but missing on UpdateAlertContactHandler: without it, a
// PUT with an empty moniker/namecontact would overwrite the stored values
// with "" via Updates(map[string]interface{}{...}), which persists zero
// values unlike a struct-based update.
func TestUpdateAlertContactHandler_RejectsEmptyRequiredFields(t *testing.T) {
	db := testoutils.NewTestDB(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	body := `{"id":1,"moniker":"","namecontact":"On-call","mention_tag":"","id_webhook":0}`
	req := httptest.NewRequest(http.MethodPut, "/alert-contacts", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-UserID", "user-a")
	rec := httptest.NewRecorder()

	UpdateAlertContactHandler(rec, req, db)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Missing required fields") {
		t.Fatalf("body = %q, want it to mention missing required fields", rec.Body.String())
	}
}

// TestUpdateAlertContactHandler_ReturnsNotFoundForMissingContact pins the
// RowsAffected check on UpdateAlertContact (db.go): a WHERE id = ? AND
// user_id = ? that matches nothing previously returned a nil error, so the
// handler answered 200 "Alert contact updated" for a pure no-op.
func TestUpdateAlertContactHandler_ReturnsNotFoundForMissingContact(t *testing.T) {
	db := testoutils.NewTestDB(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	body := `{"id":999999,"moniker":"val1","namecontact":"On-call","mention_tag":"","id_webhook":0}`
	req := httptest.NewRequest(http.MethodPut, "/alert-contacts", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-UserID", "user-a")
	rec := httptest.NewRecorder()

	UpdateAlertContactHandler(rec, req, db)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteMonitoringWebhookHandler_CascadesAlertContacts pins the cascade
// added to DeleteMonitoringWebhook (db.go): AlertContact has no DB-level
// foreign key, so deleting a webhook used to leave orphaned contacts whose
// id_webhook matches nothing — permanently unable to fire a mention.
func TestDeleteMonitoringWebhookHandler_CascadesAlertContacts(t *testing.T) {
	db := testoutils.NewTestDB(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	userID := "user-a"
	if err := database.InsertMonitoringWebhook(userID, "https://discord.com/api/webhooks/1/abc", "webhook", "discord", "", db); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	webhooks, err := database.ListMonitoringWebhooks(db, userID)
	if err != nil || len(webhooks) != 1 {
		t.Fatalf("list webhooks: %v (n=%d)", err, len(webhooks))
	}
	webhookID := webhooks[0].ID

	if err := database.InsertAlertContact(db, userID, "val1", "On-call", "123456789012345678", webhookID); err != nil {
		t.Fatalf("seed alert contact: %v", err)
	}
	contacts, err := database.ListAlertContacts(db, userID)
	if err != nil || len(contacts) != 1 {
		t.Fatalf("list contacts before delete: %v (n=%d)", err, len(contacts))
	}

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/webhooks/validator?id=%d", webhookID), nil)
	req.Header.Set("X-Debug-UserID", userID)
	rec := httptest.NewRecorder()

	DeleteMonitoringWebhookHandler(rec, req, db)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	contactsAfter, err := database.ListAlertContacts(db, userID)
	if err != nil {
		t.Fatalf("list contacts after delete: %v", err)
	}
	if len(contactsAfter) != 0 {
		t.Fatalf("contacts after delete = %d, want 0 (orphaned id_webhook=%d)", len(contactsAfter), webhookID)
	}
}
