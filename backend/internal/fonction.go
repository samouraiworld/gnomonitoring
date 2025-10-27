package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/telegram"
	"gopkg.in/yaml.v2"
	"gorm.io/gorm"
)

type config struct {
	BackendPort            string `yaml:"backend_port"`
	AllowOrigin            string `yaml:"allow_origin"`
	RPCEndpoint            string `yaml:"rpc_endpoint"`
	MetricsPort            int    `yaml:"metrics_port"`
	Gnoweb                 string `yaml:"gnoweb"`
	Graphql                string `yaml:"graphql"`
	ClerkSecretKey         string `yaml:"clerk_secret_key"`
	DevMode                bool   `yaml:"dev_mode"`
	TokenTelegramValidator string `yaml:"token_telegram_validator"`
	TokenTelegramGovdao    string `yaml:"token_telegram_govdao"`
}

var Config config

// Load config.yaml
func LoadConfig() {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	err = yaml.Unmarshal(data, &Config)
	if err != nil {
		log.Fatalf("Error parsing config file: %v", err)
	}

	log.Printf("DevMode value: %v", Config.DevMode)
}
func SendDiscordAlert(msg string, webhookURL string) error {
	payload := map[string]string{"content": msg}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error sending Discord alert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook HTTP status: %d", resp.StatusCode)
	}
	return nil
}

func SendSlackAlert(msg string, webhookURL string) error {

	payload := map[string]string{"text": msg}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Erreur sending Slack alert : %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Slack webhook HTTP %d", resp.StatusCode)
	}
	return nil
}
func SendAllValidatorAlerts(missed int, today, level, addr, moniker string, start_height, end_height int64, db *gorm.DB) error {
	type Webhook struct {
		UserID string
		URL    string
		Type   string
		ID     int
	}
	var fullMsg string
	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}
	for _, wh := range webhooks {

		//================== Build msg ===============

		var emoji, prefix string
		switch wh.Type {
		case "discord":
			if level == "CRITICAL" {
				emoji = "🚨"
				prefix = "**"

				type tag struct{ MentionTag string }
				var res []tag
				err := db.Model(&database.AlertContact{}).
					Select("mention_tag").
					Where("user_id = ? AND moniker = ? AND id_webhook = ?", wh.UserID, moniker, wh.ID).
					Find(&res).Error

				fullMsg = fmt.Sprintf(
					"%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
					emoji, prefix, level, prefix, today, addr, moniker, missed, start_height, end_height,
				)
				if err != nil {
					return fmt.Errorf("failed to fetch mentions: %w", err)
				}
				for _, r := range res {
					fullMsg += "\n<@" + r.MentionTag + ">"
				}
			}
			if level == "WARNING" {
				emoji = "⚠️"
				prefix = ""
				fullMsg = fmt.Sprintf(
					"%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
					emoji, prefix, level, prefix, today, addr, moniker, missed, start_height, end_height,
				)

			}
			log.Println(fullMsg)
			sendErr := SendDiscordAlert(fullMsg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		case "slack":
			if level == "CRITICAL" {
				emoji = "🚨"
				prefix = "*"

				type tag struct{ MentionTag string }
				log.Printf("TYPE SLACK")
				var res []tag
				err := db.Model(&database.AlertContact{}).
					Select("mention_tag").
					Where("user_id = ? AND moniker = ? AND id_webhook = ?", wh.UserID, moniker, wh.ID).
					Find(&res).Error
				if err != nil {
					return fmt.Errorf("failed to fetch mentions: %w", err)
				}
				fullMsg = fmt.Sprintf(
					"%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
					emoji, prefix, level, prefix, today, addr, moniker, missed, start_height, end_height,
				)
				for _, r := range res {

					fullMsg += "\n <@" + r.MentionTag + ">"
				}
				//log.Println(fullMsg)
				if level == "WARNING" {
					emoji = "⚠️"
					prefix = ""
					fullMsg = fmt.Sprintf(
						"%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
						emoji, prefix, level, prefix, today, addr, moniker, missed, start_height, end_height,
					)

				}
			}
			sendErr := SendSlackAlert(fullMsg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		default:
			continue
		}

	}
	// ======================================== TELEGRAM
	if level == "CRITICAL" {
		emoji := "🚨"
		fullMsg = fmt.Sprintf(
			"%s <b>%s</b> %s\n"+
				"addr: <code>%s</code>\n"+
				"moniker: <b>%s</b>\n"+
				"missed %d blocks (%d → %d)",
			emoji,
			html.EscapeString(level),
			html.EscapeString(today),
			html.EscapeString(addr),
			html.EscapeString(moniker),
			missed, start_height, end_height,
		)
	}

	if level == "WARNING" {
		emoji := "⚠️"
		fullMsg = fmt.Sprintf(
			"%s <b>%s</b> %s\n"+
				"addr: <code>%s</code>\n"+
				"moniker: <b>%s</b>\n"+
				"missed %d blocks (%d → %d)",
			emoji,
			html.EscapeString(level),
			html.EscapeString(today),
			html.EscapeString(addr),
			html.EscapeString(moniker),
			missed, start_height, end_height,
		)
	}

	telegram.MsgTelegram(fullMsg, Config.TokenTelegramValidator, "validator", db)

	return nil
}

func SendUserReportAlert(userID, msg string, db *gorm.DB) error {
	type Webhook struct {
		URL  string
		Type string
	}

	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).
		Select("url", "type").
		Where("user_id = ?", userID).
		Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks for user %s: %w", userID, err)
	}

	for _, wh := range webhooks {
		switch wh.Type {
		case "discord":
			if err := SendDiscordAlert(msg, wh.URL); err != nil {
				return fmt.Errorf("failed to send Discord alert: %w", err)
			}
		case "slack":
			if err := SendSlackAlert(msg, wh.URL); err != nil {
				return fmt.Errorf("failed to send Slack alert: %w", err)
			}
		default:
			log.Printf("⚠️ Unknown webhook type for user %s: %s", userID, wh.Type)
		}
	}

	return nil
}

