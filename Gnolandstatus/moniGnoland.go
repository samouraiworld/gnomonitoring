package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// Config Yaml
type Config struct {
	RPCEndpoint       string `yaml:"rpc_endpoint"`
	DiscordWebhookURL string `yaml:"discord_webhook_url"`
	WindowSize        int    `yaml:"windows_size"`
	DailyReportHour   int    `yaml:"daily_report_hour"`
	DailyReportMinute int    `yaml:"daily_report_minute"`
}

var testAlert = flag.Bool("test-alert", false, "Send alert test for Discord")

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

	log.Printf("Config loaded: RPC=%s \n discord URL %s", config.RPCEndpoint, config.DiscordWebhookURL)
}

type BlockParticipation struct {
	Height     int64
	Validators map[string]bool
}

var (
	blockWindow []BlockParticipation
	//windowSize        = 100
	participationRate = make(map[string]float64)
	lastAlertSent     = make(map[string]int64) // to avoid spamming
	monikerMap        = make(map[string]string)
)

// prometheus var
var validatorParticipation = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gnoland_validator_participation_rate",
		Help: "Validator participation rate (%) over the sliding window",
	},
	[]string{"validator_address", "moniker"},
)

// prometheus var start and end block analyse
var (
	blockWindowStartHeight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gnoland_block_window_start_height",
			Help: "Start height of the current block window",
		},
	)
	blockWindowEndHeight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gnoland_block_window_end_height",
			Help: "End height of the current block window",
		},
	)
)

func init() {
	prometheus.MustRegister(validatorParticipation)
	prometheus.MustRegister(blockWindowStartHeight)
	prometheus.MustRegister(blockWindowEndHeight)
}

var monikerMutex sync.RWMutex

func main() {
	flag.Parse()

	loadConfig()
	initMonikerMap()

	if *testAlert {
		sendDiscordAlert("g1test123456", 42.0, "🧪TEST Moniker", 200, 300)
		return
	}

	rpcClient, err := rpcclient.NewHTTPClient(config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}

	client := gnoclient.Client{RPCClient: rpcClient}

	// Initializing the window with the latest blocks

	latestHeight, err := client.LatestBlockHeight()
	if err != nil {
		log.Fatalf("Error retrieving last height: %v", err)
	}

	startHeight := latestHeight - int64(config.WindowSize) + 1
	if startHeight < 1 {
		startHeight = 1
	}

	for h := startHeight; h <= latestHeight; h++ {
		block, err := client.Block(h)
		if err != nil || block.Block.LastCommit == nil {
			log.Printf("Error block %d: %v", h, err)
			continue
		}

		participating := make(map[string]bool)
		for _, precommit := range block.Block.LastCommit.Precommits {
			if precommit != nil {
				participating[precommit.ValidatorAddress.String()] = true
			}
		}

		blockWindow = append(blockWindow, BlockParticipation{
			Height:     h,
			Validators: participating,
		})
	}

	log.Printf("Sliding window initialized to block %d.\n", latestHeight)
	// send report all days
	go func() {
		for {

			now := time.Now()

			// Time of next sending (for example at 9:00 p.m.)
			next := time.Date(now.Year(), now.Month(), now.Day(), config.DailyReportHour, config.DailyReportMinute, 0, 0, now.Location())
			if next.Before(now) {
				next = next.Add(24 * time.Hour)
			}

			durationUntilNext := next.Sub(now)
			log.Printf("Next Discord report in %s", durationUntilNext)

			time.Sleep(durationUntilNext)
			sendDailyStats()
		}
	}()

	// init Monimap all 5 min if have a news validator
	go func() {
		for {
			initMonikerMap()
			time.Sleep(5 * time.Minute)
		}
	}()

	// Start the real-time tracking loop
	go func() {

		currentHeight := latestHeight

		for {

			latest, err := client.LatestBlockHeight()
			if err != nil {
				log.Printf("Recovery error: height: %v", err)
				continue
			}

			if latest <= currentHeight {
				continue // not news block
			}
			log.Println("last block ", latest)
			// Load new blocks (if more than one at a time)

			for h := currentHeight + 1; h <= latest; h++ {
				block, err := client.Block(h)
				println(block)
				if err != nil || block.Block.LastCommit == nil {
					log.Printf("Erreur bloc %d: %v", h, err)
					continue
				}

				participating := make(map[string]bool)
				for _, precommit := range block.Block.LastCommit.Precommits {
					if precommit != nil {
						participating[precommit.ValidatorAddress.String()] = true
					}
				}

				blockWindow = append(blockWindow, BlockParticipation{
					Height:     h,
					Validators: participating,
				})
				if len(blockWindow) > config.WindowSize {
					blockWindow = blockWindow[1:]
				}

				log.Printf("Block %d added to window", h)

				//Calculation of participation rates
				validatorCounts := make(map[string]int)
				for _, bp := range blockWindow {
					for val := range bp.Validators {
						validatorCounts[val]++
					}
				}

				start := blockWindow[0].Height
				end := blockWindow[len(blockWindow)-1].Height

				// for prometheus
				blockWindowStartHeight.Set(float64(start))
				blockWindowEndHeight.Set(float64(end))

				for val, moniker := range monikerMap {
					count := validatorCounts[val]
					rate := float64(count) / float64(len(blockWindow)) * 100
					participationRate[val] = rate

					log.Printf("Validator %s (%s) : %.2f%% \n", val, moniker, rate)
					validatorParticipation.WithLabelValues(val, moniker).Set(rate)
					if rate < 100 {
						if lastAlertSent[val] < h-int64(config.WindowSize) {
							sendDiscordAlert(val, rate, moniker, start, end)
							lastAlertSent[val] = h
						}
					}
				}
			}

			currentHeight = latest
		}
	}()

	// Exposure Prometheus
	http.Handle("/metrics", promhttp.Handler())
	log.Println("Prometheus metrics available on :8888/metrics")
	log.Fatal(http.ListenAndServe(":8888", nil))
}

