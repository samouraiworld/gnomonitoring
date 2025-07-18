package internal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

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
func SendDiscordAlert(msg string, webhookURL string) {

	payload := map[string]string{"content": msg}
	body, _ := json.Marshal(payload)

	http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
}

func SendSlackAlert(msg string, webhookURL string) {

	payload := map[string]string{"text": msg}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Erreur envoi Slack : %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Slack webhook HTTP %d", resp.StatusCode)
	}
}

// var SendDiscordAlertValidator = func(message string, db *sql.DB) error {

// 	hooks, err := ListMonitoringWebhooks(db, user_id) // ou cache local
// 	if err != nil {
// 		return fmt.Errorf("error retrieving hooks: %w", err)
// 	}
// 	for _, hook := range hooks {
// 		if hook.Type != "discord" {
// 			continue
// 		}
// 		payload := map[string]string{"content": message}
// 		body, _ := json.Marshal(payload)

//			resp, err := http.Post(hook.URL, "application/json", bytes.NewBuffer(body))
//			if err != nil {
//				log.Printf("Error sending to %s: %v", hook.URL, err)
//				continue
//			}
//			resp.Body.Close()
//		}
//		return nil
//	}
func SendDiscordAlertValidator(userID string, message string, db *sql.DB) error {
	hooks, err := ListMonitoringWebhooks(db, userID)
	if err != nil {
		return fmt.Errorf("error retrieving hooks: %w", err)
	}

	contacts, err := ListAlertContacts(db, userID)
	if err != nil {
		log.Printf("Failed to fetch contacts for user %s: %v", userID, err)
	}

	// Ajouter les mentions au message
	for _, c := range contacts {
		if c.MENTIONTAG != "" {
			message += "\n" + c.MENTIONTAG
		}
	}

	for _, hook := range hooks {
		if hook.Type != "discord" {
			continue
		}
		payload := map[string]string{"content": message}
		body, _ := json.Marshal(payload)

		resp, err := http.Post(hook.URL, "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.Printf("Error sending to %s: %v", hook.URL, err)
			continue
		}
		resp.Body.Close()
	}

	return nil
}

var SendSlackAlertValidator = func(user_id, message string, db *sql.DB) error {
	//func SendSlackAlertValidator(message string, db *sql.DB) error {
	hooks, err := ListMonitoringWebhooks(db, user_id)
	if err != nil {
		log.Printf("Error retrieving hooks: %v", err)
		return fmt.Errorf("error retrieving hooks: %w", err)

	}

	for _, hook := range hooks {
		if hook.Type != "slack" {
			continue
		}

		payload := map[string]string{"text": message}
		body, _ := json.Marshal(payload)

		resp, err := http.Post(hook.URL, "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.Printf("Error sending to Slack webhook %s: %v", hook.URL, err)
			continue
		}
		resp.Body.Close()
	}
	return nil
}

func SendAllValidatorAlerts(message string, db *sql.DB) error {
	userIDs, err := GetAllUserIDsWithMonitoringWebhooks(db)
	if err != nil {
		return fmt.Errorf("failed to get user_ids: %w", err)
	}

	for _, userID := range userIDs {
		err := SendDiscordAlertValidator(userID, message, db)
		if err != nil {
			log.Printf("Failed to send alert to user %s: %v", userID, err)
		}
	}
	return nil
}
func GetAllUserIDsWithMonitoringWebhooks(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT user_id FROM webhooks_validator`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}
