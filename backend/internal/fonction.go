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

var SendDiscordAlertValidator = func(message string, db *sql.DB) error {
	hooks, err := ListMonitoringWebhooks(db) // ou cache local
	if err != nil {
		return fmt.Errorf("error retrieving hooks: %w", err)
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

var SendSlackAlertValidator = func(message string, db *sql.DB) error {
	//func SendSlackAlertValidator(message string, db *sql.DB) error {
	hooks, err := ListMonitoringWebhooks(db)
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
