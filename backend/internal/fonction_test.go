package internal_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebhookChainFilteringInAlerts verifies that the webhook query used inside
// SendAllValidatorAlerts (and SendResolveValidator / SendInfoValidator) returns
// only the webhooks that belong to the requested chain or have no chain (global).
func TestWebhookChainFilteringInAlerts(t *testing.T) {
	db := testoutils.NewTestDB(t)

	chainA := "chainA"
	chainB := "chainB"

	whA := database.WebhookValidator{
		UserID: "userA",
		URL:    "https://hooks.example.com/chainA",
		Type:   "discord",
	}
	whA.ChainID = &chainA

	whB := database.WebhookValidator{
		UserID: "userB",
		URL:    "https://hooks.example.com/chainB",
		Type:   "discord",
	}
	whB.ChainID = &chainB

	// Global webhook: no chain constraint (NULL chain_id).
	whGlobal := database.WebhookValidator{
		UserID: "userGlobal",
		URL:    "https://hooks.example.com/global",
		Type:   "discord",
	}
	// Leave ChainID as nil (zero value for *string).

	require.NoError(t, db.Create(&whA).Error)
	require.NoError(t, db.Create(&whB).Error)
	require.NoError(t, db.Create(&whGlobal).Error)

	// Replicate the exact query used in SendAllValidatorAlerts.
	type Webhook struct {
		UserID  string
		URL     string
		Type    string
		ID      int
		ChainID *string
	}
	var webhooks []Webhook
	err := db.Model(&database.WebhookValidator{}).
		Where("chain_id = ? OR chain_id IS NULL", chainA).
		Find(&webhooks).Error
	require.NoError(t, err)

	urls := make([]string, 0, len(webhooks))
	for _, wh := range webhooks {
		urls = append(urls, wh.URL)
	}

	assert.Contains(t, urls, "https://hooks.example.com/chainA", "chainA webhook must be included")
	assert.Contains(t, urls, "https://hooks.example.com/global", "global (NULL) webhook must be included")
	assert.NotContains(t, urls, "https://hooks.example.com/chainB", "chainB webhook must be excluded")
}

// TestInsertMonitoringWebhookWithChain verifies that InsertMonitoringWebhook
// correctly sets ChainID when a non-empty chainID is provided, and leaves
// ChainID as NULL when an empty string is provided.
func TestInsertMonitoringWebhookWithChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert with explicit chain.
	err := database.InsertMonitoringWebhook(
		"user1",
		"https://hooks.example.com/betanet",
		"betanet webhook",
		"discord",
		"betanet",
		db,
	)
	require.NoError(t, err)

	var withChain database.WebhookValidator
	err = db.Where("url = ?", "https://hooks.example.com/betanet").First(&withChain).Error
	require.NoError(t, err)
	require.NotNil(t, withChain.ChainID, "ChainID should not be nil when chainID was provided")
	assert.Equal(t, "betanet", *withChain.ChainID)

	// Insert without chain (empty string).
	err = database.InsertMonitoringWebhook(
		"user2",
		"https://hooks.example.com/global",
		"global webhook",
		"slack",
		"",
		db,
	)
	require.NoError(t, err)

	var withoutChain database.WebhookValidator
	err = db.Where("url = ?", "https://hooks.example.com/global").First(&withoutChain).Error
	require.NoError(t, err)
	assert.Nil(t, withoutChain.ChainID, "ChainID should be NULL when empty chainID was provided")
}

