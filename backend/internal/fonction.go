package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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
		log.Printf("Erreur sending Slack alert : %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Slack webhook HTTP %d", resp.StatusCode)
	}
	return nil
}
func SendAllValidatorAlerts(missed int, today, level, addr, moniker string, start_height, end_height int, db *gorm.DB) error {
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
		// 2. Check if alert was recently sent
		var count int64
		err := db.Raw(`
		SELECT COUNT(*) FROM alert_logs 
		WHERE user_id = ? AND addr = ? AND level = ? AND url = ?
		AND start_height= ?
		AND skipped = 1	
		`, wh.UserID, addr, level, wh.URL, start_height).Scan(&count).Error

		if err != nil {
			log.Printf("❌ DB error checking alert_logs: %v", err)
			continue
		}

		if count > 0 {
			log.Printf("⏱️ Skipping alert for %s (%s, %s): already sent", moniker, wh.UserID, wh.URL)
			continue
		}
		//Check if in the daily participate table for an addr the column participate = 1 and the value and block_heigt > end height
		// Then send the alert, otherwise no
		var countparticipated int
		err = db.Raw(`
			SELECT sum(participated) FROM daily_participations
			WHERE addr = ? AND block_height= (?-1)
			`, addr, start_height).Scan(&countparticipated).Error
		if err != nil {
			log.Printf("❌ DB error checking count participated: %v", err)
			continue
		}
		log.Println("countparticipated", countparticipated)
		if countparticipated == 0 {
			log.Printf("⏱️ Skipping alert for %s (%s, %s): limit reached", moniker, wh.UserID, wh.URL)
			database.InsertAlertlog(db, wh.UserID, addr, moniker, level, wh.URL, start_height, end_height, false, "", time.Now())

			continue
		}

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

		database.InsertAlertlog(db, wh.UserID, addr, moniker, level, wh.URL, start_height, end_height, true, fullMsg, time.Now())

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

func SendResolveAlerts(db *gorm.DB) {
	log.Println("******************start go routine resolve**********************")

	type LastAlert struct {
		UserID      string
		Addr        string
		URL         string
		Moniker     string
		StartHeight int
		EndHeight   int
	}

	var alerts []LastAlert

	err := db.Raw(`
		SELECT user_id, addr, url, moniker, end_height,start_height
		FROM alert_logs
		WHERE level IN ('CRITICAL', 'WARNING')
		
	`).Scan(&alerts).Error
	if err != nil {
		log.Printf("❌ Error fetching last alerts: %v", err)
		return
	}

	for _, a := range alerts {
		// Check if resolve at send
		var count int64
		err := db.Raw(`
		SELECT COUNT(*) FROM alert_logs 
		WHERE user_id = ? AND addr = ?  AND url = ? and level = "RESOLVED"
		AND start_height= ? AND end_height = ? 	
		`, a.UserID, a.Addr, a.URL, a.StartHeight, a.EndHeight).Scan(&count).Error

		if err != nil {
			log.Printf("❌ DB error checking alert_logs: %v", err)
			continue
		}

		if count > 0 {
			log.Printf("⏱️ Skipping resolve alert for %s (%s, %s): already sent", a.Moniker, a.UserID, a.URL)
			continue
		}

		// check if participation is true after end_heigt+1
		var countparticipated int
		err = db.Raw(`
			SELECT participated FROM daily_participations
			WHERE addr = ? AND block_height= (?+2)
			`, a.Addr, a.EndHeight).Scan(&countparticipated).Error
		if err != nil {
			log.Printf("❌ DB error checking count participated: %v", err)
			continue
		}
		if countparticipated == 0 {
			// log.Printf("Not resolve error")
			continue
		}

		// send RESOLVED
		resolveMsg := fmt.Sprintf("✅ RESOLVED: No more missed blocks for %s (%s)", a.Moniker, a.Addr)
		if strings.Contains(a.URL, "discord.com") {
			SendDiscordAlert(resolveMsg, a.URL)
		} else if strings.Contains(a.URL, "slack.com") {
			SendSlackAlert(resolveMsg, a.URL)
		}
		database.InsertAlertlog(db, a.UserID, a.Addr, a.Moniker, "RESOLVED", a.URL, a.StartHeight, a.EndHeight, false, resolveMsg, time.Now())
		log.Printf("✅ Sent resolve alert for %s", a.Addr)
	}

}

func SendInfoValidateur(msg string, level string, db *gorm.DB) error {
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
		database.InsertAlertlog(db, wh.UserID, "", "moniker", level, wh.URL, 0, 0, true, msg, time.Now())

	}
	return nil
}
