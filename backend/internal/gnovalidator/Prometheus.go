package gnovalidator

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"gorm.io/gorm"
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
		[]string{"chain", "validator_address", "moniker"},
	)

	ConsecutiveMissedBlocks = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_consecutive_missed_blocks",
			Help: "Number of consecutive blocks missed by a validator",
		},
		[]string{"chain", "validator_address", "moniker"},
	)
	ValidatorParticipation = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_validator_participation_rate",
			Help: "Validator participation rate (%) over the sliding window",
		},
		[]string{"chain", "validator_address", "moniker"},
	)

	initOnce sync.Once
)

func Init() {
	initOnce.Do(func() {
		prometheus.MustRegister(ValidatorParticipation)
		prometheus.MustRegister(MissedBlocks)
		prometheus.MustRegister(ConsecutiveMissedBlocks)
	})
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

func UpdatePrometheusMetricsFromDB(db *gorm.DB, chainID string) error {
	// ValidatorParticipation
	stats, err := CalculateValidatorRates(db, chainID)
	if err != nil {
		return err
	}

	for _, stat := range stats {
		ValidatorParticipation.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(stat.Rate)
	}
	// MissedBlocks
	missedStats, err := CalculateMissedBlocks(db, chainID)
	if err != nil {
		return err
	}
	for _, stat := range missedStats {
		MissedBlocks.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(float64(stat.Missed))
	}

	// ConsecutiveMissedBlocks
	consecutiveStats, err := CalculateConsecutiveMissedBlocks(db, chainID)
	if err != nil {
		return err
	}
	for _, stat := range consecutiveStats {
		ConsecutiveMissedBlocks.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(float64(stat.Count))
	}
	return nil
}

func StartMetricsUpdater(db *gorm.DB) {
	go func() {
		for {
			for _, chainID := range internal.EnabledChains {
				if err := UpdatePrometheusMetricsFromDB(db, chainID); err != nil {
					log.Printf("Error updating metrics for chain %s: %v", chainID, err)
				}
			}
			time.Sleep(5 * time.Minute)
		}
	}()
}
