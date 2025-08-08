package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Setup test DB and router
func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	assert.NoError(t, err)

	// Auto-migrate needed tables
	err = db.AutoMigrate(
		&database.User{}, &database.AlertContact{}, &database.WebhookValidator{},
		&database.WebhookGovDAO{}, &database.HourReport{}, &database.GovDAOState{},
		&database.DailyParticipation{}, &database.AlertLog{}, &database.AddrMoniker{},
	)
	assert.NoError(t, err)

	return db
}

// ---------- TEST /webhooks ----------

func TestGetWebhooks(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&database.Webhook{Description: "Test", URL: "http://localhost", Type: "val"})

	req := httptest.NewRequest(http.MethodGet, "/webhooks", nil)
	w := httptest.NewRecorder()

	internal.GetWebhooks(db).ServeHTTP(w, req)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Contains(t, string(body), "http://localhost")
}

// ---------- TEST /alerts ----------

func TestGetAlerts(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&database.AlertLog{
		Moniker:     "Validator1",
		Level:       "CRITICAL",
		StartHeight: 100,
		EndHeight:   200,
		SentAt:      time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	w := httptest.NewRecorder()

	api.GetAlertLogHandler(db).ServeHTTP(w, req)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Contains(t, string(body), "Validator1")
}

// ---------- TEST /reports ----------

func TestGetHourReports(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&internal.HourReport{
		Addr:      "addr1",
		Moniker:   "moniker1",
		Hour:      "2025-08-01 13:00:00",
		Missed:    3,
		Total:     100,
		CreatedAt: time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/reports", nil)
	w := httptest.NewRecorder()

	internal.GetHourReports(db).ServeHTTP(w, req)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Contains(t, string(body), "moniker1")
}

// ---------- TEST /info/blockheight ----------

func TestGetBlockHeight(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/info/blockheight", nil)
	w := httptest.NewRecorder()

	database.GetBlockHeight().ServeHTTP(w, req)
	res := w.Result()
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)

	var response map[string]int64
	err := json.NewDecoder(res.Body).Decode(&response)
	assert.NoError(t, err)
	_, exists := response["block_height"]
	assert.True(t, exists)
}
