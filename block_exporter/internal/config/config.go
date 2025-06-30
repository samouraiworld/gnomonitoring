package config

import (
	"log"
	"os"

	"gopkg.in/yaml.v2"
)

type config struct {
	RPCEndpoint      string `yaml:"rpc_endpoint"`
	ValidatorAddress string `yaml:"validator_address"`
	MetricsPort      int    `yaml:"metrics_port"`
}

//var Config Config

// Charger config.yaml
func LoadConfig() {
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
