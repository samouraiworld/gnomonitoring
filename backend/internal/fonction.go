package internal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v2"
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

func SendAllValidatorAlerts(message, level, addr, moniker string, db *sql.DB) error {
	// 1. get all webhooks_validator
	query := `
		SELECT user_id, url, type 
		FROM webhooks_validator;
	`

	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query webhooks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var userID, url, typ string
		if err := rows.Scan(&userID, &url, &typ); err != nil {
			return fmt.Errorf("failed to scan webhook row: %w", err)
		}

		// 2. Check if we should send this alert. On (user_id + addr + level + url)
		var lastSent time.Time
		err = db.QueryRow(`
			SELECT sent_at FROM alert_log 
			WHERE user_id = ? AND addr = ? AND level = ? AND url = ?`, userID, addr, level, url).Scan(&lastSent)

		if err == nil && time.Since(lastSent) < 500*time.Minute {
			log.Printf("⏱️ Skipping alert for %s (%s, %s): recently sent", moniker, userID, url)
			continue
		}

		fullMsg := message

		// 3. Add mention tag if level is critical
		if level == "** CRITICAL **" {
			mentionQuery := `
				SELECT namecontact, mention_tag 
				FROM alert_contacts 
				WHERE user_id = ? AND moniker = ?;
			`
			mentionRows, err := db.Query(mentionQuery, userID, moniker)
			if err != nil {
				return fmt.Errorf("failed to query alert_contacts: %w", err)
			}
			defer mentionRows.Close()

			var mentions string
			for mentionRows.Next() {
				var name, tag sql.NullString
				if err := mentionRows.Scan(&name, &tag); err != nil {
					return fmt.Errorf("failed to scan contact mention: %w", err)
				}
				if tag.Valid {
					mentions += tag.String + " "
				} else {
					mentions += "@" + name.String + " "
				}
			}

			if mentions != "" {
				fullMsg += "\n" + mentions
			}
		}

		// 4. Send if discord or slack
		switch typ {
		case "discord":
			err = SendDiscordAlert(fullMsg, url)
		case "slack":
			err = SendSlackAlert(fullMsg, url)
		default:
			continue
		}

		if err != nil {
			log.Printf("❌ Failed to send alert to %s (%s): %v", url, typ, err)
			continue
		}

		// 5. Insert in the lert_log with URL
		_, err = db.Exec(`
			INSERT INTO alert_log (user_id, addr, moniker, level, url, sent_at)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(user_id, addr, level, url) DO UPDATE SET sent_at = excluded.sent_at;
		`, userID, addr, moniker, level, url)
		if err != nil {
			log.Printf("failed to insert alert_log for %v (%s): %v", url, typ, err)
			return fmt.Errorf("failed to insert alert log for %v (%s): %w", url, typ, err)
		}
	}

	return nil
}
func SendUserReportAlert(userID, msg string, db *sql.DB) error {
	query := `
		SELECT url, type 
		FROM webhooks_validator
		WHERE user_id = ?;
	`

	rows, err := db.Query(query, userID)
	if err != nil {
		return fmt.Errorf("failed to query webhooks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var url, typ string
		if err := rows.Scan(&url, &typ); err != nil {
			return fmt.Errorf("failed to scan webhook row: %w", err)
		}

		switch typ {
		case "discord":
			if err := SendDiscordAlert(msg, url); err != nil {
				return fmt.Errorf("failed to send Discord alert: %w", err)
			}
		case "slack":
			if err := SendSlackAlert(msg, url); err != nil {
				return fmt.Errorf("failed to send Slack alert: %w", err)
			}
		default:
			// Type inconnu → ignore
			continue
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating over rows: %w", err)
	}

	return nil
}
