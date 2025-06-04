package main

import (
	"log"
	"os"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
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

func main() {
	loadConfig()

	// prometheus.MustRegister(missedBlocks)
	// prometheus.MustRegister(consecutiveMissedBlocks)

	rpcClient, err := rpcclient.NewHTTPClient(config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}
	client := gnoclient.Client{RPCClient: rpcClient}

	addr := "g1tq3gyzjmuu4gzu4np4ckfgun87j540gvx43d65"
	path := "gnoland/valopers/v2"

	res, core, err := client.Eval
	QEval(path, addr)
	// var addr crypto.Address
	// addr, err = crypto.AddressFromString("g1tq3gyzjmuu4gzu4np4ckfgun87j540gvx43d65")
	// base, result, err := client.QueryAccount(addr)

	log.Println(res)
	log.Println(core)
	log.Println(err)

	// http.Handle("/metrics", promhttp.Handler())
	// log.Println("Prometheus metrics available on :8888/metrics")
	// log.Fatal(http.ListenAndServe(":8888", nil))
}

// https://test6.testnets.gno.land/r/gnoland/valopers/v2
