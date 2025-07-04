package internal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var TestAlert = flag.Bool("test-alert", false, "Send alert test for Discord")
var MonikerMutex sync.RWMutex
var lastRPCErrorAlert time.Time //anti spam for error RPC

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
	Init()
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
		defer func() {
			if r := recover(); r != nil {
				log.Printf("⚠️ Panic in daily stats goroutine: %v", r)
			}
		}()

		for {
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

	// Start the real-time tracking loop
	go func() {

		currentHeight := latestHeight
		lastProgressHeight := currentHeight
		lastProgressTime := time.Now()
		alertSent := false
		restoredNotified := false

		for {

			latest, err := client.LatestBlockHeight()
			if err != nil {
				log.Printf("Recovery error: height: %v", err)

				if time.Since(lastRPCErrorAlert) > 10*time.Minute {
					msg := fmt.Sprintf("⚠️ Error when querying latest block height: %v", err)
					msg += fmt.Sprintf("\nLast known block height: %d", currentHeight)

					SendDiscordAlertValidator(msg, db)
					SendSlackAlertValidator(msg, db)

					lastRPCErrorAlert = time.Now()
				}

				time.Sleep(10 * time.Second)
				continue
			}

			lastRPCErrorAlert = time.Time{}

			//  Stagnation detection
			if latest == lastProgressHeight {
				if !alertSent && time.Since(lastProgressTime) > 2*time.Minute {
					msg := fmt.Sprintf("⚠️ Blockchain stuck at height %d for more than 2 minutes", latest)
					log.Println(msg)
					SendDiscordAlertValidator(msg, db)
					SendSlackAlertValidator(msg, db)

					alertSent = true
					restoredNotified = false
					lastProgressTime = time.Now()
				}
			} else {
				lastProgressHeight = latest
				lastProgressTime = time.Now()

				//send alert if gnoland return to normal
				if alertSent && !restoredNotified {
					SendDiscordAlertValidator("✅ **Activity Restored**: Gnoland is back to normal.", db)
					SendSlackAlertValidator("✅ *Activity Restored*: Gnoland is back to normal.", db)
					restoredNotified = true
					alertSent = false
				}
			}

			if latest <= currentHeight {
				time.Sleep(3 * time.Second)
				continue
			}

			// if latest <= currentHeight {
			// 	continue // not news block
			// }
			log.Println("last block ", latest)
			// Load new blocks (if more than one at a time)

			for h := currentHeight + 1; h <= latest; h++ {

				//Check if have a news validator
				if h%100 == 0 {
					log.Println("🔁 Refresh MonikerMap after 100 blocks")

					// Copy snapshot before update
					oldMap := make(map[string]string)
					MonikerMutex.RLock()
					for k, v := range MonikerMap {
						oldMap[k] = v
					}
					MonikerMutex.RUnlock()

					// Update
					InitMonikerMap()

					// Comparaison
					MonikerMutex.RLock()
					for addr, moniker := range MonikerMap {
						if _, exists := oldMap[addr]; !exists {
							msg := fmt.Sprintf("**✅ News Validator %s addr: %s  **", addr, moniker)
							msgS := fmt.Sprintf("*✅ News Validator %s addr: %s  *", addr, moniker)
							log.Println(msg)
							SendDiscordAlertValidator(msg, db)
							SendSlackAlertValidator(msgS, db)

						}
					}
					MonikerMutex.RUnlock()
				}
				// end of initMoniker of x block
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
	// log.Println(msg)

	err := SendDiscordAlertValidator(msg, db)
	if err != nil {
		log.Printf("[SendDailyStats] Discord alert failed: %v", err)
	}
	err = SendSlackAlertValidator(msg, db)
	if err != nil {
		log.Printf("[SendDailyStats] Slack alert failed: %v", err)
	}

}

func SendDiscordAlertValidator(message string, db *sql.DB) error {
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
func SendSlackAlertValidator(message string, db *sql.DB) error {
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
