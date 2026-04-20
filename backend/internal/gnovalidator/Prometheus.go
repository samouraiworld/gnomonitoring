package gnovalidator

import (
	"context"
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

	MissedBlocksWindow = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_missed_blocks_window",
			Help: "Number of blocks missed by a validator in the given time window (1h, 24h, 7d)",
		},
		[]string{"chain", "validator_address", "moniker", "window"},
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

	// Phase 4: RPC-enriched metrics
	ValidatorVotingPower = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_validator_voting_power",
			Help: "Current voting power of each validator",
		},
		[]string{"chain", "validator_address", "moniker"},
	)

	ChainPeerCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_chain_peer_count",
			Help: "Number of connected peers",
		},
		[]string{"chain"},
	)

	ChainMempoolTxCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_chain_mempool_tx_count",
			Help: "Pending transactions in mempool",
		},
		[]string{"chain"},
	)

	ChainValsetSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gnoland_chain_valset_size",
			Help: "Number of active validators in current set",
		},
		[]string{"chain"},
	)

	initOnce sync.Once
)

func Init() {
	initOnce.Do(func() {
		prometheus.MustRegister(ValidatorParticipation)
		prometheus.MustRegister(MissedBlocks)
		prometheus.MustRegister(ConsecutiveMissedBlocks)
		prometheus.MustRegister(MissedBlocksWindow)
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
		// Phase 4: RPC-enriched metrics
		prometheus.MustRegister(ValidatorVotingPower)
		prometheus.MustRegister(ChainPeerCount)
		prometheus.MustRegister(ChainMempoolTxCount)
		prometheus.MustRegister(ChainValsetSize)
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

func UpdatePrometheusMetricsFromDB(db *gorm.DB, chainID string, ctxOpts ...context.Context) error {
	// Accept optional context; default to background if not provided.
	ctx := context.Background()
	if len(ctxOpts) > 0 && ctxOpts[0] != nil {
		ctx = ctxOpts[0]
	}

	// Use context-aware DB for all queries so they abort on timeout/cancellation.
	db = db.WithContext(ctx)

	log.Printf("[metrics][%s] updating", chainID)

	// Blocker 1 fix: Delete stale per-validator metrics for this chain before
	// re-populating. If a validator disappeared since the last cycle, its old
	// gauge value is removed instead of lingering forever.
	chainLabel := prometheus.Labels{"chain": chainID}
	ValidatorParticipation.DeletePartialMatch(chainLabel)
	ValidatorUptime.DeletePartialMatch(chainLabel)
	ValidatorOperationTime.DeletePartialMatch(chainLabel)
	ValidatorTxContribution.DeletePartialMatch(chainLabel)
	ValidatorMissingBlocksMonth.DeletePartialMatch(chainLabel)
	ValidatorFirstSeenUnix.DeletePartialMatch(chainLabel)
	MissedBlocks.DeletePartialMatch(chainLabel)
	ConsecutiveMissedBlocks.DeletePartialMatch(chainLabel)
	MissedBlocksWindow.DeletePartialMatch(chainLabel)

	// Phase 1: Base validator metrics (non-critical errors logged, execution continues)

	// ValidatorParticipation (current calendar month)
	participationRates, err := database.GetCurrentPeriodParticipationRate(db, chainID, "current_month")
	if err != nil {
		log.Printf("[metrics][%s] ValidatorParticipation error: %v", chainID, err)
	} else {
		for _, stat := range participationRates {
			ValidatorParticipation.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(stat.ParticipationRate)
		}
	}

	// MissedBlocks
	missedStats, err := CalculateMissedBlocks(db, chainID)
	if err != nil {
		log.Printf("[metrics][%s] MissedBlocks error: %v", chainID, err)
	} else {
		for _, stat := range missedStats {
			MissedBlocks.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(float64(stat.Missed))
		}
	}

	// ConsecutiveMissedBlocks
	consecutiveStats, err := CalculateConsecutiveMissedBlocks(db, chainID)
	if err != nil {
		log.Printf("[metrics][%s] ConsecutiveMissedBlocks error: %v", chainID, err)
	} else {
		for _, stat := range consecutiveStats {
			ConsecutiveMissedBlocks.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(float64(stat.Count))
		}
	}

	// MissedBlocksWindow (1h, 24h, 7d)
	windows := map[string]time.Duration{
		"1h":  time.Hour,
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
	}
	for windowLabel, dur := range windows {
		since := time.Now().Add(-dur)
		windowStats, err := database.GetMissedBlocksWindow(db, chainID, since)
		if err != nil {
			log.Printf("[metrics][%s] MissedBlocksWindow[%s] error: %v", chainID, windowLabel, err)
			continue
		}
		for _, stat := range windowStats {
			MissedBlocksWindow.WithLabelValues(chainID, stat.Addr, stat.Moniker, windowLabel).Set(float64(stat.MissingBlock))
		}
	}

	// ValidatorUptime (last 500 blocks)
	uptimeStats, err := database.UptimeMetricsaddr(db, chainID)
	if err != nil {
		log.Printf("[metrics][%s] ValidatorUptime error: %v", chainID, err)
	} else {
		for _, stat := range uptimeStats {
			ValidatorUptime.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(stat.Uptime)
		}
	}

	// ValidatorOperationTime (days since last down)
	operationStats, err := database.OperationTimeMetricsaddr(db, chainID)
	if err != nil {
		log.Printf("[metrics][%s] OperationTime error: %v", chainID, err)
	} else {
		for _, stat := range operationStats {
			ValidatorOperationTime.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(stat.DaysDiff)
		}
	}

	// ValidatorTxContribution (current month)
	txStats, err := database.TxContrib(db, chainID, "current_month")
	if err != nil {
		log.Printf("[metrics][%s] TxContribution error: %v", chainID, err)
	} else {
		allZero := true
		for _, s := range txStats {
			if s.TxContrib > 0 {
				allZero = false
				break
			}
		}
		if len(txStats) > 0 && allZero {
			log.Printf("[metrics][%s] TxContribution: all zero — proposer data may be missing", chainID)
		}
		for _, stat := range txStats {
			ValidatorTxContribution.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(stat.TxContrib)
		}
	}

	// ValidatorMissingBlocksMonth (current month)
	missingStats, err := database.MissingBlock(db, chainID, "current_month")
	if err != nil {
		log.Printf("[metrics][%s] MissingBlocksMonth error: %v", chainID, err)
	} else {
		for _, stat := range missingStats {
			ValidatorMissingBlocksMonth.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(float64(stat.MissingBlock))
		}
	}

	// ValidatorFirstSeenUnix (unix timestamp of first participation)
	firstSeenStats, err := database.GetFirstSeen(db, chainID)
	if err != nil {
		log.Printf("[metrics][%s] FirstSeen error: %v", chainID, err)
	} else {
		for _, stat := range firstSeenStats {
			// Parse the date string returned from SQL (format: "YYYY-MM-DD HH:MM:SS" or with timezone)
			var t time.Time
			layouts := []string{
				"2006-01-02 15:04:05-07:00", // with timezone
				"2006-01-02 15:04:05",       // without timezone
				"2006-01-02",                // date only (from daily_participation_agregas.block_date)
			}
			var parseErr error
			for _, layout := range layouts {
				t, parseErr = time.Parse(layout, stat.FirstSeen)
				if parseErr == nil {
					break
				}
			}
			if parseErr != nil {
				log.Printf("[metrics][%s] failed to parse first_seen date for %s: %v", chainID, stat.Addr, parseErr)
				continue
			}
			ValidatorFirstSeenUnix.WithLabelValues(chainID, stat.Addr, stat.Moniker).Set(float64(t.Unix()))
		}
	}

	// Phase 2: Chain-level metrics

	activeCount, err := database.GetActiveValidatorCount(db, chainID)
	if err != nil {
		log.Printf("[metrics][%s] ActiveValidatorCount error: %v", chainID, err)
	} else {
		ChainActiveValidators.WithLabelValues(chainID).Set(float64(activeCount))
	}

	avgRate, err := database.GetAvgParticipationRate(db, chainID)
	if err != nil {
		log.Printf("[metrics][%s] AvgParticipation error: %v", chainID, err)
	} else {
		ChainAvgParticipationRate.WithLabelValues(chainID).Set(avgRate)
	}

	height, err := database.GetCurrentChainHeight(db, chainID)
	if err != nil {
		log.Printf("[metrics][%s] CurrentHeight error: %v", chainID, err)
	} else {
		ChainCurrentHeight.WithLabelValues(chainID).Set(float64(height))
	}

	// Phase 3: Alert metrics
	for _, level := range []string{"CRITICAL", "WARNING"} {
		alertActiveCount, err := database.GetActiveAlertCount(db, chainID, level)
		if err != nil {
			log.Printf("[metrics][%s] ActiveAlerts[%s] error: %v", chainID, level, err)
			continue
		}
		ActiveAlerts.WithLabelValues(chainID, level).Set(float64(alertActiveCount))

		totalCount, err := database.GetTotalAlertCount(db, chainID, level)
		if err != nil {
			log.Printf("[metrics][%s] TotalAlerts[%s] error: %v", chainID, level, err)
			continue
		}
		AlertsTotal.WithLabelValues(chainID, level).Set(float64(totalCount))
	}

	// Phase 4: RPC-enriched metrics (voting power, peer count, mempool, valset size)
	snap := FetchChainHealthSnapshot(db, chainID)
	if snap.RPCReachable {
		monikerMap := GetMonikerMap(chainID)

		ValidatorVotingPower.DeletePartialMatch(chainLabel)
		for _, v := range snap.ValidatorSet {
			moniker := monikerMap[v.Address]
			ValidatorVotingPower.WithLabelValues(chainID, v.Address, moniker).Set(float64(v.VotingPower))
		}

		ChainPeerCount.WithLabelValues(chainID).Set(float64(snap.PeerCount))
		ChainMempoolTxCount.WithLabelValues(chainID).Set(float64(snap.MempoolTxCount))
		ChainValsetSize.WithLabelValues(chainID).Set(float64(len(snap.ValidatorSet)))
	} else {
		log.Printf("[metrics][%s] RPC unreachable, skipping Phase 4 metrics", chainID)
	}

	log.Printf("[metrics][%s] update complete", chainID)
	return nil
}

// metricsTimeout is the maximum duration allowed for a full metrics update cycle
// (all chains combined). With SetMaxOpenConns(1) all chains are serialised on a
// single connection, so a per-chain timeout would cause later chains to time out
// while waiting in the pool queue. A single global timeout is simpler and correct.
const metricsTimeout = 10 * time.Minute

func StartMetricsUpdater(db *gorm.DB) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[metrics] panic recovered: %v", r)
			}
		}()

		log.Printf("[metrics] started, chains: %v", internal.EnabledChains)

		for {
			ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)

			for _, chainID := range internal.EnabledChains {
				func(cid string) {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[metrics][%s] panic recovered: %v", cid, r)
						}
					}()

					if err := UpdatePrometheusMetricsFromDB(db, cid, ctx); err != nil {
						if ctx.Err() != nil {
							log.Printf("[metrics] cycle timed out after %v, remaining chains skipped", metricsTimeout)
						} else {
							log.Printf("[metrics][%s] update failed: %v", cid, err)
						}
					}
				}(chainID)

				// Stop processing remaining chains if the cycle deadline is exceeded.
				if ctx.Err() != nil {
					break
				}
			}

			cancel()
			time.Sleep(5 * time.Minute)
		}
	}()
}
