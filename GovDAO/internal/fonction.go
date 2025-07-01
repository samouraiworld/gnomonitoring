package internal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"gopkg.in/yaml.v2"
)

func ProposalExists(i int) (bool, string, string) {
	url := fmt.Sprintf("https://test6.testnets.gno.land/r/gov/dao:%d", i)
	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("Erreur HTTP : %v\n", err)
		return false, "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, "", ""
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		fmt.Printf("Erreur parsing HTML : %v\n", err)
		return true, "", ""
	}

	// title := doc.Find("h3[id]").Eq(0).Text()
	title := strings.TrimPrefix(doc.Find("h3[id]").Eq(0).Text(), "Title: ")

	moniker := doc.Find("h2[id]").Eq(1).Text()

	return true, title, moniker
}

type config struct {
	IntervallSecond int `yaml:"interval_seconde"`
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

	//log.Printf("Config loaded: discord URL %s", config.DiscordWebhookURL)
	log.Printf("Config loaded: %+v", Config)
}
func SendDiscordAlert(msg string, webhookURL string) {
	// webhookURL := config.DiscordWebhookURL

	payload := map[string]string{"content": msg}
	body, _ := json.Marshal(payload)

	http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
}

// func SaveConfig() {
// 	data, err := yaml.Marshal(&config)
// 	if err != nil {
// 		log.Printf("Error serializing config : %v", err)
// 		return
// 	}

//		err = os.WriteFile("config.yaml", data, 0644)
//		if err != nil {
//			log.Printf("Error writing config.yaml file : %v", err)
//		}
//	}
func SendSlackAlert(msg string, webhookURL string) {
	// webhookURL := config.SlackWebhookURL

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
func InitDB() *sql.DB {
	db, err := sql.Open("sqlite3", "./webhooks.db")
	if err != nil {
		log.Fatalf("Erreur ouverture DB: %v", err)
	}

	createTable := `
	CREATE TABLE IF NOT EXISTS webhooks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		url TEXT NOT NULL,
		type TEXT NOT NULL, -- "discord" ou "slack"
		last_checked_id INTEGER NOT NULL DEFAULT 0
	);
	`
	_, err = db.Exec(createTable)
	if err != nil {
		log.Fatalf("Erreur création table: %v", err)
	}

	return db
}

type Webhook struct {
	ID            int
	URL           string
	Type          string
	LastCheckedID int
}

func InsertWebhook(url string, wtype string, db *sql.DB) error {
	if wtype != "discord" && wtype != "slack" {
		return fmt.Errorf("Type invalide. Utilise 'discord' ou 'slack'")
	}

	_, err := db.Exec("INSERT OR IGNORE INTO webhooks (url, type, last_checked_id) VALUES (?, ?, 0)", url, wtype)
	return err
}

func Loadwebhooks(db *sql.DB) ([]Webhook, error) {
	rows, err := db.Query("SELECT id, url, type, last_checked_id FROM webhooks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var webhooks []Webhook
	for rows.Next() {
		var w Webhook
		if err := rows.Scan(&w.ID, &w.URL, &w.Type, &w.LastCheckedID); err != nil {
			return nil, err
		}
		webhooks = append(webhooks, w)
	}
	return webhooks, nil
}

func UpdateLastCheckedID(url string, newID int, db *sql.DB) error {
	_, err := db.Exec("UPDATE webhooks SET last_checked_id = ? WHERE url = ?", newID, url)
	return err
}
func SendSingleDiscord(msg, url string) {
	payload := map[string]string{"content": msg}
	body, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewBuffer(body))
}

func SendSingleSlack(msg, url string) {
	payload := map[string]string{"text": msg}
	body, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewBuffer(body))
}

func ListWebhooks(db *sql.DB) ([]Webhook, error) {
	rows, err := db.Query("SELECT id, url, type, last_checked_id FROM webhooks ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Webhook
	for rows.Next() {
		var wh Webhook
		err := rows.Scan(&wh.ID, &wh.URL, &wh.Type, &wh.LastCheckedID)
		if err != nil {
			return nil, err
		}
		list = append(list, wh)
	}
	return list, nil
}
func DeleteWebhook(id int, db *sql.DB) error {
	_, err := db.Exec("DELETE FROM webhooks WHERE id = ?", id)
	return err
}
