package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// Structure de configuration YAML
type Config struct {
	RPCEndpoint      string `yaml:"rpc_endpoint"`
	ValidatorAddress string `yaml:"validator_address"`
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

	log.Printf("Config loaded: RPC=%s, Validator=%s\n", config.RPCEndpoint, config.ValidatorAddress)
}

var (
	missedBlocks = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gnoland_missed_blocks",
		Help: "Number of blocks missed by the validator",
	})

	consecutiveMissedBlocks = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gnoland_consecutive_missed_blocks",
		Help: "Number of consecutive missed blocks by the validator",
	})
)

func main() {
	loadConfig()

	prometheus.MustRegister(missedBlocks)
	prometheus.MustRegister(consecutiveMissedBlocks)

	rpcClient, err := rpcclient.NewHTTPClient(config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}

	client := gnoclient.Client{RPCClient: rpcClient}

	go func() {
		missedTotal := 0
		consecutive := 0
		var previousHeight int64 = 0

		for {
			height, err := client.LatestBlockHeight()
			if err != nil {
				log.Println("Error fetching latest height:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			if height == previousHeight {
				time.Sleep(1 * time.Second)
				continue
			}
			previousHeight = height

			block, err := client.Block(height)
			if err != nil {
				log.Println("Error fetching block:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			found := false
			for _, precommit := range block.Block.LastCommit.Precommits {
				if precommit != nil && precommit.ValidatorAddress.String() == config.ValidatorAddress {
					found = true
					break
				}
			}

			if found {
				consecutive = 0
			} else {
				missedTotal++
				consecutive++
				log.Printf("Validator missed block %d\n", height)
			}

			missedBlocks.Set(float64(missedTotal))
			consecutiveMissedBlocks.Set(float64(consecutive))

			time.Sleep(1 * time.Second)
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	log.Println("Prometheus metrics available on :8888/metrics")
	log.Fatal(http.ListenAndServe(":8888", nil))
}
