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
	log.Printf("start %s", startStr)
	log.Printf("end %s", endStr)

	result := db.
		Model(&AlertLog{}).
		Select("DISTINCT moniker, level,addr, start_height, end_height,msg,sent_at").
		Order("end_height desc").
		Where("chain_id = ? AND sent_at BETWEEN ? AND ?", chainID, startStr, endStr).
		Limit(10).
		Scan(&alerts)

	return alerts, result.Error
}

func GetCurrentPeriodParticipationRate(db *gorm.DB, chainID, period string) ([]ParticipationRate, error) {
	log.Println("==========Start Get Participate Rate ")
	var results []ParticipationRate
	startStr, endStr, err := getPeriodParams(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
		return nil, err
	}

	query := `
		SELECT
			dp.addr,
			COALESCE(am.moniker, dp.addr) AS moniker,
			ROUND(SUM(dp.participated) * 100.0 / COUNT(*), 1) AS participation_rate
		FROM
			daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE
			dp.chain_id = ? AND dp.date >= ? AND dp.date < ?
		GROUP BY
			dp.addr
		ORDER BY
			participation_rate ASC`

	err = db.Raw(query, chainID, startStr, endStr).Scan(&results).Error

	return results, err
}

// ====================================== Up Time / tx_contrib Metrics ==========================
func OperationTimeMetricsaddr(db *gorm.DB, chainID string) ([]OperationTimeMetrics, error) {
	var results []OperationTimeMetrics

	query := `
		WITH last_down AS (
			SELECT addr, chain_id, MAX(date) AS last_down_date
			FROM daily_participations
			WHERE chain_id = ? AND participated = 0
			GROUP BY chain_id, addr
		),
		last_up AS (
			SELECT addr, chain_id, MAX(date) AS last_up_date
			FROM daily_participations
			WHERE chain_id = ? AND participated = 1
			GROUP BY chain_id, addr
		)
		SELECT
			COALESCE(am.moniker, ld.addr) AS moniker,
			ld.addr,
			ld.last_down_date,
			lu.last_up_date,
			ROUND(julianday(lu.last_up_date) - julianday(ld.last_down_date), 1) AS days_diff
		FROM last_down ld
		LEFT JOIN last_up lu ON lu.chain_id = ld.chain_id AND lu.addr = ld.addr
		LEFT JOIN addr_monikers am ON am.chain_id = ld.chain_id AND am.addr = ld.addr;`

	if err := db.Raw(query, chainID, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}

	return results, nil
}
func UptimeMetricsaddr(db *gorm.DB, chainID string) ([]UptimeMetrics, error) {

	var results []UptimeMetrics

	// Use the actual last 500 scraped blocks per validator instead of a theoretical
	// block_height range. This handles gaps where the scraper was not running and
	// avoids returning 0% uptime due to missing block data.
	query := `
		WITH recent_blocks AS (
			SELECT DISTINCT block_height
			FROM daily_participations
			WHERE chain_id = ?
			ORDER BY block_height DESC
			LIMIT 500
		),
		base AS (
			SELECT
				p.chain_id,
				p.addr,
				SUM(CASE WHEN p.participated THEN 1 ELSE 0 END) AS ok,
				COUNT(*) AS total
			FROM daily_participations p
			INNER JOIN recent_blocks rb ON p.block_height = rb.block_height
			GROUP BY p.chain_id, p.addr
		)
		SELECT
			COALESCE(am.moniker, base.addr) AS moniker,
			base.addr,
			100.0 * ok / total AS uptime
		FROM base
		LEFT JOIN addr_monikers am ON am.chain_id = base.chain_id AND am.addr = base.addr
		ORDER BY uptime ASC`

	if err := db.Raw(query, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}

	return results, nil
}

func TxContrib(db *gorm.DB, chainID, period string) ([]TxContribMetrics, error) {
	var results []TxContribMetrics

	startStr, endStr, err := getPeriodParams(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
		return nil, err
	}

	query := `
		SELECT
			COALESCE(am.moniker, dp.addr) AS moniker,
			dp.addr,
			ROUND((SUM(dp.tx_contribution) * 100.0 / (SELECT SUM(tx_contribution) FROM daily_participations WHERE chain_id = ?)), 1) AS tx_contrib
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE
			dp.chain_id = ? AND dp.date >= ? AND dp.date < ?
		GROUP BY dp.addr`

	if err := db.Raw(query, chainID, chainID, startStr, endStr).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request TxContrib: %s", err)
	}

	return results, nil
}

// ===================================== MISSING BLOCK =============================
func MissingBlock(db *gorm.DB, chainID, period string) ([]MissingBlockMetrics, error) {
	var results []MissingBlockMetrics

	startStr, endStr, err := getPeriodParams(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
		return nil, err
	}

	query := `
		SELECT
			COALESCE(am.moniker, dp.addr) AS moniker,
			dp.addr,
			SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END) AS missing_block
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE
			dp.chain_id = ? AND dp.date >= ? AND dp.date < ?
		GROUP BY dp.addr`

	if err := db.Raw(query, chainID, startStr, endStr).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request MissingBlock: %s", err)
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
		log.Printf("📦 Loaded from DB — Addr: %s, Moniker: %s", e.Addr, e.Moniker)
	}
	log.Printf("✅ Loaded %d monikers from DB", len(monikerMap))
	return monikerMap, nil
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
		SELECT
			dp.addr,
			COALESCE(am.moniker, dp.addr) AS moniker,
			MIN(dp.date) AS first_seen
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ? AND dp.participated = 1
		GROUP BY dp.addr
		ORDER BY first_seen ASC`

	if err := db.Raw(query, chainID).Scan(&results).Error; err != nil {
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
	var count int

	query := `
		SELECT COUNT(DISTINCT addr)
		FROM daily_participations
		WHERE chain_id = ? AND participated = 1
		AND block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 100`

	err := db.Raw(query, chainID, chainID).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("error counting active validators: %w", err)
	}

	return count, nil
}

// GetAvgParticipationRate returns the average participation rate (0-100) across all validators
// in the last 100 blocks of the given chain.
func GetAvgParticipationRate(db *gorm.DB, chainID string) (float64, error) {
	var avgRate sql.NullFloat64

	query := `
		SELECT AVG(CAST(participated AS FLOAT)) * 100
		FROM daily_participations
		WHERE chain_id = ?
		AND block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 100`

	err := db.Raw(query, chainID, chainID).Scan(&avgRate).Error
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

	query := `SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?`

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
func GetActiveAlertCount(db *gorm.DB, chainID, level string) (int, error) {
	var count int

	// An alert is considered "active" if it's the most recent alert for that validator
	// and has not been resolved (no subsequent RESOLVED alert exists).
	// For simplicity: count alert_logs with given level, assuming most recent is active.
	query := `
		SELECT COUNT(DISTINCT addr)
		FROM alert_logs
		WHERE chain_id = ? AND level = ?
		AND sent_at = (
			SELECT MAX(sent_at) FROM alert_logs al2
			WHERE al2.chain_id = alert_logs.chain_id
			AND al2.addr = alert_logs.addr
		)
		AND level IN ('CRITICAL', 'WARNING')`

	err := db.Raw(query, chainID, level).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("error counting active alerts: %w", err)
	}

	return count, nil
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
