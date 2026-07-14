package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
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
