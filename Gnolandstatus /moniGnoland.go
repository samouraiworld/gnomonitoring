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

// Structure de configuration YAML
type Config struct {
	RPCEndpoint       string `yaml:"rpc_endpoint"`
	DiscordWebhookURL string `yaml:"discord_webhook_url"`
}

var testAlert = flag.Bool("test-alert", false, "Envoie une alerte test Discord")

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
	blockWindow       []BlockParticipation
	windowSize        = 100
	participationRate = make(map[string]float64)
	lastAlertSent     = make(map[string]int64) // pour éviter de spammer
	monikerMap        = make(map[string]string)
)

// prometheus var
var validatorParticipation = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gnoland_validator_participation_rate",
		Help: "Taux de participation (%) du validateur sur la fenêtre glissante",
	},
	[]string{"validator_address", "moniker"},
)

func init() {
	prometheus.MustRegister(validatorParticipation)
}

var monikerMutex sync.RWMutex

func main() {
	flag.Parse()

	if *testAlert {
		sendDiscordAlert("g1test123456", 42.0, "🧪TEST Moniker")
		return
	}

	loadConfig()
	initMonikerMap()

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

	go func() {
		for {
			initMonikerMap()
			time.Sleep(5 * time.Minute)
		}
	}()

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

				for val, moniker := range monikerMap {
					count := validatorCounts[val]
					rate := float64(count) / float64(len(blockWindow)) * 100
					participationRate[val] = rate

					log.Printf("Validator %s (%s) : %.2f%% \n", val, moniker, rate)
					validatorParticipation.WithLabelValues(val, moniker).Set(rate)
					if rate < 100 {
						if lastAlertSent[val] < h-int64(windowSize) {
							sendDiscordAlert(val, rate, moniker)
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

func sendDiscordAlert(validator string, rate float64, moniker string) {
	webhookURL := config.DiscordWebhookURL

	message := fmt.Sprintf("⚠️ Le validateur %s (%s) a un taux de participation de %.2f%% sur les %d derniers blocs.",
		moniker, validator, rate, windowSize)

	payload := map[string]string{"content": message}
	body, _ := json.Marshal(payload)

	http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
}

func initMonikerMap() {
	resp, err := http.Get("https://test6.api.onbloc.xyz/v1/blocks?limit=40")
	if err != nil {
		log.Fatalf("Erreur lors de la récupération des blocs : %v", err)
	}
	defer resp.Body.Close()

	// Lire tout le corps une seule fois
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Erreur lors de la lecture de la réponse : %v", err)
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
		log.Fatalf("Erreur de décodage JSON : %v", err)
	}
	monikerMutex.Lock()
	defer monikerMutex.Unlock()

	monikerMap = make(map[string]string)

	// Afficher chaque pair blockProposer + blockProposerLabel
	for _, block := range blocksResp.Data.Items {
		//fmt.Printf("Address: %s | Moniker: %s\n", block.BlockProposer, block.BlockProposerLabel)
		monikerMap[block.BlockProposer] = block.BlockProposerLabel

	}
	verifyValidatorCount()
}

func verifyValidatorCount() {
	resp, err := http.Get("https://test6.api.onbloc.xyz/v1/stats/summary/accounts")
	if err != nil {
		log.Printf("Erreur récupération du résumé des comptes : %v", err)
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
		log.Printf("Erreur de décodage JSON résumé : %v", err)
		return
	}

	expected := summaryResp.Data.Data.Validators
	actual := len(monikerMap)
	log.Printf("Nombre de validateurs dans les blocs récupérés : %d / %d attendus\n", actual, expected)

	if actual != expected {
		message := fmt.Sprintf("⚠️ Attention : seuls %d validateurs récupérés sur %d attendus !", actual, expected)
		log.Printf(message)
		// sendDiscordAlert(message)

	}
}
