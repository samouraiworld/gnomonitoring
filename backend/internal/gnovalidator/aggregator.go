package gnovalidator

import (
	"fmt"
	"log"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"gorm.io/gorm"
)

// rawRetentionDays and aggregatorPeriod are now read from the admin_config table
// via GetThresholds(). Default values: 7 days retention, 60 minute aggregation period.

// StartAggregator runs an immediate aggregation pass then repeats every hour.
// It processes all enabled chains: aggregates complete past days from
// daily_participations into daily_participation_agregas, then prunes raw rows
// older than rawRetentionDays.
func StartAggregator(db *gorm.DB) {
	go func() {
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[aggregator] panic recovered: %v", r)
					}
				}()

				runAggregation(db)

				ticker := time.NewTicker(GetThresholds().AggregatorPeriod())
				defer ticker.Stop()
				for range ticker.C {
					runAggregation(db)
				}
			}()
			// Only reached after a panic — brief pause before restarting.
			log.Printf("[aggregator] restarting after panic")
			time.Sleep(30 * time.Second)
		}
	}()
}

func runAggregation(db *gorm.DB) {
	for _, chainID := range internal.EnabledChains {
		if err := AggregateChain(db, chainID); err != nil {
			log.Printf("[aggregator][%s] aggregation failed: %v", chainID, err)
		}
		if err := PruneRawData(db, chainID); err != nil {
			log.Printf("[aggregator][%s] prune failed: %v", chainID, err)
		}
	}
}

// AggregateChain inserts or updates rows in daily_participation_agregas for all
// complete days (< today UTC) not yet aggregated for the given chain.
// Each day is processed in its own transaction to keep transactions short and
// avoid contending with concurrent writers (realtime loop, moniker refresh, etc.).
func AggregateChain(db *gorm.DB, chainID string) error {
	// Collect the distinct unaggregated days in one read (no write lock).
	var lastDate string
	if err := db.Raw(
		`SELECT COALESCE(MAX(block_date), '0001-01-01') FROM daily_participation_agregas WHERE chain_id = ?`,
		chainID,
	).Scan(&lastDate).Error; err != nil {
		return err
	}

	var days []string
	if err := db.Raw(
		`SELECT DISTINCT date::date AS d
		 FROM daily_participations
		 WHERE chain_id = ? AND date::date > ? AND date::date < CURRENT_DATE
		 ORDER BY d ASC`,
		chainID, lastDate,
	).Scan(&days).Error; err != nil {
		return err
	}

	if len(days) == 0 {
		return nil
	}

	query := `
		INSERT INTO daily_participation_agregas
		  (chain_id, addr, block_date, moniker,
		   participated_count, missed_count, tx_contribution_count, proposed_count,
		   total_blocks, first_block_height, last_block_height)
		SELECT
		  chain_id,
		  addr,
		  date::date                                            AS block_date,
		  MAX(moniker)                                          AS moniker,
		  SUM(CASE WHEN participated     THEN 1 ELSE 0 END)    AS participated_count,
		  SUM(CASE WHEN NOT participated THEN 1 ELSE 0 END)    AS missed_count,
		  SUM(CASE WHEN tx_contribution  THEN 1 ELSE 0 END)    AS tx_contribution_count,
		  SUM(CASE WHEN proposed         THEN 1 ELSE 0 END)    AS proposed_count,
		  COUNT(*)                                             AS total_blocks,
		  MIN(block_height)                                    AS first_block_height,
		  MAX(block_height)                                    AS last_block_height
		FROM daily_participations
		WHERE chain_id = ? AND date::date = ?
		GROUP BY chain_id, addr, date::date
		ON CONFLICT(chain_id, addr, block_date) DO UPDATE SET
		  moniker               = excluded.moniker,
		  participated_count    = excluded.participated_count,
		  missed_count          = excluded.missed_count,
		  tx_contribution_count = excluded.tx_contribution_count,
		  proposed_count        = excluded.proposed_count,
		  total_blocks          = excluded.total_blocks,
		  first_block_height    = excluded.first_block_height,
		  last_block_height     = excluded.last_block_height`

	var totalRows int64
	for _, day := range days {
		result := db.Exec(query, chainID, day)
		if result.Error != nil {
			return fmt.Errorf("aggregate day %s: %w", day, result.Error)
		}
		totalRows += result.RowsAffected
		// Yield briefly between days so other writers (realtime loop, monikers)
		// can make progress without contending with this loop's transactions.
		time.Sleep(5 * time.Millisecond)
	}

	if totalRows > 0 {
		log.Printf("[aggregator][%s] aggregated %d rows over %d days", chainID, totalRows, len(days))
	}
	return nil
}

const pruneBatchSize = 10_000

// PruneRawData deletes rows from daily_participations older than rawRetentionDays
// in batches of pruneBatchSize to keep each DELETE transaction short and avoid
// long-running transactions that could bloat Postgres dead tuples.
func PruneRawData(db *gorm.DB, chainID string) error {
	retentionDays := GetThresholds().RawRetentionDays
	cutoffDays := fmt.Sprintf("%d days", retentionDays) // e.g. "7 days", cast to interval in SQL
	var totalPruned int64

	for {
		result := db.Exec(
			`DELETE FROM daily_participations
			 WHERE id IN (
			   SELECT id FROM daily_participations
			   WHERE chain_id = ? AND date < NOW() - ?::interval
			   ORDER BY id
			   LIMIT ?
			 )`,
			chainID, cutoffDays, pruneBatchSize,
		)
		if result.Error != nil {
			return result.Error
		}
		totalPruned += result.RowsAffected
		if result.RowsAffected < int64(pruneBatchSize) {
			break
		}
	}

	if totalPruned > 0 {
		log.Printf("[aggregator][%s] pruned %d raw rows (older than %d days)", chainID, totalPruned, retentionDays)
	}
	return nil
}
