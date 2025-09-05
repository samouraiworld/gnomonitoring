package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3" // pour sqlite
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testutils"
	"github.com/stretchr/testify/require"
)

// 🔧 create db for test
func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open in-memory DB: %v", err)

	}

	schema, err := os.ReadFile("./schema.sql") // ou "./migrations/schema.sql"
	if err != nil {
		log.Fatalf("Error reading schema.sql: %v", err)
	}

	_, err = db.Exec(string(schema))
	if err != nil {
		log.Fatalf("Table creation error: %v", err)
	}
	if err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	return db
}

// ============================= USER TEST ===================================================
func TestCreateUserHandler(t *testing.T) {
	db := testutils.NewTestDB(t)

	body := `{
		"user_id": "user123",
		"name": "Alice",
		"email": "alice@example.com"
	}`

	req := httptest.NewRequest(http.MethodPost, "/users", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	CreateUserhandler(rr, req, db)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}
}

func TestGetUserHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)
	// Insert fake user
	_, err = db.Exec(`INSERT INTO users (user_id, nameuser, email) VALUES (?, ?, ?)`, "user123", "Alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users?user_id=user123", nil)
	rr := httptest.NewRecorder()

	GetUserHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var user database.User
	err = json.NewDecoder(rr.Body).Decode(&user)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if user.UserID != "user123" {
		t.Errorf("expected user_id 'user123', got '%s'", user.UserID)
	}
}

func TestDeleteUserHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO users (user_id, nameuser, email) VALUES (?, ?, ?)`, "user123", "Bob", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/users?user_id=user123", nil)
	rr := httptest.NewRecorder()

	DeleteUserHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}
func TestUpdateUserHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO users (user_id, nameuser, email) VALUES (?, ?, ?)`, "user123", "Bob", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// JSON for update
	payload := `{"name":"Alice","email":"alice@example.com"}`
	req := httptest.NewRequest(http.MethodPut, "/users?user_id=user123", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()

	// call handler
	UpdateUserHandler(rr, req, gormDB)

	// check http resp
	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	// Check data update
	var name, email string
	err = db.QueryRow("SELECT nameuser, email FROM users WHERE user_id = ?", "user123").Scan(&name, &email)
	if err != nil {
		t.Fatal(err)
	}

	if name != "Alice" {
		t.Errorf("expected name 'Alice', got '%s'", name)
	}
	if email != "alice@example.com" {
		t.Errorf("expected email 'alice@example.com', got '%s'", email)
	}

}

