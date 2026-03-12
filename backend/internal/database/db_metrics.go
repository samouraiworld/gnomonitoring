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
func InsertAlertlog(db *gorm.DB, addr, moniker, level string, startheight, endheight int64, skipped bool, sent time.Time, msg string) error {
	alert := AlertLog{
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

func GetAlertLog(db *gorm.DB, period string) ([]AlertSummary, error) {
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
		db.Raw(`
				SELECT
					MIN(sent_at),
					MAX(sent_at)
				FROM alert_logs
				`).Row().Scan(&minS, &maxS)

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
		Where("sent_at BETWEEN ? AND ?", startStr, endStr).
		Limit(10).
		Scan(&alerts)

	return alerts, result.Error
}

func GetCurrentPeriodParticipationRate(db *gorm.DB, period string) ([]ParticipationRate, error) {
	log.Println("==========Start Get Participate Rate ")
	var results []ParticipationRate
	startStr, endStr, err := getPeriodParams(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
		return nil, err
	}

	query := `
		SELECT
			addr,
			moniker,
			ROUND(SUM(participated) * 100.0 / COUNT(*), 1) AS participation_rate
		FROM
			daily_participations
		WHERE
			date >= ? AND date < ?
		GROUP BY
			addr, moniker
		ORDER BY
			participation_rate ASC`

	err = db.Raw(query, startStr, endStr).Scan(&results).Error

	return results, err
}

// ====================================== Up Time / tx_contrib Metrics ==========================
func OperationTimeMetricsaddr(db *gorm.DB) ([]OperationTimeMetrics, error) {
	var results []OperationTimeMetrics

	query := `
		WITH last_down AS (
			SELECT addr, moniker, MAX(date) AS last_down_date
			FROM daily_participations
			WHERE participated = 0
			GROUP BY addr
		),
		last_up AS (
			SELECT addr, MAX(date) AS last_up_date
			FROM daily_participations
			WHERE participated = 1
			GROUP BY addr
		)
		SELECT
			ld.moniker,
			ld.addr,
			ld.last_down_date,
			lu.last_up_date,
			ROUND(julianday(lu.last_up_date) - julianday(ld.last_down_date), 1) AS days_diff
		FROM last_down ld
		LEFT JOIN last_up lu ON lu.addr = ld.addr;`

	if err := db.Raw(query).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}

	return results, nil
}
func UptimeMetricsaddr(db *gorm.DB) ([]UptimeMetrics, error) {

	var results []UptimeMetrics

	// Use the actual last 500 scraped blocks per validator instead of a theoretical
	// block_height range. This handles gaps where the scraper was not running and
	// avoids returning 0% uptime due to missing block data.
	query := `
		WITH recent_blocks AS (
			SELECT DISTINCT block_height
			FROM daily_participations
			ORDER BY block_height DESC
			LIMIT 500
		),
		base AS (
			SELECT
				p.moniker,
				p.addr,
				SUM(CASE WHEN p.participated THEN 1 ELSE 0 END) AS ok,
				COUNT(*) AS total
			FROM daily_participations p
			INNER JOIN recent_blocks rb ON p.block_height = rb.block_height
			GROUP BY p.addr
		)
		SELECT
			moniker,
			addr,
			100.0 * ok / total AS uptime
		FROM base
		ORDER BY uptime ASC`

	if err := db.Raw(query).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}

	return results, nil
}

func TxContrib(db *gorm.DB, period string) ([]TxContribMetrics, error) {
	var results []TxContribMetrics

	startStr, endStr, err := getPeriodParams(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
		return nil, err
	}

	query := `
		SELECT
			moniker,
			addr,
			ROUND((SUM(tx_contribution) * 100.0 / (SELECT SUM(tx_contribution) FROM daily_participations)), 1) AS tx_contrib
		FROM daily_participations
		WHERE
			date >= ? AND date < ?
		GROUP BY addr`

	if err := db.Raw(query, startStr, endStr).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request TxContrib: %s", err)
	}

	return results, nil
}

// ===================================== MISSING BLOCK =============================
func MissingBlock(db *gorm.DB, period string) ([]MissingBlockMetrics, error) {
	var results []MissingBlockMetrics

	startStr, endStr, err := getPeriodParams(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
		return nil, err
	}

	query := `
		SELECT
			moniker,
			addr,
			SUM(CASE WHEN participated = 0 THEN 1 ELSE 0 END) AS missing_block
		FROM daily_participations
		WHERE
			date >= ? AND date < ?
		GROUP BY addr`

	if err := db.Raw(query, startStr, endStr).Scan(&results).Error; err != nil {
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
func GetMoniker(db *gorm.DB) (map[string]string, error) {
	var entries []AddrMoniker
	result := db.Find(&entries)
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
func GetFirstSeen(db *gorm.DB) ([]FirstSeenMetrics, error) {
	var results []FirstSeenMetrics

	query := `
		SELECT
			addr,
			moniker,
			MIN(date) AS first_seen
		FROM daily_participations
		WHERE participated = 1
		GROUP BY addr
		ORDER BY first_seen ASC`

	if err := db.Raw(query).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request FirstSeen: %s", err)
	}

	return results, nil
}

func GetTimeOfBlock(db *gorm.DB, numBlock int64) (time.Time, error) {
	var blockTime time.Time

	err := db.Raw(`
		SELECT DISTINCT date
		FROM daily_participations
		WHERE block_height = ?
	`, numBlock).Scan(&blockTime).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get time of block %d: %w", numBlock, err)
	}

	return blockTime, nil
}
func GetTimeOfAlert(db *gorm.DB, numBlock int64) (time.Time, error) {
	var blockTime time.Time

	err := db.Raw(`
		SELECT DISTINCT sent_at
		FROM alert_logs
		WHERE start_height = ? AND end_height = ?
	`, numBlock, numBlock).Scan(&blockTime).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get time of block %d: %w", numBlock, err)
	}

	return blockTime, nil
}
