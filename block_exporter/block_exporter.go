package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// Structure de configuration YAML
type Config struct {
	RPCEndpoint      string `yaml:"rpc_endpoint"`
	ValidatorAddress string `yaml:"validator_address"`
	MetricsPort      int    `yaml:"metrics_port"`
}

var config Config

// blockReader exposes only the read-only RPCs used by the exporter. Importing
// gnoclient also links its signing/key-management dependencies, including the
// unmaintained OpenPGP implementation, even though this process never signs.
type blockReader struct {
	rpc interface {
		Status() (*ctypes.ResultStatus, error)
		Block(*int64) (*ctypes.ResultBlock, error)
	}
}

func (c blockReader) LatestBlockHeight() (int64, error) {
	if c.rpc == nil {
		return 0, errors.New("missing RPCClient")
	}
	status, err := c.rpc.Status()
	if err != nil {
		return 0, fmt.Errorf("block number query failed: %w", err)
	}
	return status.SyncInfo.LatestBlockHeight, nil
}

func (c blockReader) Block(height int64) (*ctypes.ResultBlock, error) {
	if c.rpc == nil {
		return nil, errors.New("missing RPCClient")
	}
	if height <= 0 {
		return nil, errors.New("invalid block height provided")
	}
	block, err := c.rpc.Block(&height)
	if err != nil {
		return nil, fmt.Errorf("block query failed: %w", err)
	}
	return block, nil
}

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

	client := blockReader{rpc: rpcClient}

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
	//log.Fatal(http.ListenAndServe(":8888", nil))
	addr := fmt.Sprintf(":%d", config.MetricsPort)
	log.Printf("Prometheus metrics available on %s/metrics", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