func SendInfoValidator(msg string, level string, db *gorm.DB) error {
	type Webhook struct {
		UserID string
		URL    string
		Type   string
		ID     int
	}

	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}
	for _, wh := range webhooks {
		switch wh.Type {
		case "discord":
			sendErr := SendDiscordAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		case "slack":
			sendErr := SendSlackAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		}
		// database.InsertAlertlog(db, wh.addr, "moniker", level, wh.URL, 0, 0, true, msg, time.Now())

	}

	telegram.MsgTelegram(msg, Config.TokenTelegramValidator, "validator", db)

	return nil
}

func MultiSendReportGovdao(id int, title, urlgnoweb, urltx string, db *gorm.DB) error {

	type Webhook struct {
		UserID        string
		URL           string
		Type          string
		LastCheckedID int
	}
	var webhooks []Webhook
	if err := db.Model(&database.WebhookGovDAO{}).Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}
	for _, wh := range webhooks {

		SendReportGovdao(id, title, urlgnoweb, urltx, wh.Type, wh.URL)

	}
	// build msg for telegram and senb at all chatid
	msg := telegram.FormatTelegramMsg(id, title, urlgnoweb, urltx)
	err := telegram.MsgTelegram(msg, Config.TokenTelegramGovdao, "govdao", db)
	if err != nil {
		log.Printf("error send govdao telegram  %s", err)
	}

	return nil

}

func SendReportGovdao(id int, title, urlgnoweb, urltx, typew string, urlwebhook string) error {

	switch typew {
	case "discord":

		msg := fmt.Sprintf("--- \n"+
			"🗳️ ** New Proposal N° %d: %s ** -  \n"+
			"🔗source: %s \n"+
			"🗒️Tx: %s"+
			"🖐️ Interact & Vote: https://gnolove.world/govdao/proposal/%d",
			id, title, urlgnoweb, urltx, id)
		log.Println(msg)
		sendErr := SendDiscordAlert(msg, urlwebhook)
		if sendErr != nil {
			log.Printf("❌ Failed to send alert to %s (%s): %v", urlwebhook, typew, sendErr)

		}

	case "slack":
		msg := fmt.Sprintf("--- \n"+
			"🗳️ * New Proposal N° %d: %s * -  \n"+
			"🔗source: %s \n"+
			"🗒️Tx: %s"+
			"🖐️ Interact & Vote: https://gnolove.world/govdao/proposal/%d",
			id, title, urlgnoweb, urltx, id)

		sendErr := SendSlackAlert(msg, urlwebhook)
		if sendErr != nil {
			log.Printf("❌ Failed to send alert to %s (%s): %v", urlwebhook, typew, sendErr)

		}

	}

	return nil

}

func SendInfoGovdao(msg string, db *gorm.DB) error {
	type Webhook struct {
		UserID        string
		URL           string
		Type          string
		LastCheckedID int
	}

	var webhooks []Webhook
	if err := db.Model(&database.WebhookGovDAO{}).Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}
	for _, wh := range webhooks {
		switch wh.Type {
		case "discord":
			sendErr := SendDiscordAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		case "slack":
			sendErr := SendSlackAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		}
		// database.InsertAlertlog(db, wh.addr, "moniker", level, wh.URL, 0, 0, true, msg, time.Now())

	}
	return nil
}
