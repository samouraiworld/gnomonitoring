package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/PuerkitoBio/goquery"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DiscordWebhookURL string `yaml:"discord_webhook_url"`
	IntervallSecond   int    `yaml:"interval_seconde"`
	LastCheckedID     int    `yaml:"last_checked_id"`
}

var config Config

// Charger config.yaml
func loadConfig() {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		log.Fatalf("Error parsing config file: %v", err)
	}

	log.Printf("Config loaded: discord URL %s", config.DiscordWebhookURL)
}

func proposalExists(i int) (bool, string) {
	url := fmt.Sprintf("https://test6.testnets.gno.land/r/gov/dao:%d", i)
	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("Erreur HTTP : %v\n", err)
		return false, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, ""
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		fmt.Printf("Erreur parsing HTML : %v\n", err)
		return true, ""
	}

	moniker := doc.Find("h2[id]").Eq(1).Text()

	fmt.Printf("moniker: %s\n", moniker)
	return true, moniker
}
func main() {
	loadConfig()
	lastChecked := config.LastCheckedID //init valor
	ticker := time.NewTicker(time.Duration(config.IntervallSecond) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			fmt.Printf("🔍 Vérif proposal %d...\n", lastChecked+1)
			exists, moniker := proposalExists(lastChecked + 1)

			if exists {
				config.LastCheckedID = lastChecked + 1
				saveConfig()
				msg := fmt.Sprintf("🗳️ News Proposal %s https://test6.testnets.gno.land/r/gov/dao:%d \n", moniker, lastChecked+1)
				fmt.Printf("🗳️ News Proposal %s : dao:%d\n", moniker, lastChecked+1)
				sendDiscordAlert(msg)
				lastChecked++
			} else {
				fmt.Println("Aucune nouvelle proposition.")
			}
		}
	}
}
func sendDiscordAlert(msg string) {
	webhookURL := config.DiscordWebhookURL

	payload := map[string]string{"content": msg}
	body, _ := json.Marshal(payload)

	http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
}

func saveConfig() {
	data, err := yaml.Marshal(&config)
	if err != nil {
		log.Printf("Erreur lors de la sérialisation de la config : %v", err)
		return
	}

	err = os.WriteFile("config.yaml", data, 0644)
	if err != nil {
		log.Printf("Erreur lors de l’écriture du fichier config.yaml : %v", err)
	}
}
