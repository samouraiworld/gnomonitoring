package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gopkg.in/yaml.v2"
	"gorm.io/gorm"
)

type config struct {
	IntervallSecond   int    `yaml:"interval_seconde"`
	BackendPort       string `yaml:"backend_port"`
	AllowOrigin       string `yaml:"allow_origin"`
	RPCEndpoint       string `yaml:"rpc_endpoint"`
	WindowSize        int    `yaml:"windows_size"`
	DailyReportHour   int    `yaml:"daily_report_hour"`
	DailyReportMinute int    `yaml:"daily_report_minute"`
	MetricsPort       int    `yaml:"metrics_port"`
	Gnoweb            string `yaml:"gnoweb"`
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

	log.Printf("Config loaded: %+v", Config)
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
		return fmt.Errorf("Discord webhook HTTP status: %d", resp.StatusCode)
	}
	return nil
}

func SendSlackAlert(msg string, webhookURL string) error {

	payload := map[string]string{"text": msg}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Erreur envoi Slack : %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Slack webhook HTTP %d", resp.StatusCode)
	}
	return nil
}
func SendAllValidatorAlerts(message, level, addr, moniker string, db *gorm.DB) error {
	type Webhook struct {
		UserID string
		URL    string
		Type   string
	}

	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}

	for _, wh := range webhooks {
		// 2. Check if alert was recently sent
		var lastSent time.Time
		err := db.Raw(`
			SELECT sent_at FROM alert_logs 
			WHERE user_id = ? AND addr = ? AND level = ? AND url = ?
		`, wh.UserID, addr, level, wh.URL).Scan(&lastSent).Error

		if err == nil && time.Since(lastSent) < 500*time.Minute {
			log.Printf("⏱️ Skipping alert for %s (%s, %s): recently sent", moniker, wh.UserID, wh.URL)
			continue
		}

		fullMsg := message

		// 3. Mention if CRITICAL
		if level == "CRITICAL" {
			log.Println("EnTREE DANS CRITICAL")
			type tag struct {
				MentionTag string
			}
			var res []tag

			err := db.Model(&database.AlertContact{}).
				Select("mention_tag").
				Where("user_id = ? AND moniker = ?", wh.UserID, moniker).
				Find(&res).Error

			if err != nil {
				return fmt.Errorf("failed to fetch mentions: %w", err)
			}
			for _, r := range res {

				fmt.Println(r.MentionTag)
				fullMsg += "\n" + "<@" + r.MentionTag + ">"
			}
			println("full Message", fullMsg)

		}

		// 4. Envoi
		var sendErr error
		switch wh.Type {
		case "discord":
			sendErr = SendDiscordAlert(fullMsg, wh.URL)
		case "slack":
			sendErr = SendSlackAlert(fullMsg, wh.URL)
		default:
			continue
		}

		if sendErr != nil {
			log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
			continue
		}

		// 5. Insert log
		err = db.Exec(`
			INSERT INTO alert_logs (user_id, addr, moniker, level, url, sent_at)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(user_id, addr, level, url) DO UPDATE SET sent_at = excluded.sent_at
		`, wh.UserID, addr, moniker, level, wh.URL).Error

		if err != nil {
			log.Printf("⚠️ Failed to insert alert log for %s (%s): %v", wh.URL, wh.Type, err)
		}
	}

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
