package database

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ====================================== ALERT LOG ======================================
func InsertAlertlog(db *gorm.DB, chainID, addr, moniker, level string, startheight, endheight int64, skipped bool, sent time.Time, msg string) error {
	alert := AlertLog{
		ChainID:     chainID,
		Addr:        addr,
		Moniker:     moniker,
		Level:       level,
		StartHeight: startheight,
		EndHeight:   endheight,
		Skipped:     skipped,
		Msg:         msg,
		SentAt:      sent,
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&alert).Error
}

func GetAlertLog(db *gorm.DB, chainID, period string) ([]AlertSummary, error) {
	var alerts []AlertSummary

	var start, end time.Time
	now := time.Now()

	switch period {
	case "current_week":
		today := time.Now()
		weekday := int(today.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday => 7
		}
		start = today.AddDate(0, 0, -weekday+1) // Return to last Monday
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
		end = start.AddDate(0, 0, 7)
	case "current_month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)

	case "current_year":
		start = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(1, 0, 0)

	case "all_time":
		var minS, maxS sql.NullString
		const layout = "2006-01-02 15:04:05.999999999-07:00"
		if err := db.Raw(`
				SELECT
					MIN(sent_at),
					MAX(sent_at)
				FROM alert_logs
				WHERE chain_id = ?
				`, chainID).Row().Scan(&minS, &maxS); err != nil {
			return nil, fmt.Errorf("error scanning alert log bounds: %w", err)
		}

		startf, err := time.Parse(layout, minS.String)
		if err != nil {
			return nil, fmt.Errorf("error get date max alertLog %w", err)
		}
		start = startf
		endf, err := time.Parse(layout, maxS.String)
		if err != nil {
			return nil, fmt.Errorf("error get date min alertLog %w", err)
		}
		end = endf.AddDate(1, 0, 0)

	default:
		return nil, fmt.Errorf("invalid period: %s", period)
	}

	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	result := db.
		Model(&AlertLog{}).
		Select("DISTINCT moniker, level,addr, start_height, end_height,msg,sent_at").
		Order("end_height desc").
		Where("chain_id = ? AND sent_at BETWEEN ? AND ?", chainID, startStr, endStr).
		Limit(10).
		Scan(&alerts)

	return alerts, result.Error
}

// GetAlertLogsLast24h returns all alert_logs rows sent in the last 24 hours
// for the given chain, ordered by sent_at DESC. Includes WARNING, CRITICAL,
// and RESOLVED rows. Limits to 50 rows to cap message size.
func GetAlertLogsLast24h(db *gorm.DB, chainID string) ([]AlertSummary, error) {
	var results []AlertSummary
	err := db.Raw(`
		SELECT moniker, addr, level, start_height, end_height, msg, sent_at
		FROM alert_logs
		WHERE chain_id = ?
		  AND sent_at >= datetime('now', '-24 hours')
		  AND level IN ('WARNING', 'CRITICAL', 'RESOLVED')
		ORDER BY sent_at DESC
		LIMIT 50
	`, chainID).Scan(&results).Error
	return results, err
}

func GetCurrentPeriodParticipationRate(db *gorm.DB, chainID, period string) ([]ParticipationRate, error) {
	var results []ParticipationRate
	startStr, _, err := getPeriodParams(period)
	if err != nil {
		log.Printf("[db] invalid period: %v", err)
		return nil, err
	}

	// Three-way UNION:
	//   1. Agrega — past complete days (fast path, production)
	//   2. Raw fallback — past days not yet in agrega (tests + new chains + today if agrega lags)
	//   3. Raw today — current day always from raw (never aggregated yet)
	query := `
		SELECT combined.addr,
			COALESCE(am.moniker, combined.addr) AS moniker,
			ROUND(SUM(combined.participated_count) * 100.0 / NULLIF(SUM(combined.total_blocks), 0), 1) AS participation_rate
		FROM (
			SELECT chain_id, addr, participated_count, total_blocks
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ? AND block_date < DATE('now')
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				COUNT(*)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date = DATE(dp.date)
			WHERE dp.chain_id = ? AND dp.date >= ? AND DATE(dp.date) < DATE('now') AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				COUNT(*)
			FROM daily_participations dp
			WHERE dp.chain_id = ? AND DATE(dp.date) = DATE('now')
			GROUP BY dp.chain_id, dp.addr
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr
		ORDER BY participation_rate ASC`

	err = db.Raw(query, chainID, startStr, chainID, startStr, chainID).Scan(&results).Error

	return results, err
}