func sendDiscordAlert(validator string, rate float64, moniker string, startHeight int64, endHeight int64) {
	webhookURL := config.DiscordWebhookURL

	message := fmt.Sprintf("⚠️ Validator %s (%s) has a participation rate of %.2f%% over the last %d blocks (from block %d to %d).",
		moniker, validator, rate, config.WindowSize, startHeight, endHeight)

	payload := map[string]string{"content": message}
	body, _ := json.Marshal(payload)

	http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
}

func initMonikerMap() {
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
	monikerMutex.Lock()
	defer monikerMutex.Unlock()

	monikerMap = make(map[string]string)

	// Display each pair blockProposer + blockProposerLabel
	for _, block := range blocksResp.Data.Items {
		monikerMap[block.BlockProposer] = block.BlockProposerLabel

	}
	verifyValidatorCount()
}

func verifyValidatorCount() {
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
	actual := len(monikerMap)
	log.Printf("Number of validators in recovered blocks: %d / %d expected\n", actual, expected)

	if actual != expected {
		message := fmt.Sprintf("⚠️ Warning: only %d validators recovered out of %d expected!", actual, expected)
		log.Printf(message)
		initMonikerMap()
		// sendDiscordAlert(message)

	}
}
func sendDailyStats() {
	monikerMutex.RLock()
	defer monikerMutex.RUnlock()

	start := blockWindow[0].Height
	end := blockWindow[len(blockWindow)-1].Height

	var buffer bytes.Buffer
	//buffer.WriteString("📊 **Daily participation summary** :\n\n")
	buffer.WriteString(fmt.Sprintf("📊 **Daily participation summary** (Blocks %d → %d) :\n\n", start, end))

	for val, rate := range participationRate {
		moniker := monikerMap[val]
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

	_, err := http.Post(config.DiscordWebhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Error sending daily stats on Discord: : %v", err)
	}
}
