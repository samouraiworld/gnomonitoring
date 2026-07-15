package gnovalidator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestDispatchDailyReportToWebhooks_SendsEmbedToDiscordAndBlocksToSlack(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// The outbound webhook client (internal.alertHTTPClient) rejects loopback
	// destinations by design (SSRF hardening). Point it at a plain client for
	// the duration of this test so it can reach the httptest.Server below,
	// mirroring the same-package swap pattern in internal/fonction_test.go —
	// exposed here via SetAlertHTTPClientForTest since alertHTTPClient itself
	// is unexported and this test lives outside package internal.
	restore := internal.SetAlertHTTPClientForTest(&http.Client{})
	defer restore()

	var discordBody, slackBody map[string]any
	discordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&discordBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer discordSrv.Close()
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&slackBody)
		w.Write([]byte("ok"))
	}))
	defer slackSrv.Close()

	db.Create(&database.WebhookValidator{UserID: "u1", URL: discordSrv.URL, Type: "discord", Description: "d"})
	db.Create(&database.WebhookValidator{UserID: "u1", URL: slackSrv.URL, Type: "slack", Description: "s"})

	data := DailyReportData{ChainID: "test12", Date: "2025-11-02", TotalCount: 1, AllHealthy: true}
	if err := dispatchDailyReportToWebhooks(db, "u1", "test12", data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := discordBody["embeds"]; !ok {
		t.Fatalf("expected an embeds payload sent to the Discord webhook, got: %+v", discordBody)
	}
	if _, ok := slackBody["blocks"]; !ok {
		t.Fatalf("expected a blocks payload sent to the Slack webhook, got: %+v", slackBody)
	}
}
