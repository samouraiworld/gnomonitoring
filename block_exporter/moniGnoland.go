package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/gnolang/gno/tm2/pkg/bft/types"

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

type BlockParticipation struct {
	Height     int64
	Validators map[string]bool
}

var (
	blockWindow       []BlockParticipation
	windowSize        = 100
	participationRate = make(map[string]float64)
	lastAlertSent     = make(map[string]int64) // pour éviter de spammer
	monikerMap        = make(map[string]string)
)

// fonction pour récupérer les moniker
func fetchValidators(rpc rpcclient.Client, height int64) ([]*types.Validator, error) {
	res, err := rpc.Validators(&height)
	if err != nil {
		return nil, err
	}
	return res.Validators, nil
}

func main() {
	loadConfig()

	rpcClient, err := rpcclient.NewHTTPClient(config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}

	client := gnoclient.Client{RPCClient: rpcClient}

	// Initialisation de la fenêtre avec les derniers blocs
	latestHeight, err := client.LatestBlockHeight()
	if err != nil {
		log.Fatalf("Erreur en récupérant le dernier height: %v", err)
	}

	// Récupérer la liste des validateurs pour créer le monikerMap
	vals, err := fetchValidators(rpcClient, latestHeight)
	if err != nil {
		log.Printf("Erreur en récupérant la liste des validateurs : %v\n", err)
	} else {
		for _, val := range vals {
			moniker := val.Description.Moniker
			address := val.Address.String()
			monikerMap[address] = moniker
		}
	}

	startHeight := latestHeight - int64(windowSize) + 1
	if startHeight < 1 {
		startHeight = 1
	}

	for h := startHeight; h <= latestHeight; h++ {
		block, err := client.Block(h)
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
	}

	log.Printf("Fenêtre glissante initialisée jusqu’au bloc %d.\n", latestHeight)

	// Démarrer la boucle de suivi temps réel
	go func() {
		currentHeight := latestHeight

		for {
			latest, err := client.LatestBlockHeight()
			if err != nil {
				log.Printf("Erreur récupération height: %v", err)
				continue
			}

			if latest <= currentHeight {
				continue // Pas de nouveau bloc
			}
			log.Println("last block ", latest)
			// Charger les nouveaux blocs (si plusieurs à la fois)
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
				if len(blockWindow) > windowSize {
					blockWindow = blockWindow[1:]
				}

				log.Printf("Bloc %d ajouté à la fenêtre", h)

				// Calcul des taux de participation
				validatorCounts := make(map[string]int)
				for _, bp := range blockWindow {
					for val := range bp.Validators {
						validatorCounts[val]++
					}
				}

				for val, count := range validatorCounts {
					rate := float64(count) / float64(len(blockWindow)) * 100
					participationRate[val] = rate
					moniker := monikerMap[val]
					if moniker == "" {
						moniker = "inconnu"
					}
					log.Printf("Validator %s %s : %.2f%% \n", val, moniker, rate)

					if rate < 100 {
						// Envoi d’une alerte si pas déjà envoyée récemment
						if lastAlertSent[val] < h-int64(windowSize) { //pour eviter de spamer
							sendDiscordAlert(val, rate)
							lastAlertSent[val] = h
						}
					}
				}
			}

			currentHeight = latest
		}
	}()

	// Exposition Prometheus
	http.Handle("/metrics", promhttp.Handler())
	log.Println("Prometheus metrics available on :8888/metrics")
	log.Fatal(http.ListenAndServe(":8888", nil))
}

func sendDiscordAlert(validator string, rate float64) {
	webhookURL := os.Getenv("DISCORD_WEBHOOK_URL")

	moniker := monikerMap[validator]
	if moniker == "" {
		moniker = "inconnu"
	}

	message := fmt.Sprintf("⚠️ Le validateur **%s** (%s) a un taux de participation de %.2f%% sur les %d derniers blocs.",
		moniker, validator, rate, windowSize)

	payload := map[string]string{"content": message}
	body, _ := json.Marshal(payload)

	http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
}
