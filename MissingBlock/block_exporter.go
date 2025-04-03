package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type StatusResponse struct {
	Result struct {
		SyncInfo struct {
			LatestBlockHeight string `json:"latest_block_height"` // Changer en string
		} `json:"sync_info"`
	} `json:"result"`
}

type ValidatorResponse struct {
	Result struct {
		ValidatorInfo struct {
			SignedBlocks int `json:"signed_blocks"` // Hypothétique, à confirmer selon les données renvoyées par l'API
		} `json:"validator_info"`
	} `json:"result"`
}

var (
	totalBlocks = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gnoland_total_blocks",
		Help: "Total number of blocks received by the validator",
	})

	signedBlocks = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gnoland_signed_blocks",
		Help: "Total number of blocks signed by the validator",
	})

	missedBlocks = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gnoland_missed_blocks",
		Help: "Total number of blocks missed by the validator",
	})
)

func fetchMetrics() {
	// Récupérer l'état général du validateur (numéro de bloc)
	statusResp, err := http.Get("http://127.0.0.1:26657/status")
	if err != nil {
		log.Println("Error fetching status from RPC:", err)
		return
	}
	defer statusResp.Body.Close()

	var status StatusResponse
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		log.Println("Error decoding status JSON:", err)
		return
	}

	// Récupérer les informations spécifiques au validateur (bloc signé)
	validatorResp, err := http.Get("http://127.0.0.1:26657/validators")
	if err != nil {
		log.Println("Error fetching validator info from RPC:", err)
		return
	}
	defer validatorResp.Body.Close()

	var validator ValidatorResponse
	if err := json.NewDecoder(validatorResp.Body).Decode(&validator); err != nil {
		log.Println("Error decoding validator JSON:", err)
		return
	}

	// Conversion des chaînes en int avec strconv.Atoi
	totalBlocksHeight, err := strconv.Atoi(status.Result.SyncInfo.LatestBlockHeight) // Conversion de string en int
	if err != nil {
		log.Println("Error converting LatestBlockHeight:", err)
		return
	}

	signedBlocksCount := validator.Result.ValidatorInfo.SignedBlocks // No conversion needed if it's already an int

	// Mettre à jour les métriques
	totalBlocks.Set(float64(totalBlocksHeight))
	signedBlocks.Set(float64(signedBlocksCount))
	missedBlocks.Set(float64(totalBlocksHeight - signedBlocksCount)) // Le calcul des blocs manqués
}

func main() {
	// Enregistrement des métriques
	prometheus.MustRegister(totalBlocks)
	prometheus.MustRegister(signedBlocks)
	prometheus.MustRegister(missedBlocks)

	// Mise à jour des métriques toutes les 10 secondes
	go func() {
		for {
			fetchMetrics()
			log.Println("Metrics updated")
			time.Sleep(1 * time.Second)
		}
	}()

	// Serveur HTTP pour Prometheus
	http.Handle("/metrics", promhttp.Handler())
	log.Println("Starting metrics server on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