// ====================================== Up Time / tx_contrib Metrics ==========================
func OperationTimeMetricsaddr(db *gorm.DB, chainID string) ([]OperationTimeMetrics, error) {
	var results []OperationTimeMetrics

	// Combines agrega (complete past days) with raw fallback (days not yet aggregated).
	query := `
		WITH last_down AS (
			SELECT chain_id, addr, MAX(last_down) AS last_down_date FROM (
				SELECT chain_id, addr, block_date AS last_down
				FROM daily_participation_agregas WHERE chain_id = ? AND missed_count > 0
				UNION ALL
				SELECT dp.chain_id, dp.addr, DATE(dp.date) AS last_down
				FROM daily_participations dp
				LEFT JOIN daily_participation_agregas dpa
					ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date = DATE(dp.date)
				WHERE dp.chain_id = ? AND dp.participated = 0 AND dpa.block_date IS NULL
			) GROUP BY chain_id, addr
		),
		last_up AS (
			SELECT chain_id, addr, MAX(last_up) AS last_up_date FROM (
				SELECT chain_id, addr, block_date AS last_up
				FROM daily_participation_agregas WHERE chain_id = ? AND participated_count > 0
				UNION ALL
				SELECT dp.chain_id, dp.addr, DATE(dp.date) AS last_up
				FROM daily_participations dp
				LEFT JOIN daily_participation_agregas dpa
					ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date = DATE(dp.date)
				WHERE dp.chain_id = ? AND dp.participated = 1 AND dpa.block_date IS NULL
			) GROUP BY chain_id, addr
		)
		SELECT
			COALESCE(am.moniker, ld.addr) AS moniker,
			ld.addr,
			ld.last_down_date,
			lu.last_up_date,
			ROUND(julianday(lu.last_up_date) - julianday(ld.last_down_date), 1) AS days_diff
		FROM last_down ld
		LEFT JOIN last_up lu ON lu.chain_id = ld.chain_id AND lu.addr = ld.addr
		LEFT JOIN addr_monikers am ON am.chain_id = ld.chain_id AND am.addr = ld.addr`

	if err := db.Raw(query, chainID, chainID, chainID, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}

	return results, nil
}
func UptimeMetricsaddr(db *gorm.DB, chainID string) ([]UptimeMetrics, error) {
	var results []UptimeMetrics
	query := `
		SELECT combined.addr,
			COALESCE(am.moniker, combined.addr) AS moniker,
			100.0 * SUM(combined.participated_count) / NULLIF(SUM(combined.total_blocks), 0) AS uptime
		FROM (
			SELECT chain_id, addr, participated_count, total_blocks
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= date('now', '-30 days') AND block_date < DATE('now')
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				COUNT(*)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date = DATE(dp.date)
			WHERE dp.chain_id = ? AND dp.date >= date('now', '-30 days') AND DATE(dp.date) < DATE('now')
				AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				COUNT(*)
			FROM daily_participations dp
			WHERE dp.chain_id = ? AND DATE(dp.date) = DATE('now')
			GROUP BY dp.chain_id, dp.addr
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr
		ORDER BY uptime ASC`
	if err := db.Raw(query, chainID, chainID, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}
	return results, nil
}

