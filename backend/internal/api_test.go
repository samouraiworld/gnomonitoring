package internal

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
	db := setupTestDB(t)
	defer db.Close()

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
	db := setupTestDB(t)
	defer db.Close()

	// Insert fake user
	_, err := db.Exec(`INSERT INTO users (user_id, nameuser, email) VALUES (?, ?, ?)`, "user123", "Alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users?user_id=user123", nil)
	rr := httptest.NewRecorder()

	GetUserHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var user Users
	err = json.NewDecoder(rr.Body).Decode(&user)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if user.USER_ID != "user123" {
		t.Errorf("expected user_id 'user123', got '%s'", user.USER_ID)
	}
}

func TestDeleteUserHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO users (user_id, nameuser, email) VALUES (?, ?, ?)`, "user123", "Bob", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/users?user_id=user123", nil)
	rr := httptest.NewRecorder()

	DeleteUserHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}
func TestUpdateUserHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	_, err := db.Exec(`INSERT INTO users (user_id, nameuser, email) VALUES (?, ?, ?)`, "user123", "Bob", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// JSON for update
	payload := `{"name":"Alice","email":"alice@example.com"}`
	req := httptest.NewRequest(http.MethodPut, "/users?user_id=user123", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()

	// call handler
	UpdateUserHandler(rr, req, db)

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
	db := setupTestDB(t)
	defer db.Close()

	body := `{
		"user": "user123",
		"url": "https://example.com/webhook",
		"type": "discord"
	}`

	req := httptest.NewRequest(http.MethodPost, "/webhooks/govdao", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	log.Println(rr)

	CreateWebhookHandler(rr, req, db)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}
}

func TestListWebhooksHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO webhooks_govdao (user_id, url, type) VALUES (?, ?, ?)`, "user123", "https://example.com", "discord")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/webhooks/govdao?user_id=user123", nil)
	rr := httptest.NewRecorder()

	ListWebhooksHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestDeleteWebhookHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO webhooks_govdao (id, user_id, url, type) VALUES (1, ?, ?, ?)`, "user123", "https://example.com", "discord")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/webhooks/govdao?id=1&user_id=user123", nil)
	rr := httptest.NewRecorder()

	DeleteWebhookHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestUpdateWebhookHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO webhooks_govdao (id, user_id, url, type) VALUES (1, ?, ?, ?)`, "user123", "https://example.com", "discord")
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

	UpdateWebhookHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

// ============================================ VALIDATOR===========================
func TestCreateMonitoringWebhookHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	body := `{
		"user": "user123",
		"url": "https://example.com/validator",
		"type": "discord"
	}`

	req := httptest.NewRequest(http.MethodPost, "/webhooks/validator", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	CreateMonitoringWebhookHandler(rr, req, db)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}
}

func TestListMonitoringWebhooksHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO webhooks_validator (user_id, url, type) VALUES (?, ?, ?)`, "user123", "https://example.com/validator", "discord")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/webhooks/validator?user_id=user123", nil)
	rr := httptest.NewRecorder()

	ListMonitoringWebhooksHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestDeleteMonitoringWebhookHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO webhooks_validator (id, user_id, url, type) VALUES (1, ?, ?, ?)`, "user123", "https://example.com/validator", "discord")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/webhooks/validator?id=1&user_id=user123", nil)
	rr := httptest.NewRecorder()

	DeleteMonitoringWebhookHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestUpdateMonitoringWebhookHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO webhooks_validator (id, user_id, url, type) VALUES (1, ?, ?, ?)`, "user123", "https://old.com", "discord")
	if err != nil {
		t.Fatal(err)
	}

	payload := `{
		"id": 1,
		"user": "user123",
		"url": "https://new.com",
		"type": "discord"
	}`

	req := httptest.NewRequest(http.MethodPut, "/webhooks/validator", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	UpdateMonitoringWebhookHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

// =======================ALERT CONTACT
func TestInsertAlertContactHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	payload := `{
		"user_id": "user123",
		"moniker": "monikerX",
		"namecontact": "contactX",
		"mention_tag": "@mention"
	}`

	req := httptest.NewRequest(http.MethodPost, "/alert-contacts", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	InsertAlertContactHandler(rr, req, db)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}
}

func TestGetAlertContactsHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO alert_contacts (user_id, moniker, namecontact, mention_tag) VALUES (?, ?, ?, ?)`, "user123", "monikerX", "contactX", "@mention")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/alert-contacts?user_id=user123", nil)
	rr := httptest.NewRecorder()

	GetAlertContactsHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestUpdateAlertContactHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO alert_contacts (id, user_id, moniker, namecontact, mention_tag) VALUES (1, ?, ?, ?, ?)`, "user123", "monikerX", "contactX", "@mention")
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

	UpdateAlertContactHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestDeleteAlertContactHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO alert_contacts (id, user_id, moniker, namecontact, mention_tag) VALUES (1, ?, ?, ?, ?)`, "user123", "monikerX", "contactX", "@mention")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/alert-contacts?id=1", nil)
	rr := httptest.NewRecorder()

	DeleteAlertContactHandler(rr, req, db)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}
