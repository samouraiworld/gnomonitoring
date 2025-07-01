package internal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var TestAlert = flag.Bool("test-alert", false, "Send alert test for Discord")
var MonikerMutex sync.RWMutex

type BlockParticipation struct {
	Height     int64
	Validators map[string]bool
}

var (
	BlockWindow []BlockParticipation
	//windowSize        = 100
	ParticipationRate = make(map[string]float64)
	LastAlertSent     = make(map[string]int64) // to avoid spamming
	MonikerMap        = make(map[string]string)
)

// prometheus var
var ValidatorParticipation = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gnoland_validator_participation_rate",
		Help: "Validator participation rate (%) over the sliding window",
	},
	[]string{"validator_address", "moniker"},
)

// prometheus var start and end block analyse
var (
	BlockWindowStartHeight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gnoland_block_window_start_height",
			Help: "Start height of the current block window",
		},
	)
	BlockWindowEndHeight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gnoland_block_window_end_height",
			Help: "End height of the current block window",
		},
	)
)

func StartValidatorMonitoring(db *sql.DB) {

	flag.Parse()

	// LoadConfig()
	InitMonikerMap()

	if *TestAlert {
		moniker := "g1test123456"
		validator := "xxxxxxxx"
		rate := 42.0
		winsize := 100
		startHeight := 0
		endHeight := 99
		url := "https://discord.com/api/webhooks/1362423744097681458/vslQMyePTF587NZzwD979Q_p8b3xtmlUrKR154liHoGSxTWbILh7Lf4-_Y75W0TFPTV5"
		message := fmt.Sprintf("⚠️ Validator %s (%s) has a participation rate of %.2f%% over the last %d blocks (from block %d to %d).",
			moniker, validator, rate, winsize, startHeight, endHeight)
		SendDiscordAlert(message, url)
		return
	}

	rpcClient, err := rpcclient.NewHTTPClient(Config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}

	client := gnoclient.Client{RPCClient: rpcClient}

	// Initializing the window with the latest blocks

	latestHeight, err := client.LatestBlockHeight()
	if err != nil {
		log.Fatalf("Error retrieving last height: %v", err)
	}

	startHeight := latestHeight - int64(Config.WindowSize) + 1
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

		BlockWindow = append(BlockWindow, BlockParticipation{
			Height:     h,
			Validators: participating,
		})
	}

	log.Printf("Sliding window initialized to block %d.\n", latestHeight)
	// send report all days
	go func() {
		for {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("⚠️ Panic in daily stats goroutine: %v", r)
				}
			}()

			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), Config.DailyReportHour, Config.DailyReportMinute, 0, 0, now.Location())
			if next.Before(now) {
				next = next.Add(24 * time.Hour)
			}
			durationUntilNext := next.Sub(now)
			log.Printf("Next Discord report in %s", durationUntilNext)

			time.Sleep(durationUntilNext)
			log.Println("⏰ Time reached. Sending daily stats...")
			SendDailyStats(db)
		}
	}()

	// init Monimap all 5 min if have a news validator
	go func() {
		for {

			//copy monikermap
			MonikerMutex.RLock()
			old := make(map[string]string)
			for k, v := range MonikerMap {
				old[k] = v
			}
			MonikerMutex.RUnlock()
			//update Moniker map
			InitMonikerMap()

			//conmparate
			MonikerMutex.RLock()
			newLength := len(MonikerMap)
			oldLength := len(old)
			if newLength != oldLength {
				// search news validator
				for addr, moniker := range MonikerMap {
					if _, exists := old[addr]; !exists {
						// Send Discord Alert
						msg := fmt.Sprintf("**News Validator %s addr: %s  **", addr, moniker)
						msgS := fmt.Sprintf("*News Validator %s addr: %s  *", addr, moniker)

						SendDiscordAlertValidator(msg, db)
						SendSlackAlertValidator(msgS, db)

					}
				}
			}

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

				BlockWindow = append(BlockWindow, BlockParticipation{
					Height:     h,
					Validators: participating,
				})
				if len(BlockWindow) > Config.WindowSize {
					BlockWindow = BlockWindow[1:]
				}

				log.Printf("Block %d added to window", h)

				//Calculation of participation rates
				validatorCounts := make(map[string]int)
				for _, bp := range BlockWindow {
					for val := range bp.Validators {
						validatorCounts[val]++
					}
				}

				start := BlockWindow[0].Height
				end := BlockWindow[len(BlockWindow)-1].Height

				// for prometheus
				BlockWindowStartHeight.Set(float64(start))
				BlockWindowEndHeight.Set(float64(end))

				for val, moniker := range MonikerMap {
					count := validatorCounts[val]
					rate := float64(count) / float64(len(BlockWindow)) * 100
					ParticipationRate[val] = rate

					log.Printf("Validator %s (%s) : %.2f%% \n", val, moniker, rate)
					ValidatorParticipation.WithLabelValues(val, moniker).Set(rate)
					if rate < 100 {
						if LastAlertSent[val] < h-int64(Config.WindowSize) {
							message := fmt.Sprintf("⚠️ Validator %s (%s) has a participation rate of %.2f%% over the last %d blocks (from block %d to %d).",
								moniker, val, rate, Config.WindowSize, start, end)
							SendDiscordAlertValidator(message, db)
							SendSlackAlertValidator(message, db)

							LastAlertSent[val] = h
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
	//log.Fatal(http.ListenAndServe(":8888", nil))
	addr := fmt.Sprintf(":%d", Config.MetricsPort)
	log.Printf("Prometheus metrics available on %s/metrics", addr)
	log.Fatal(http.ListenAndServe(addr, nil))

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
func Init() {
	prometheus.MustRegister(ValidatorParticipation)
	prometheus.MustRegister(BlockWindowStartHeight)
	prometheus.MustRegister(BlockWindowEndHeight)
}
func SendDailyStats(db *sql.DB) {
	MonikerMutex.RLock()
	defer MonikerMutex.RUnlock()

	start := BlockWindow[0].Height
	end := BlockWindow[len(BlockWindow)-1].Height

	var buffer bytes.Buffer
	buffer.WriteString(fmt.Sprintf("📊 *Daily participation summary* (Blocks %d → %d) :\n\n", start, end))

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
	msg := buffer.String()

	SendDiscordAlertValidator(msg, db)
	SendSlackAlertValidator(msg, db)

}

func SendDiscordAlertValidator(message string, db *sql.DB) {
	hooks, err := ListMonitoringWebhooks(db) // ou cache local
	if err != nil {
		log.Printf("Error retrieving hooks: %v", err)
		return
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
}
func SendSlackAlertValidator(message string, db *sql.DB) {
	hooks, err := ListMonitoringWebhooks(db)
	if err != nil {
		log.Printf("Error retrieving hooks: %v", err)
		return
	}

	for _, hook := range hooks {
		if hook.Type != "slack" {
			continue
		}

		payload := map[string]string{"text": message} // Slack attend le champ "text"
		body, _ := json.Marshal(payload)

		resp, err := http.Post(hook.URL, "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.Printf("Error sending to Slack webhook %s: %v", hook.URL, err)
			continue
		}
		resp.Body.Close()
	}
}
