package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestValidateWebhookURL(t *testing.T) {
	cases := []struct {
		name      string
		whType    string
		url       string
		wantError bool
	}{
		{"discord valid discord.com", "discord", "https://discord.com/api/webhooks/1/abc", false},
		{"discord valid discordapp.com", "discord", "https://discordapp.com/api/webhooks/1/abc", false},
		{"discord wrong host", "discord", "https://evil.example/webhooks/1/abc", true},
		{"discord http scheme rejected", "discord", "http://discord.com/api/webhooks/1/abc", true},
		{"discord loopback rejected", "discord", "https://127.0.0.1/api/webhooks/1/abc", true},
		{"slack valid hooks.slack.com", "slack", "https://hooks.slack.com/services/T0/B0/xyz", false},
		{"slack wrong host", "slack", "https://hooks.evil.example/services/T0/B0/xyz", true},
		{"unknown type rejected", "teams", "https://discord.com/api/webhooks/1/abc", true},
		{"malformed url rejected", "discord", "://not a url", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWebhookURL(tc.whType, tc.url)
			if tc.wantError && err == nil {
				t.Fatalf("expected error for %s/%s, got nil", tc.whType, tc.url)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("expected no error for %s/%s, got %v", tc.whType, tc.url, err)
			}
		})
	}
}

func TestCreateMonitoringWebhookHandler_RejectsDisallowedHost(t *testing.T) {
	db := testoutils.NewTestDB(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	body := `{"url":"https://internal.attacker.example/steal","type":"discord","description":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/validator", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-UserID", "test-user")
	rec := httptest.NewRecorder()

	CreateMonitoringWebhookHandler(rec, req, db)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not allowed") {
		t.Fatalf("body = %q, want it to mention the host is not allowed", rec.Body.String())
	}
}

func withTestChain(t *testing.T) {
	t.Helper()
	internal.Config.Chains = map[string]*internal.ChainConfig{
		"test12": {
			RPCEndpoints:     []string{"http://localhost:26657"},
			GraphqlEndpoints: []string{"http://localhost:8080/graphql/query"},
			GnowebEndpoints:  []string{"http://localhost:8080"},
			Enabled:          true,
		},
	}
	internal.EnabledChains = []string{"test12"}
	internal.Config.DefaultChain = "test12"
	t.Cleanup(func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	})
}

// TestCreateMonitoringWebhookHandler_RequiresChainID pins the fix that
// chain_id is now mandatory at webhook registration: a webhook_validators row
// with no chain_id makes the daily-report dispatch (chain_id = ? OR chain_id
// IS NULL) and the plain-text alert dispatch (chain_id = ?, no NULL match)
// silently disagree on who receives a report.
func TestCreateMonitoringWebhookHandler_RequiresChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withTestChain(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	body := `{"url":"https://discord.com/api/webhooks/1/abc","type":"discord","description":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/validator", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-UserID", "test-user")
	rec := httptest.NewRecorder()

	CreateMonitoringWebhookHandler(rec, req, db)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chain_id is required") {
		t.Fatalf("body = %q, want it to mention chain_id is required", rec.Body.String())
	}
}

// TestCreateMonitoringWebhookHandler_SucceedsWithChainID also pins the fix to
// the missing `json:"chain_id"` struct tag on database.WebhookValidator:
// without it, the request body's chain_id never decoded into webhook.ChainID
// at all, so chain-scoped creation silently behaved as unscoped.
func TestCreateMonitoringWebhookHandler_SucceedsWithChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withTestChain(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	body := `{"url":"https://discord.com/api/webhooks/1/abc","type":"discord","description":"x","chain_id":"test12"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/validator", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-UserID", "test-user")
	rec := httptest.NewRecorder()

	CreateMonitoringWebhookHandler(rec, req, db)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}

	var wh database.WebhookValidator
	if err := db.Where("user_id = ?", "test-user").First(&wh).Error; err != nil {
		t.Fatalf("expected the webhook row to be persisted: %v", err)
	}
	if wh.ChainID == nil || *wh.ChainID != "test12" {
		t.Fatalf("expected persisted chain_id = %q, got %+v", "test12", wh.ChainID)
	}
}

func TestCreateWebhookHandler_RequiresChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withTestChain(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	body := `{"url":"https://discord.com/api/webhooks/1/abc","type":"discord","description":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/govdao", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-UserID", "test-user")
	rec := httptest.NewRecorder()

	CreateWebhookHandler(rec, req, db)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chain_id is required") {
		t.Fatalf("body = %q, want it to mention chain_id is required", rec.Body.String())
	}
}

// TestUpdateWebhookHandler_RejectsClearingChainID ensures an update can't
// blank out an already-required chain_id back to unscoped.
func TestUpdateWebhookHandler_RejectsClearingChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withTestChain(t)
	internal.Config.DevMode = true
	defer func() { internal.Config.DevMode = false }()

	chainID := "test12"
	db.Create(&database.WebhookGovDAO{
		UserID: "test-user", URL: "https://discord.com/api/webhooks/1/abc",
		Type: "discord", Description: "x", ChainID: &chainID,
	})

	body := `{"url":"https://discord.com/api/webhooks/1/abc","type":"discord","description":"x","chain_id":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/webhooks/govdao", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-UserID", "test-user")
	rec := httptest.NewRecorder()

	UpdateWebhookHandler(rec, req, db)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chain_id is required") {
		t.Fatalf("body = %q, want it to mention chain_id is required", rec.Body.String())
	}
}