func TxContrib(db *gorm.DB, chainID, period string) ([]TxContribMetrics, error) {
	var results []TxContribMetrics

	startStr, _, err := getPeriodParams(period)
	if err != nil {
		log.Printf("[db] invalid period: %v", err)
		return nil, err
	}

	query := `
		WITH combined AS (
			SELECT chain_id, addr, tx_contribution_count
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ? AND block_date < DATE('now')
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				SUM(CASE WHEN dp.tx_contribution THEN 1 ELSE 0 END)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date = DATE(dp.date)
			WHERE dp.chain_id = ? AND dp.date >= ? AND DATE(dp.date) < DATE('now') AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				SUM(CASE WHEN dp.tx_contribution THEN 1 ELSE 0 END)
			FROM daily_participations dp
			WHERE dp.chain_id = ? AND DATE(dp.date) = DATE('now')
			GROUP BY dp.chain_id, dp.addr
		),
		total AS (SELECT NULLIF(SUM(tx_contribution_count), 0) AS total_tx FROM combined)
		SELECT combined.addr,
			COALESCE(am.moniker, combined.addr) AS moniker,
			ROUND(SUM(combined.tx_contribution_count) * 100.0 / (SELECT total_tx FROM total), 1) AS tx_contrib
		FROM combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr`

	if err := db.Raw(query, chainID, startStr, chainID, startStr, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request TxContrib: %s", err)
	}

	return results, nil
}

// ===================================== MISSING BLOCK =============================
func MissingBlock(db *gorm.DB, chainID, period string) ([]MissingBlockMetrics, error) {
	var results []MissingBlockMetrics

	startStr, _, err := getPeriodParams(period)
	if err != nil {
		log.Printf("[db] invalid period: %v", err)
		return nil, err
	}

	query := `
		SELECT combined.addr,
			COALESCE(am.moniker, combined.addr) AS moniker,
			SUM(combined.missed_count) AS missing_block
		FROM (
			SELECT chain_id, addr, missed_count
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ? AND block_date < DATE('now')
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date = DATE(dp.date)
			WHERE dp.chain_id = ? AND dp.date >= ? AND DATE(dp.date) < DATE('now') AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END)
			FROM daily_participations dp
			WHERE dp.chain_id = ? AND DATE(dp.date) = DATE('now')
			GROUP BY dp.chain_id, dp.addr
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr`

	if err := db.Raw(query, chainID, startStr, chainID, startStr, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request MissingBlock: %s", err)
	}

	return results, nil
}

// GetMissedBlocksWindow returns the count of missed blocks per validator
// within the given time window (since = time.Now() - duration).
func GetMissedBlocksWindow(db *gorm.DB, chainID string, since time.Time) ([]MissingBlockMetrics, error) {
	var results []MissingBlockMetrics
	sinceStr := since.UTC().Format("2006-01-02 15:04:05")
	query := `
		SELECT
			COALESCE(am.moniker, dp.addr) AS moniker,
			dp.addr,
			SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END) AS missing_block
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ?
		  AND dp.date >= ?
		GROUP BY dp.addr`
	if err := db.Raw(query, chainID, sinceStr).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in GetMissedBlocksWindow: %w", err)
	}
	return results, nil
}

// ====================================== ADDR MONIKER =============================

func InsertAddrMoniker(db *gorm.DB, addr, moniker string) error {
	addrmoniker := AddrMoniker{

		Addr:    addr,
		Moniker: moniker,
	}
	return db.Create(&addrmoniker).Error
}
func GetMoniker(db *gorm.DB, chainID string) (map[string]string, error) {
	var entries []AddrMoniker
	result := db.Where("chain_id = ?", chainID).Find(&entries)
	if result.Error != nil {
		return nil, result.Error
	}

	monikerMap := make(map[string]string)
	for _, e := range entries {
		monikerMap[e.Addr] = e.Moniker
	}
	log.Printf("[db] loaded %d monikers", len(monikerMap))
	return monikerMap, nil
}

