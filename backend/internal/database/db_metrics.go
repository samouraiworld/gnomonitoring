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
	startStr, endStr, err := getPeriod(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
	}

	query := fmt.Sprintf(`
		SELECT
			addr,
			moniker,
			ROUND(SUM(participated) * 100.0 / COUNT(*), 1) AS participation_rate
		FROM
			daily_participations
		WHERE
			date >= %s AND date < %s
		GROUP BY
			addr, moniker
		ORDER BY
			participation_rate ASC;
	`, startStr, endStr)

	err = db.Raw(query).Scan(&results).Error
	log.Println(results)

	return results, err
}

// ====================================== Up Time / tx_contrib Metrics ==========================
func OperationTimeMetricsaddr(db *gorm.DB) ([]OperationTimeMetrics, error) {
	var results []OperationTimeMetrics

	query := `
					SELECT
					Moniker,
				dp.addr,
				
				MAX(dp.date) AS last_down_date,
				(
					SELECT MAX(date)
					FROM daily_participations AS d2
					WHERE d2.addr = dp.addr AND d2.participated = 1
				) AS last_up_date,
				round(julianday((
					SELECT MAX(date)
					FROM daily_participations AS d2
					WHERE d2.addr = dp.addr AND d2.participated = 1
				)) - julianday(MAX(dp.date)),1) AS days_diff
				FROM daily_participations AS dp
				WHERE dp.participated = 0
				GROUP BY dp.addr; `

	if err := db.Raw(query).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}

	return results, nil
}
func UptimeMetricsaddr(db *gorm.DB) ([]UptimeMetrics, error) {

	var results []UptimeMetrics

	query := `
					WITH
						bounds AS (
							SELECT (SELECT MAX(block_height) AS max_height FROM daily_participations) AS latest,
							 500 AS window, 
							 ((SELECT MAX(block_height) AS max_height FROM daily_participations)- 500+ 1) AS start_h,
							  (SELECT MAX(block_height) AS max_height FROM daily_participations) AS end_h
						),
						base AS (
							SELECT
							p.moniker,
							p.addr,
							SUM(CASE WHEN p.participated THEN 1 ELSE 0 END) AS ok,
							COUNT(*) AS total
							FROM daily_participations p
							JOIN bounds b
							ON p.block_height BETWEEN b.start_h AND b.end_h
							GROUP BY p.addr
						)
						SELECT
						moniker,
						addr,
					
					
						100.0 * ok / total AS uptime
						FROM base
						ORDER BY uptime ASC;`

	if err := db.Raw(query).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}

	return results, nil
}

func TxContrib(db *gorm.DB, period string) ([]TxContribMetrics, error) {
	var results []TxContribMetrics

	startStr, endStr, err := getPeriod(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
	}

	query := fmt.Sprintf(`
		select 
		moniker,
		addr,
			round((SUM(tx_contribution) * 100.0 / (SELECT SUM(tx_contribution) FROM daily_participations)),1) AS tx_contrib
		from daily_participations
		WHERE
			date >= %s AND date < %s
		group by addr;  `, startStr, endStr)

	if err := db.Raw(query).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request TxContrib: %s", err)
	}

	return results, nil
}

// ===================================== MISSING BLOCK =============================
func MissingBlock(db *gorm.DB, period string) ([]MissingBlockMetrics, error) {
	var results []MissingBlockMetrics

	startStr, endStr, err := getPeriod(period)
	if err != nil {
		log.Printf("Error invalid period %s", err)
	}

	query := fmt.Sprintf(`
		select 
		moniker,
		addr,
		SUM(CASE WHEN participated = 0 THEN 1 ELSE 0 END) AS missing_block
		from daily_participations
		WHERE
			date >= %s AND date < %s
		group by addr;  `, startStr, endStr)

	if err := db.Raw(query).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request TxContrib: %s", err)
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
func getPeriod(period string) (startStr, endStr string, err error) {

	var start, end time.Time
	// var startStr, endStr string
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
		startStr = fmt.Sprintf("'%s'", start.Format("2006-01-02"))
		endStr = fmt.Sprintf("'%s'", end.Format("2006-01-02"))
	case "current_month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)
		startStr = fmt.Sprintf("'%s'", start.Format("2006-01-02"))
		endStr = fmt.Sprintf("'%s'", end.Format("2006-01-02"))

	case "current_year":
		start = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(1, 0, 0)
		startStr = fmt.Sprintf("'%s'", start.Format("2006-01-02"))
		endStr = fmt.Sprintf("'%s'", end.Format("2006-01-02"))

	case "all_time":

		startStr = `(select min(date) from daily_participations)`
		endStr = `(select max(date) from daily_participations)`

	default:
		return "", "", fmt.Errorf("invalid period: %s", period)
	}
	return startStr, endStr, nil
}

func GetTimeOfBlock(db *gorm.DB, numBlock int64) (time.Time, error) {
	var blockTime time.Time

	query := fmt.Sprintf(`
		SELECT DISTINCT date 
		FROM daily_participations 
		WHERE block_height = %d;
	`, numBlock)

	err := db.Raw(query).Scan(&blockTime).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get time of block %d: %w", numBlock, err)
	}

	return blockTime, nil
}
func GetTimeOfAlert(db *gorm.DB, numBlock int64) (time.Time, error) {
	var blockTime time.Time

	query := fmt.Sprintf(`
		SELECT DISTINCT sent_at 
		FROM alert_logs 
		WHERE start_height = %d and end_height =%d;
	`, numBlock, numBlock)

	err := db.Raw(query).Scan(&blockTime).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get time of block %d: %w", numBlock, err)
	}

	return blockTime, nil
}