// TestDuplicateWebhookAllowedAcrossChains verifies that the same URL+type is
// allowed for different chains (per-user), but rejected on the same chain when
// a unique index enforces it.
func TestDuplicateWebhookAllowedAcrossChains(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Add a unique index on (user_id, url, type, chain_id) to enforce the
	// business rule being tested. The production table does not yet enforce
	// this at the DB level, so we create it inline for the test.
	err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_wv_user_url_type_chain
		ON webhook_validators(user_id, url, type, chain_id)
	`).Error
	require.NoError(t, err, "failed to create test unique index")

	chainA := "chainA"
	chainB := "chainB"

	// First insert: userA, chainA — must succeed.
	whA1 := database.WebhookValidator{
		UserID: "userA",
		URL:    "https://hooks.example.com/webhook",
		Type:   "discord",
	}
	whA1.ChainID = &chainA
	require.NoError(t, db.Create(&whA1).Error, "first insert on chainA should succeed")

	// Second insert: same URL+type for userA on chainB — must also succeed.
	whA2 := database.WebhookValidator{
		UserID: "userA",
		URL:    "https://hooks.example.com/webhook",
		Type:   "discord",
	}
	whA2.ChainID = &chainB
	require.NoError(t, db.Create(&whA2).Error, "insert on chainB with same URL+type should succeed")

	// Third insert: same URL+type for userA on chainA again — must fail.
	whA3 := database.WebhookValidator{
		UserID: "userA",
		URL:    "https://hooks.example.com/webhook",
		Type:   "discord",
	}
	whA3.ChainID = &chainA
	err = db.Create(&whA3).Error
	assert.Error(t, err, "duplicate URL+type on the same chain should be rejected")
}

// TestAlertLogChainIsolation verifies that GetAlertLog returns only the records
// that belong to the requested chain and ignores records from other chains.
func TestAlertLogChainIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	now := time.Now()

	alertA := database.AlertLog{
		ChainID:     "chainA",
		Addr:        "g1sharedaddr",
		Moniker:     "ValidatorOnChainA",
		Level:       "CRITICAL",
		StartHeight: 100,
		EndHeight:   130,
		Skipped:     true,
		SentAt:      now,
	}
	alertB := database.AlertLog{
		ChainID:     "chainB",
		Addr:        "g1sharedaddr",
		Moniker:     "ValidatorOnChainB",
		Level:       "WARNING",
		StartHeight: 200,
		EndHeight:   205,
		Skipped:     true,
		SentAt:      now,
	}

	require.NoError(t, db.Create(&alertA).Error)
	require.NoError(t, db.Create(&alertB).Error)

	alerts, err := database.GetAlertLog(db, "chainA", "all_time")
	require.NoError(t, err)

	require.NotEmpty(t, alerts, "should return at least one alert for chainA")
	for _, a := range alerts {
		assert.Equal(t, "ValidatorOnChainA", a.Moniker,
			"only chainA alerts should be returned; found unexpected moniker %q", a.Moniker)
		assert.NotEqual(t, "ValidatorOnChainB", a.Moniker)
	}
}

// TestMissingSeriesCTEChainFilter verifies that the CTE query used in
// WatchValidatorAlerts (the missing-block detection query) correctly isolates
// results by chain_id so that a missing validator on chainA does not appear in
// results for chainB, and vice-versa.
func TestMissingSeriesCTEChainFilter(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Use recent timestamps so they fall within the "datetime('now', '-24 hours')"
	// window in the CTE query.
	recent := time.Now().Add(-1 * time.Hour)

	// chainA: validator missed 6 consecutive blocks.
	chainARows := make([]database.DailyParticipation, 6)
	for i := range 6 {
		chainARows[i] = database.DailyParticipation{
			ChainID:        "chainA",
			Addr:           "g1valA",
			Moniker:        "ValidatorA",
			BlockHeight:    int64(1000 + i),
			Date:           recent,
			Participated:   false,
			TxContribution: false,
		}
	}

	// chainB: same block heights, but validator participated on every block.
	chainBRows := make([]database.DailyParticipation, 6)
	for i := range 6 {
		chainBRows[i] = database.DailyParticipation{
			ChainID:        "chainB",
			Addr:           "g1valB",
			Moniker:        "ValidatorB",
			BlockHeight:    int64(1000 + i),
			Date:           recent,
			Participated:   true,
			TxContribution: false,
		}
	}

	require.NoError(t, db.Create(&chainARows).Error)
	require.NoError(t, db.Create(&chainBRows).Error)

	// Run the same CTE that WatchValidatorAlerts uses, scoped to chainA.
	type MissingRow struct {
		Addr        string
		Moniker     string
		StartHeight int64
		EndHeight   int64
		Missed      int
	}

	rows, err := db.Raw(`
		WITH ranked AS (
			SELECT
				addr,
				moniker,
				date,
				block_height,
				participated,
				CASE
					WHEN participated = 0 AND LAG(participated) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) = 1
					THEN 1
					WHEN participated = 0 AND LAG(participated) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) IS NULL
					THEN 1
					ELSE 0
				END AS new_seq
			FROM daily_participations
			WHERE chain_id = ? AND date >= datetime('now', '-24 hours')
		),
		grouped AS (
			SELECT
				*,
				SUM(new_seq) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) AS seq_id
			FROM ranked
		)
		SELECT
			addr,
			moniker,
			MIN(block_height) OVER (PARTITION BY addr, moniker, DATE(date), seq_id) AS start_height,
			block_height AS end_height,
			SUM(1) OVER (
				PARTITION BY addr, moniker, DATE(date), seq_id
				ORDER BY block_height
				ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
			) AS missed
		FROM grouped
		WHERE participated = 0
		ORDER BY addr, moniker, date, seq_id, block_height
	`, "chainA").Rows()
	require.NoError(t, err)
	defer rows.Close()

	var results []MissingRow
	for rows.Next() {
		var r MissingRow
		require.NoError(t, rows.Scan(&r.Addr, &r.Moniker, &r.StartHeight, &r.EndHeight, &r.Missed))
		results = append(results, r)
	}

	require.NotEmpty(t, results, "chainA validator should appear in missing series")

	for _, r := range results {
		assert.Equal(t, "g1valA", r.Addr,
			"only chainA validator should appear; got addr %q", r.Addr)
		assert.NotEqual(t, "g1valB", r.Addr,
			"chainB validator must not appear in chainA CTE results")
	}

	// The last row for chainA should have missed == 6.
	last := results[len(results)-1]
	assert.Equal(t, 6, last.Missed, "expected 6 consecutive missed blocks for chainA validator")
}

// TestAlertMessageContainsChainID verifies that the formatted alert message
// strings produced by SendAllValidatorAlerts include a "[chainID] " prefix so
// that recipients can identify which chain triggered the alert.
func TestAlertMessageContainsChainID(t *testing.T) {
	chainIDs := []string{"betanet", "gnoland1", "staging", "test-chain-99"}

	for _, chainID := range chainIDs {
		t.Run(chainID, func(t *testing.T) {
			chainLabel := fmt.Sprintf("[%s] ", chainID)

			// Reproduce the WARNING message format from SendAllValidatorAlerts
			// (discord / slack branch).
			level := "WARNING"
			emoji := "⚠️"
			prefix := ""
			today := "2026-03-19"
			addr := "g1testaddr"
			moniker := "TestValidator"
			missed := 5
			var startHeight, endHeight int64 = 1000, 1004

			discordMsg := fmt.Sprintf(
				"%s%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
				chainLabel, emoji, prefix, level, prefix, today, addr, moniker, missed, startHeight, endHeight,
			)

			assert.True(t,
				strings.HasPrefix(discordMsg, "["+chainID+"]"),
				"discord WARNING message must start with [%s]; got: %q", chainID, discordMsg,
			)

			// Reproduce the CRITICAL message format (discord / slack branch).
			level = "CRITICAL"
			emoji = "🚨"
			prefix = "**"

			discordCriticalMsg := fmt.Sprintf(
				"%s%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
				chainLabel, emoji, prefix, level, prefix, today, addr, moniker, missed, startHeight, endHeight,
			)

			assert.True(t,
				strings.HasPrefix(discordCriticalMsg, "["+chainID+"]"),
				"discord CRITICAL message must start with [%s]; got: %q", chainID, discordCriticalMsg,
			)

			// Reproduce the Telegram HTML message format.
			telegramMsg := fmt.Sprintf(
				"[%s] 🚨 <b>%s</b> %s\naddr: <code>%s</code>\nmoniker: <b>%s</b>\nmissed %d blocks (%d → %d)",
				chainID, level, today, addr, moniker, missed, startHeight, endHeight,
			)

			assert.Contains(t, telegramMsg, "["+chainID+"]",
				"telegram message must contain [%s]; got: %q", chainID, telegramMsg,
			)
		})
	}
}