// GetFirstActiveBlocksMap returns a map of addr -> first_active_block for a chain.
func GetFirstActiveBlocksMap(db *gorm.DB, chainID string) (map[string]int64, error) {
	var entries []AddrMoniker
	result := db.Where("chain_id = ?", chainID).Find(&entries)
	if result.Error != nil {
		return nil, result.Error
	}
	fabMap := make(map[string]int64, len(entries))
	for _, e := range entries {
		fabMap[e.Addr] = e.FirstActiveBlock
	}
	return fabMap, nil
}

// getPeriodParams returns parameterized date boundaries for a given period.
// Returns date strings suitable for use as GORM query parameters (not for fmt.Sprintf).
func getPeriodParams(period string) (startStr, endStr string, err error) {
	var start, end time.Time
	now := time.Now()

	switch period {
	case "current_week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday => 7
		}
		start = now.AddDate(0, 0, -weekday+1) // Return to last Monday
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
		end = start.AddDate(0, 0, 7)

	case "current_month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)

	case "current_year":
		start = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(1, 0, 0)

	case "all_time":
		// Use extreme bounds to cover all data
		start = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		end = now.AddDate(1, 0, 0)

	default:
		return "", "", fmt.Errorf("invalid period: %s", period)
	}

	startStr = start.Format("2006-01-02")
	endStr = end.Format("2006-01-02")
	return startStr, endStr, nil
}

// ====================================== First Seen ======================================

// GetFirstSeen returns the earliest participation date for each validator.
// This can be used as a "start time" / "first seen" metric.
func GetFirstSeen(db *gorm.DB, chainID string) ([]FirstSeenMetrics, error) {
	var results []FirstSeenMetrics

	query := `
		SELECT combined.addr,
			COALESCE(am.moniker, combined.addr) AS moniker,
			MIN(combined.first_seen) AS first_seen
		FROM (
			SELECT chain_id, addr, block_date AS first_seen
			FROM daily_participation_agregas
			WHERE chain_id = ? AND participated_count > 0
			UNION ALL
			SELECT dp.chain_id, dp.addr, DATE(dp.date) AS first_seen
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date = DATE(dp.date)
			WHERE dp.chain_id = ? AND dp.participated = 1 AND dpa.block_date IS NULL
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr
		ORDER BY first_seen ASC`

	if err := db.Raw(query, chainID, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request FirstSeen: %s", err)
	}

	return results, nil
}

func GetTimeOfBlock(db *gorm.DB, chainID string, numBlock int64) (time.Time, error) {
	var blockTime time.Time

	err := db.Raw(`
		SELECT DISTINCT date
		FROM daily_participations
		WHERE chain_id = ? AND block_height = ?
	`, chainID, numBlock).Scan(&blockTime).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get time of block %d: %w", numBlock, err)
	}

	return blockTime, nil
}
func GetTimeOfAlert(db *gorm.DB, chainID string, numBlock int64) (time.Time, error) {
	var blockTime time.Time

	err := db.Raw(`
		SELECT DISTINCT sent_at
		FROM alert_logs
		WHERE chain_id = ? AND start_height = ? AND end_height = ?
	`, chainID, numBlock, numBlock).Scan(&blockTime).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get time of block %d: %w", numBlock, err)
	}

	return blockTime, nil
}

// ====================================== CHAIN HEALTH METRICS ==============================

// GetActiveValidatorCount returns the count of validators with at least 1 participation
// in the last 100 blocks scanned for the given chain.
func GetActiveValidatorCount(db *gorm.DB, chainID string) (int, error) {
	var maxHeight int64
	if err := db.Raw(`SELECT COALESCE(MAX(block_height), 0) FROM daily_participations WHERE chain_id = ?`, chainID).Scan(&maxHeight).Error; err != nil {
		return 0, fmt.Errorf("error fetching max height for active count: %w", err)
	}
	if maxHeight == 0 {
		return 0, nil
	}

	var count int
	query := `
		SELECT COUNT(DISTINCT addr) AS count
		FROM daily_participations
		WHERE chain_id = ? AND participated = 1
		AND block_height > ?`

	err := db.Raw(query, chainID, maxHeight-100).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("error counting active validators: %w", err)
	}

	return count, nil
}

