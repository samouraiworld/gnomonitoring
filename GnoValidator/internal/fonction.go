package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

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

	log.Printf("Config loaded: RPC=%s \n"+
		"discord URL %s \n"+
		"WindowsSize=%d	\n"+
		"DailyReportHour= %d\n"+
		"DailyReportMinute= %d \n"+
		"MetricsPort= %d \n", Config.RPCEndpoint, Config.DiscordWebhookURL, Config.WindowSize, Config.DailyReportHour, Config.DailyReportMinute, Config.MetricsPort)
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
	// 1. Récupérer la liste des validateurs depuis Gno RPC
	url := fmt.Sprintf("%s/validators", strings.TrimRight(Config.RPCEndpoint, "/"))
	resp, err := http.Get(url)
	if err != nil {
		log.Fatalf("Error retrieving validators: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Error reading validator response: %v", err)
	}

	type Validator struct {
		Address string `json:"address"`
	}
	type ValidatorsResponse struct {
		Result struct {
			Validators []Validator `json:"validators"`
		} `json:"result"`
	}

	var validatorsResp ValidatorsResponse
	if err := json.Unmarshal(body, &validatorsResp); err != nil {
		log.Fatalf("Error decoding validator JSON: %v", err)
	}

	// 2. Récupérer les monikers depuis onbloc.xyz
	blockResp, err := http.Get("https://test6.api.onbloc.xyz/v1/blocks?limit=40")
	if err != nil {
		log.Fatalf("Error retrieving block data: %v", err)
	}
	defer blockResp.Body.Close()

	blockBody, err := io.ReadAll(blockResp.Body)
	if err != nil {
		log.Fatalf("Error reading block response: %v", err)
	}

	type Block struct {
		BlockProposer      string `json:"blockProposer"`
		BlockProposerLabel string `json:"blockProposerLabel"`
	}
	type BlockData struct {
		Items []Block `json:"items"`
	}
	type BlocksResponse struct {
		Data BlockData `json:"data"`
	}

	var blocksResp BlocksResponse
	if err := json.Unmarshal(blockBody, &blocksResp); err != nil {
		log.Fatalf("Error decoding block JSON: %v", err)
	}

	// 3. Mapper adresse → moniker
	MonikerMutex.Lock()
	defer MonikerMutex.Unlock()

	MonikerMap = make(map[string]string)

	for _, val := range validatorsResp.Result.Validators {
		moniker := "inconnu"
		for _, b := range blocksResp.Data.Items {
			if b.BlockProposer == val.Address {
				moniker = b.BlockProposerLabel
				break
			}
		}
		MonikerMap[val.Address] = moniker
	}

	log.Printf("✅ MonikerMap initialized: %d validators\n", len(MonikerMap))
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
	if Config.DiscordWebhookURL == "" {
		log.Println("❌ Discord webhook is empty — skipping alert")
		return
	}

	resp, err := http.Post(Config.DiscordWebhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Error sending daily stats on Discord: : %v", err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("✅ Discord response: %s — HTTP %d", string(respBody), resp.StatusCode)
}
