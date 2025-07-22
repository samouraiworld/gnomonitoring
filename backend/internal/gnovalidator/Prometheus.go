package gnovalidator

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type ValidatorStat struct {
	Address string
	Moniker string
	Rate    float64
}
type MissedBlockStat struct {
	Address string
	Moniker string
	Missed  int
}
type ConsecutiveMissedStat struct {
	Address string
	Moniker string
	Count   int
}

// prometheus var start and end block analyse
var (
	MissedBlocks = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_missed_blocks",
			Help: "Total number of blocks missed today by a validator",
		},
		[]string{"validator_address", "moniker"},
	)

	ConsecutiveMissedBlocks = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_consecutive_missed_blocks",
			Help: "Number of consecutive blocks missed by a validator",
		},
		[]string{"validator_address", "moniker"},
	)
	ValidatorParticipation = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_validator_participation_rate",
			Help: "Validator participation rate (%) over the sliding window",
		},
		[]string{"validator_address", "moniker"},
	)
)

func Init() {
	prometheus.MustRegister(ValidatorParticipation)
	prometheus.MustRegister(MissedBlocks)
	prometheus.MustRegister(ConsecutiveMissedBlocks)
}
func StartPrometheusServer(port int) {

	// Exposure Prometheus
	addr := fmt.Sprintf(":%d", port)

	http.Handle("/metrics", promhttp.Handler())
	go func() {
		err := http.ListenAndServe(addr, nil)
		if err != nil {
			log.Fatalf("Error starting Prometheus metrics endpoint: %v", err)
		}
	}()
}
func UpdatePrometheusMetricsFromDB(db *sql.DB) error {
	// ValidatorParticipation
	stats, err := CalculateValidatorRates(db)
	if err != nil {
		return err
	}

	for _, stat := range stats {
		ValidatorParticipation.WithLabelValues(stat.Address, stat.Moniker).Set(stat.Rate)
	}
	// MissedBlocks
	missedStats, err := CalculateMissedBlocks(db)
	if err != nil {
		return err
	}
	for _, stat := range missedStats {
		MissedBlocks.WithLabelValues(stat.Address, stat.Moniker).Set(float64(stat.Missed))
	}

	// ConsecutiveMissedBlocks
	consecutiveStats, err := CalculateConsecutiveMissedBlocks(db)
	if err != nil {
		return err
	}
	for _, stat := range consecutiveStats {
		ConsecutiveMissedBlocks.WithLabelValues(stat.Address, stat.Moniker).Set(float64(stat.Count))
	}
	return nil
}

func StartMetricsUpdater(db *sql.DB) {
	go func() {
		for {
			err := UpdatePrometheusMetricsFromDB(db)
			if err != nil {
				log.Printf("Error updating metrics: %v", err)
			}
			time.Sleep(5 * time.Minute)
		}
	}()
}
