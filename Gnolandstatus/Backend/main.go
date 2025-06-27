package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/samouraiworld/gnomonitoring/Gnolandstatus/Backend/internal"
)

func main() {
	flag.Parse()

	internal.LoadConfig()
	internal.InitMonikerMap()

	if *internal.TestAlert {
		internal.SendDiscordAlert("g1test123456", 42.0, "🧪TEST Moniker", 200, 300)
		return
	}

	rpcClient, err := rpcclient.NewHTTPClient(internal.Config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}

	client := gnoclient.Client{RPCClient: rpcClient}

	// Initializing the window with the latest blocks

	latestHeight, err := client.LatestBlockHeight()
	if err != nil {
		log.Fatalf("Error retrieving last height: %v", err)
	}

	startHeight := latestHeight - int64(internal.Config.WindowSize) + 1
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

		internal.BlockWindow = append(internal.BlockWindow, internal.BlockParticipation{
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
			next := time.Date(now.Year(), now.Month(), now.Day(), internal.Config.DailyReportHour, internal.Config.DailyReportMinute, 0, 0, now.Location())
			if next.Before(now) {
				next = next.Add(24 * time.Hour)
			}

			durationUntilNext := next.Sub(now)
			log.Printf("Next Discord report in %s", durationUntilNext)

			time.Sleep(durationUntilNext)
			internal.SendDailyStats()
		}
	}()

	// init Monimap all 5 min if have a news validator
	go func() {
		for {
			internal.InitMonikerMap()
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

				internal.BlockWindow = append(internal.BlockWindow, internal.BlockParticipation{
					Height:     h,
					Validators: participating,
				})
				if len(internal.BlockWindow) > internal.Config.WindowSize {
					internal.BlockWindow = internal.BlockWindow[1:]
				}

				log.Printf("Block %d added to window", h)

				//Calculation of participation rates
				validatorCounts := make(map[string]int)
				for _, bp := range internal.BlockWindow {
					for val := range bp.Validators {
						validatorCounts[val]++
					}
				}

				start := internal.BlockWindow[0].Height
				end := internal.BlockWindow[len(internal.BlockWindow)-1].Height

				// for prometheus
				internal.BlockWindowStartHeight.Set(float64(start))
				internal.BlockWindowEndHeight.Set(float64(end))

				for val, moniker := range internal.MonikerMap {
					count := validatorCounts[val]
					rate := float64(count) / float64(len(internal.BlockWindow)) * 100
					internal.ParticipationRate[val] = rate

					log.Printf("Validator %s (%s) : %.2f%% \n", val, moniker, rate)
					internal.ValidatorParticipation.WithLabelValues(val, moniker).Set(rate)
					if rate < 100 {
						if internal.LastAlertSent[val] < h-int64(internal.Config.WindowSize) {
							internal.SendDiscordAlert(val, rate, moniker, start, end)
							internal.LastAlertSent[val] = h
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
	addr := fmt.Sprintf(":%d", internal.Config.MetricsPort)
	log.Printf("Prometheus metrics available on %s/metrics", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
