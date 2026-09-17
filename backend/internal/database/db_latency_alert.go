package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// LatencyStat is one validator's signing-latency summary over the alert
// window, used by the latency alert loop.
type LatencyStat struct {
	Addr      string   `gorm:"column:addr"`
	Moniker   string   `gorm:"column:moniker"`
	Samples   int      `gorm:"column:samples"`
	P50       float64  `gorm:"column:p50"`
	P90       float64  `gorm:"column:p90"`
	LateRatio *float64 `gorm:"column:late_ratio"`
	MinHeight int64    `gorm:"column:min_height"`
	MaxHeight int64    `gorm:"column:max_height"`
}

// GetLatencyAlertStats returns per-validator lag percentiles and
// late-for-quorum ratio over the last windowMinutes, from the raw rows only
// (aggregates cannot answer percentiles over an arbitrary window). Only
// signed precommits with a recorded lag count as samples. Scoped to chain_id.
func GetLatencyAlertStats(db *gorm.DB, chainID string, windowMinutes int) ([]LatencyStat, error) {
	var results []LatencyStat
	window := fmt.Sprintf("%d minutes", windowMinutes)
	query := `
		SELECT
			dp.addr,
			MAX(COALESCE(am.moniker, dp.addr)) AS moniker,
			COUNT(*) AS samples,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY dp.precommit_lag_ms) AS p50,
			percentile_cont(0.9) WITHIN GROUP (ORDER BY dp.precommit_lag_ms) AS p90,
			(COUNT(*) FILTER (WHERE dp.late_for_quorum))::float8
				/ NULLIF(COUNT(dp.late_for_quorum), 0) AS late_ratio,
			MIN(dp.block_height) AS min_height,
			MAX(dp.block_height) AS max_height
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ?
		  AND dp.date >= NOW() - ?::interval
		  AND dp.precommit_lag_ms IS NOT NULL
		GROUP BY dp.addr`
	if err := db.Raw(query, chainID, window).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("GetLatencyAlertStats(%s): %w", chainID, err)
	}
	return results, nil
}

// LatencyAlertState is the most recent latency alert row for one validator.
type LatencyAlertState struct {
	Addr   string    `gorm:"column:addr"`
	Level  string    `gorm:"column:level"`
	SentAt time.Time `gorm:"column:sent_at"`
}

// GetLatencyAlertStates returns, per validator, the latest LATENCY or
// LATENCY_RESOLVED row for the chain. A validator whose latest row is LATENCY
// currently has an open latency alert. Missed-block levels are excluded, so
// the two alert families never mask each other. Scoped to chain_id.
func GetLatencyAlertStates(db *gorm.DB, chainID string) (map[string]LatencyAlertState, error) {
	var rows []LatencyAlertState
	query := `
		SELECT DISTINCT ON (addr) addr, level, sent_at
		FROM alert_logs
		WHERE chain_id = ? AND level IN ('LATENCY', 'LATENCY_RESOLVED')
		ORDER BY addr, sent_at DESC, id DESC`
	if err := db.Raw(query, chainID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("GetLatencyAlertStates(%s): %w", chainID, err)
	}
	states := make(map[string]LatencyAlertState, len(rows))
	for _, r := range rows {
		states[r.Addr] = r
	}
	return states, nil
}

// GetActiveLatencyAlertCount counts validators whose most recent latency row
// is a LATENCY (i.e. an open latency alert). Scoped to chain_id.
func GetActiveLatencyAlertCount(db *gorm.DB, chainID string) (int, error) {
	var count int
	query := `
		WITH latest AS (
			SELECT DISTINCT ON (addr) addr, level
			FROM alert_logs
			WHERE chain_id = ? AND level IN ('LATENCY', 'LATENCY_RESOLVED')
			ORDER BY addr, sent_at DESC, id DESC
		)
		SELECT COUNT(*) FROM latest WHERE level = 'LATENCY'`
	if err := db.Raw(query, chainID).Scan(&count).Error; err != nil {
		return 0, fmt.Errorf("GetActiveLatencyAlertCount(%s): %w", chainID, err)
	}
	return count, nil
}
