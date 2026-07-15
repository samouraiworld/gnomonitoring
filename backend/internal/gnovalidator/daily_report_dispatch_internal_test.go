package gnovalidator

import (
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestDispatchDailyReportToWebhooks_SendsEmbedToDiscordAndBlocksToSlack(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Swap the package-level sendDiscordEmbed/sendSlackBlocks function
	// variables (see their declaration in gnovalidator_report.go) for fakes
	// that record their arguments, instead of going through real HTTP and
	// the SSRF-guarded alertHTTPClient in package internal.
	origSendDiscordEmbed := sendDiscordEmbed
	origSendSlackBlocks := sendSlackBlocks
	defer func() {
		sendDiscordEmbed = origSendDiscordEmbed
		sendSlackBlocks = origSendSlackBlocks
	}()

	var discordEmbed internal.DiscordEmbed
	var discordURL string
	var discordCalled bool
	sendDiscordEmbed = func(embed internal.DiscordEmbed, webhookURL string) error {
		discordEmbed = embed
		discordURL = webhookURL
		discordCalled = true
		return nil
	}

	var slackBlocks []internal.SlackBlock
	var slackURL string
	var slackCalled bool
	sendSlackBlocks = func(blocks []internal.SlackBlock, webhookURL string) error {
		slackBlocks = blocks
		slackURL = webhookURL
		slackCalled = true
		return nil
	}

	const discordWebhookURL = "https://discord.com/api/webhooks/fake"
	const slackWebhookURL = "https://hooks.slack.com/services/fake"

	db.Create(&database.WebhookValidator{UserID: "u1", URL: discordWebhookURL, Type: "discord", Description: "d"})
	db.Create(&database.WebhookValidator{UserID: "u1", URL: slackWebhookURL, Type: "slack", Description: "s"})

	data := DailyReportData{ChainID: "test12", Date: "2025-11-02", TotalCount: 1, AllHealthy: true}
	if err := dispatchDailyReportToWebhooks(db, "u1", "test12", data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !discordCalled {
		t.Fatalf("expected sendDiscordEmbed to be called for the discord-type webhook")
	}
	if discordURL != discordWebhookURL {
		t.Fatalf("expected discord embed sent to %s, got %s", discordWebhookURL, discordURL)
	}
	if discordEmbed.Title == "" && discordEmbed.Description == "" && len(discordEmbed.Fields) == 0 {
		t.Fatalf("expected a non-empty embed sent to the Discord webhook, got: %+v", discordEmbed)
	}

	if !slackCalled {
		t.Fatalf("expected sendSlackBlocks to be called for the slack-type webhook")
	}
	if slackURL != slackWebhookURL {
		t.Fatalf("expected slack blocks sent to %s, got %s", slackWebhookURL, slackURL)
	}
	if len(slackBlocks) == 0 {
		t.Fatalf("expected a non-empty blocks payload sent to the Slack webhook, got: %+v", slackBlocks)
	}
}
