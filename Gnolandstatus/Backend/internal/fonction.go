package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v2"
)

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

	log.Printf("Config loaded: RPC=%s \n discord URL %s", Config.RPCEndpoint, Config.DiscordWebhookURL)
}

func Init() {
	prometheus.MustRegister(ValidatorParticipation)
	prometheus.MustRegister(BlockWindowStartHeight)
	prometheus.MustRegister(BlockWindowEndHeight)
}

func SendDiscordAlert(validator string, rate float64, moniker string, startHeight int64, endHeight int64) {
	webhookURL := Config.DiscordWebhookURL

	message := fmt.Sprintf("⚠️ Validator %s (%s) has a participation rate of %.2f%% over the last %d blocks (from block %d to %d).",
		moniker, validator, rate, Config.WindowSize, startHeight, endHeight)

	payload := map[string]string{"content": message}
	body, _ := json.Marshal(payload)

	http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
}

func InitMonikerMap() {
	resp, err := http.Get("https://test6.api.onbloc.xyz/v1/blocks?limit=40")
	if err != nil {
		log.Fatalf("Error retrieving blocks: %v", err)
	}
	defer resp.Body.Close()

	// Read the entire body once
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Error reading response: %v", err)
	}

	// Structure pour parser le JSON
	type Block struct {
		BlockProposer      string `json:"blockProposer"`
		BlockProposerLabel string `json:"blockProposerLabel"`
	}

	type Data struct {
		Items []Block `json:"items"`
	}

	type BlocksResponse struct {
		Data Data `json:"data"`
	}

	var blocksResp BlocksResponse
	if err := json.Unmarshal(body, &blocksResp); err != nil {
		log.Fatalf("Error decode JSON : %v", err)
	}
	MonikerMutex.Lock()
	defer MonikerMutex.Unlock()

	MonikerMap = make(map[string]string)

	// Display each pair blockProposer + blockProposerLabel
	for _, block := range blocksResp.Data.Items {
		MonikerMap[block.BlockProposer] = block.BlockProposerLabel

	}
	VerifyValidatorCount()
}

func VerifyValidatorCount() {
	resp, err := http.Get("https://test6.api.onbloc.xyz/v1/stats/summary/accounts")
	if err != nil {
		log.Printf("Error retrieving account summary : %v", err)
		return
	}
	defer resp.Body.Close()

	var summaryResp struct {
		Data struct {
			Data struct {
				Validators int `json:"validators"`
			} `json:"data"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&summaryResp); err != nil {
		log.Printf("Error decode JSON summary : %v", err)
		return
	}

	expected := summaryResp.Data.Data.Validators
	actual := len(MonikerMap)
	log.Printf("Number of validators in recovered blocks: %d / %d expected\n", actual, expected)

	if actual != expected {
		message := fmt.Sprintf("⚠️ Warning: only %d validators recovered out of %d expected!", actual, expected)
		log.Printf(message)
		InitMonikerMap()
		// sendDiscordAlert(message)

	}
}
func SendDailyStats() {
	MonikerMutex.RLock()
	defer MonikerMutex.RUnlock()

	start := BlockWindow[0].Height
	end := BlockWindow[len(BlockWindow)-1].Height

	var buffer bytes.Buffer
	//buffer.WriteString("📊 **Daily participation summary** :\n\n")
	buffer.WriteString(fmt.Sprintf("📊 **Daily participation summary** (Blocks %d → %d) :\n\n", start, end))

	for val, rate := range ParticipationRate {
		moniker := MonikerMap[val]
		if moniker == "" {
			moniker = "inconnu"
		}

		emoji := "🟢"
		if rate < 95.0 {
			emoji = "🔴"
		}
		buffer.WriteString(fmt.Sprintf("  %s Validator : %s addr: (%s) rate : %.2f%%\n", emoji, moniker, val, rate))
	}

	payload := map[string]string{
		"content": buffer.String(),
	}
	body, _ := json.Marshal(payload)

	_, err := http.Post(Config.DiscordWebhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Error sending daily stats on Discord: : %v", err)
	}
}