// ============================ GOVDAO TEST ===========================================
func TestCreateWebhookHandler(t *testing.T) {
	t.Run("Missing required fields", func(t *testing.T) {
		gormDB := testutils.NewTestDB(t)

		bodyInfo := database.WebhookGovDAO{
			UserID: "user123",
			URL:    "https://example.com/webhook",
			Type:   "discord",
		}

		body, err := json.Marshal(bodyInfo)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/govdao", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		log.Println(rr)

		CreateWebhookHandler(rr, req, gormDB)
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("OK", func(t *testing.T) {
		gormDB := testutils.NewTestDB(t)

		bodyInfo := database.WebhookGovDAO{
			UserID:      "user123",
			URL:         "https://example.com/webhook",
			Type:        "discord",
			Description: "description",
		}

		body, err := json.Marshal(bodyInfo)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/govdao", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		log.Println(rr)

		CreateWebhookHandler(rr, req, gormDB)
		require.Equal(t, http.StatusCreated, rr.Code)
	})
}

func TestListWebhooksHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO webhook_gov_DAOs (user_id, url, type) VALUES (?, ?, ?)`, "user123", "https://example.com", "discord")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/webhooks/govdao?user_id=user123", nil)
	rr := httptest.NewRecorder()

	ListWebhooksHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestDeleteWebhookHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO webhook_gov_DAOs (id, user_id, url, type) VALUES (1, ?, ?, ?)`, "user123", "https://example.com", "discord")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/webhooks/govdao?id=1&user_id=user123", nil)
	rr := httptest.NewRecorder()

	DeleteWebhookHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestUpdateWebhookHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO webhook_gov_DAOs (id, user_id, url, type) VALUES (1, ?, ?, ?)`, "user123", "https://example.com", "discord")
	if err != nil {
		t.Fatal(err)
	}

	payload := `{
		"id": 1,
		"user": "user123",
		"url": "https://updated.com",
		"type": "discord"
	}`

	req := httptest.NewRequest(http.MethodPut, "/webhooks/govdao", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	UpdateWebhookHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

// ============================================ VALIDATOR===========================
func TestCreateMonitoringWebhookHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)

	bodyInfo := database.WebhookGovDAO{
		UserID:      "user123",
		URL:         "https://example.com/webhook",
		Type:        "discord",
		Description: "description",
	}

	body, err := json.Marshal(bodyInfo)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/validator", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	CreateMonitoringWebhookHandler(rr, req, gormDB)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}
}

func TestListMonitoringWebhooksHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	err := gormDB.Save(&database.WebhookValidator{UserID: "user123", URL: "https://example.com/validator", Type: "discord"}).Error
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/webhooks/validator?user_id=user123", nil)
	rr := httptest.NewRecorder()

	ListMonitoringWebhooksHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestDeleteMonitoringWebhookHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)

	err := gormDB.Save(&database.WebhookValidator{UserID: "user123", URL: "https://example.com/validator", Type: "discord"}).Error
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/webhooks/validator?id=1&user_id=user123", nil)
	rr := httptest.NewRecorder()

	DeleteMonitoringWebhookHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestUpdateMonitoringWebhookHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	err := gormDB.Save(&database.WebhookValidator{ID: 1, UserID: "user123", URL: "https://example.com/validator", Type: "discord"}).Error
	require.NoError(t, err)

	payload := `{
		"id": 1,
		"user": "user123",
		"url": "https://new.com",
		"type": "discord"
	}`

	req := httptest.NewRequest(http.MethodPut, "/webhooks/validator", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	UpdateMonitoringWebhookHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
	updatedWebhook := database.WebhookValidator{}
	err = gormDB.Model(&database.WebhookValidator{}).Where("id = ?", 1).First(&updatedWebhook).Error
	require.NoError(t, err)
	require.Equal(t, "https://new.com", updatedWebhook.URL)
}

// =======================ALERT CONTACT
func TestInsertAlertContactHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)

	payload := `{
		"user_id": "user123",
		"moniker": "monikerX",
		"namecontact": "contactX",
		"mention_tag": "@mention"
	}`

	req := httptest.NewRequest(http.MethodPost, "/alert-contacts", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	InsertAlertContactHandler(rr, req, gormDB)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}
}

func TestGetAlertContactsHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO alert_contacts (user_id, moniker, namecontact, mention_tag) VALUES (?, ?, ?, ?)`, "user123", "monikerX", "contactX", "@mention")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/alert-contacts?user_id=user123", nil)
	rr := httptest.NewRecorder()

	GetAlertContactsHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestUpdateAlertContactHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO alert_contacts (id, user_id, moniker, namecontact, mention_tag) VALUES (1, ?, ?, ?, ?)`, "user123", "monikerX", "contactX", "@mention")
	if err != nil {
		t.Fatal(err)
	}

	payload := `{
		"id": 1,
		"moniker": "monikerY",
		"namecontact": "contactY",
		"mention_tag": "@new"
	}`

	req := httptest.NewRequest(http.MethodPut, "/alert-contacts", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	UpdateAlertContactHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestDeleteAlertContactHandler(t *testing.T) {
	gormDB := testutils.NewTestDB(t)
	db, err := gormDB.DB()
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO alert_contacts (id, user_id, moniker, namecontact, mention_tag) VALUES (1, ?, ?, ?, ?)`, "user123", "monikerX", "contactX", "@mention")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/alert-contacts?id=1", nil)
	rr := httptest.NewRecorder()

	DeleteAlertContactHandler(rr, req, gormDB)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}
