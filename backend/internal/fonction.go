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
	// 1. Récupérer tous les webhooks_validator avec user_id
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
		// 3. Vérifier si une alerte a déjà été envoyée récemment (ex: 30 min)
		var lastSent time.Time
		err = db.QueryRow(`
			SELECT sent_at FROM alert_log 
			WHERE user_id = ? AND addr = ? AND level = ?`, userID, addr, level).Scan(&lastSent)

		if err == nil && time.Since(lastSent) < 30*time.Minute {
			log.Printf("⏱️ Skipping alert for %s (%s): recently sent", moniker, userID)
			continue
		}

		fullMsg := message

		switch level {
		case "** CRITICAL **":
			// Ajouter les mentions si le niveau est critique

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

		case "warning", "info":
			// Ne rien ajouter au message
			// Tu peux éventuellement logger si besoin
		default:
			log.Printf("Unknown alert level: %s", level)
		}

		// 3. Envoyer via la bonne méthode
		switch typ {
		case "discord":
			err = SendDiscordAlert(fullMsg, url)
		case "slack":
			err = SendSlackAlert(fullMsg, url)
		default:
			continue
		}
		// 5. Insérer ou mettre à jour alert_log
		_, err = db.Exec(`
			INSERT INTO alert_log (user_id, addr, moniker, level, sent_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id, addr, level) DO UPDATE SET sent_at = excluded.sent_at;
		`, userID, addr, moniker, level)
		if err != nil {
			log.Printf("failed to insert alert to %v (%s): %v", url, typ, err)
			return fmt.Errorf("failed to insert alert to %v (%s): %w", url, typ, err)

		}
	}

	return nil
}