// GetAvgParticipationRate returns the average participation rate (0-100) across all validators
// in the last 100 blocks of the given chain.
func GetAvgParticipationRate(db *gorm.DB, chainID string) (float64, error) {
	var maxHeight int64
	if err := db.Raw(`SELECT COALESCE(MAX(block_height), 0) FROM daily_participations WHERE chain_id = ?`, chainID).Scan(&maxHeight).Error; err != nil {
		return 0.0, fmt.Errorf("error fetching max height for avg rate: %w", err)
	}
	if maxHeight == 0 {
		return 0.0, nil
	}

	var avgRate sql.NullFloat64
	query := `
		SELECT AVG(CAST(participated AS FLOAT)) * 100 AS avg_rate
		FROM daily_participations
		WHERE chain_id = ?
		AND block_height > ?`

	err := db.Raw(query, chainID, maxHeight-100).Scan(&avgRate).Error
	if err != nil {
		return 0.0, fmt.Errorf("error calculating avg participation rate: %w", err)
	}

	if !avgRate.Valid {
		return 0.0, nil
	}

	return avgRate.Float64, nil
}

// GetCurrentChainHeight returns the latest block height for the given chain.
func GetCurrentChainHeight(db *gorm.DB, chainID string) (int64, error) {
	var height sql.NullInt64

	query := `SELECT MAX(block_height) AS height FROM daily_participations WHERE chain_id = ?`

	err := db.Raw(query, chainID).Scan(&height).Error
	if err != nil {
		return 0, fmt.Errorf("error getting current chain height: %w", err)
	}

	if !height.Valid {
		return 0, nil
	}

	return height.Int64, nil
}

// ====================================== ALERT METRICS ==============================

// GetActiveAlertCount returns the count of currently active alerts (unresolved)
// with the given severity level for the given chain.
// Active = most recent alert for that validator has the given level.
func GetActiveAlertCount(db *gorm.DB, chainID, level string) (int, error) {
	var count int

	// Optimized: Use CTE to find latest alert per validator
	query := `
		WITH latest_alerts AS (
			SELECT addr, MAX(sent_at) as last_sent
			FROM alert_logs
			WHERE chain_id = ?
			GROUP BY addr
		)
		SELECT COUNT(*) as count
		FROM alert_logs al
		INNER JOIN latest_alerts la ON al.addr = la.addr AND al.sent_at = la.last_sent
		WHERE al.chain_id = ? AND al.level = ?`

	err := db.Raw(query, chainID, chainID, level).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("error counting active alerts: %w", err)
	}

	return count, nil
}

// MissedBlockCount holds the count of missed blocks per validator in a time window.
type MissedBlockCount struct {
	Addr    string
	Moniker string
	Missed  int
}

// GetMissedBlocksLast24h returns the count of missed blocks per validator in the
// last 24 hours for the given chain, ordered by missed count descending.
func GetMissedBlocksLast24h(db *gorm.DB, chainID string) ([]MissedBlockCount, error) {
	var result []MissedBlockCount
	err := db.Raw(`
		SELECT addr, MAX(moniker) AS moniker, COUNT(*) AS missed
		FROM daily_participations
		WHERE chain_id = ?
		  AND participated = 0
		  AND date >= datetime('now', '-24 hours')
		GROUP BY addr
		ORDER BY missed DESC
	`, chainID).Scan(&result).Error
	return result, err
}

// GetTotalAlertCount returns the total count of alerts with the given level for the given chain.
func GetTotalAlertCount(db *gorm.DB, chainID, level string) (int64, error) {
	var count int64

	query := `SELECT COUNT(*) FROM alert_logs WHERE chain_id = ? AND level = ?`

	err := db.Raw(query, chainID, level).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("error counting total alerts: %w", err)
	}

	return count, nil
}
