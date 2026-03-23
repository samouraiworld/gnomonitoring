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
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
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

	ValidatorUptime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_validator_uptime",
			Help: "Validator uptime (%) over the last 500 blocks",
		},
		[]string{"chain", "validator_address", "moniker"},
	)

	ValidatorOperationTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_validator_operation_time",
			Help: "Days since last validator downtime event",
		},
		[]string{"chain", "validator_address", "moniker"},
	)

	ValidatorTxContribution = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_validator_tx_contribution",
			Help: "Validator transaction contribution (%) in current month",
		},
		[]string{"chain", "validator_address", "moniker"},
	)

	ValidatorMissingBlocksMonth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_validator_missing_blocks_month",
			Help: "Number of blocks missed by validator in current month",
		},
		[]string{"chain", "validator_address", "moniker"},
	)

	ValidatorFirstSeenUnix = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_validator_first_seen_unix",
			Help: "Unix timestamp of validator's first participation",
		},
		[]string{"chain", "validator_address", "moniker"},
	)

	// Phase 2: Chain-level metrics
	ChainActiveValidators = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_chain_active_validators",
			Help: "Number of active validators in the last 100 blocks",
		},
		[]string{"chain"},
	)

	ChainAvgParticipationRate = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_chain_avg_participation_rate",
			Help: "Average participation rate (%) across all validators in last 100 blocks",
		},
		[]string{"chain"},
	)

	ChainCurrentHeight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_chain_current_height",
			Help: "Current block height of the chain",
		},
		[]string{"chain"},
	)

	// Phase 3: Alert metrics
	ActiveAlerts = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_active_alerts",
			Help: "Number of currently active (unresolved) alerts by severity level",
		},
		[]string{"chain", "level"},
	)

	AlertsTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_alerts_total",
			Help: "Total number of alerts sent (cumulative)",
		},
		[]string{"chain", "level"},
	)

	initOnce sync.Once
)

func Init() {
	initOnce.Do(func() {
		prometheus.MustRegister(ValidatorParticipation)
		prometheus.MustRegister(MissedBlocks)
		prometheus.MustRegister(ConsecutiveMissedBlocks)
		prometheus.MustRegister(ValidatorUptime)
		prometheus.MustRegister(ValidatorOperationTime)
		prometheus.MustRegister(ValidatorTxContribution)
		prometheus.MustRegister(ValidatorMissingBlocksMonth)
		prometheus.MustRegister(ValidatorFirstSeenUnix)
		// Phase 2: Chain metrics
		prometheus.MustRegister(ChainActiveValidators)
		prometheus.MustRegister(ChainAvgParticipationRate)
		prometheus.MustRegister(ChainCurrentHeight)
		// Phase 3: Alert metrics
		prometheus.MustRegister(ActiveAlerts)
		prometheus.MustRegister(AlertsTotal)
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
	// Phase 1: Base validator metrics (non-critical errors logged, execution continues)

	// ValidatorParticipation
	stats, err := CalculateValidatorRates(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error calculating validator rates for chain %s: %v", chainID, err)
	} else {
		for _, stat := range stats {
			ValidatorParticipation.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(stat.Rate)
		}
	}

	// MissedBlocks
	missedStats, err := CalculateMissedBlocks(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error calculating missed blocks for chain %s: %v", chainID, err)
	} else {
		for _, stat := range missedStats {
			MissedBlocks.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(float64(stat.Missed))
		}
	}

	// ConsecutiveMissedBlocks
	consecutiveStats, err := CalculateConsecutiveMissedBlocks(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error calculating consecutive missed blocks for chain %s: %v", chainID, err)
	} else {
		for _, stat := range consecutiveStats {
			ConsecutiveMissedBlocks.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(float64(stat.Count))
		}
	}

	// ValidatorUptime (last 500 blocks)
	uptimeStats, err := database.UptimeMetricsaddr(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error calculating uptime metrics for chain %s: %v", chainID, err)
	} else {
		for _, stat := range uptimeStats {
			ValidatorUptime.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(stat.Uptime)
		}
	}

	// ValidatorOperationTime (days since last down)
	operationStats, err := database.OperationTimeMetricsaddr(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error calculating operation time metrics for chain %s: %v", chainID, err)
	} else {
		for _, stat := range operationStats {
			ValidatorOperationTime.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(stat.DaysDiff)
		}
	}

	// ValidatorTxContribution (current month)
	txStats, err := database.TxContrib(db, chainID, "current_month")
	if err != nil {
		log.Printf("⚠️ Error calculating tx contribution metrics for chain %s: %v", chainID, err)
	} else {
		for _, stat := range txStats {
			ValidatorTxContribution.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(stat.TxContrib)
		}
	}

	// ValidatorMissingBlocksMonth (current month)
	missingStats, err := database.MissingBlock(db, chainID, "current_month")
	if err != nil {
		log.Printf("⚠️ Error calculating missing blocks metrics for chain %s: %v", chainID, err)
	} else {
		for _, stat := range missingStats {
			ValidatorMissingBlocksMonth.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(float64(stat.MissingBlock))
		}
	}

	// ValidatorFirstSeenUnix (unix timestamp of first participation)
	firstSeenStats, err := database.GetFirstSeen(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error calculating first seen metrics for chain %s: %v", chainID, err)
	} else {
		for _, stat := range firstSeenStats {
			// Parse the date string returned from SQL (format: "YYYY-MM-DD HH:MM:SS" or with timezone)
			var t time.Time
			layouts := []string{
				"2006-01-02 15:04:05-07:00", // with timezone
				"2006-01-02 15:04:05",       // without timezone
			}
			var parseErr error
			for _, layout := range layouts {
				t, parseErr = time.Parse(layout, stat.FirstSeen)
				if parseErr == nil {
					break
				}
			}
			if parseErr != nil {
				log.Printf("⚠️ Failed to parse first_seen date for %s: %v", stat.Addr, parseErr)
				continue
			}
			ValidatorFirstSeenUnix.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(float64(t.Unix()))
		}
	}

	// Phase 2: Chain-level metrics
	log.Printf("🔍 DEBUG: Computing chain metrics for %s", chainID)

	activeCount, err := database.GetActiveValidatorCount(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error getting active validator count for chain %s: %v", chainID, err)
	} else {
		log.Printf("✅ DEBUG: activeCount=%d for chain %s", activeCount, chainID)
		ChainActiveValidators.WithLabelValues(chainID).Set(float64(activeCount))
	}

	avgRate, err := database.GetAvgParticipationRate(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error getting avg participation rate for chain %s: %v", chainID, err)
	} else {
		ChainAvgParticipationRate.WithLabelValues(chainID).Set(avgRate)
	}

	height, err := database.GetCurrentChainHeight(db, chainID)
	if err != nil {
		log.Printf("⚠️ Error getting current chain height for chain %s: %v", chainID, err)
	} else {
		ChainCurrentHeight.WithLabelValues(chainID).Set(float64(height))
	}

	// Phase 3: Alert metrics
	// ActiveAlerts and AlertsTotal by severity level
	for _, level := range []string{"CRITICAL", "WARNING"} {
		alertActiveCount, err := database.GetActiveAlertCount(db, chainID, level)
		if err != nil {
			log.Printf("⚠️ Error getting active alert count for chain %s level %s: %v", chainID, level, err)
			continue
		}
		ActiveAlerts.WithLabelValues(chainID, level).Set(float64(alertActiveCount))

		totalCount, err := database.GetTotalAlertCount(db, chainID, level)
		if err != nil {
			log.Printf("⚠️ Error getting total alert count for chain %s level %s: %v", chainID, level, err)
			continue
		}
		// Set the total count gauge (reflects cumulative alerts from DB)
		AlertsTotal.WithLabelValues(chainID, level).Set(float64(totalCount))
	}

	return nil
}

func StartMetricsUpdater(db *gorm.DB) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("❌ PANIC in StartMetricsUpdater: %v", r)
			}
		}()

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
