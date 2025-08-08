package gnovalidator

import (
	"bytes"
	"fmt"
	"log"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"gorm.io/gorm"
)

func SheduleUserReport(userID string, hour, minute int, timezone string, db *gorm.DB, reload <-chan struct{}) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Printf("⚠️ Invalid timezone for user %s: %s, defaulting to UTC", userID, timezone)
		loc = time.UTC
	}

	for {
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if next.Before(now) {
			next = next.Add(24 * time.Hour)
		}
		wait := time.Until(next)

		log.Printf("🕓 Scheduled next report for %s at %s (%s)", userID, next.Format(time.RFC1123), wait)

		select {
		case <-time.After(wait):
			log.Printf("⏰ Sending report for user %s", userID)
			SendDailyStatsForUser(db, userID)
		case <-reload:
			log.Printf("♻️ Reloading schedule for user %s", userID)
			return
		}
	}
}

func SendDailyStatsForUser(db *gorm.DB, userID string) {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	rates, minBlock, maxBlock := CalculateRate(db, yesterday)

	var buffer bytes.Buffer
	buffer.WriteString(fmt.Sprintf("📊 *Daily Summary* for %s (Blocks %d → %d):\n\n", yesterday, minBlock, maxBlock))

	for addr, rate := range rates {
		moniker := MonikerMap[addr]
		if moniker == "" {
			moniker = "unknown"
		}
		emoji := "🟢"
		if rate < 95.0 {
			emoji = "🔴"
		}
		buffer.WriteString(fmt.Sprintf("  %s Validator: %s addr: (%s) rate: %.2f%%\n", emoji, moniker, addr, rate))
	}

	msg := buffer.String()

	err := internal.SendUserReportAlert(userID, msg, db)
	if err != nil {
		log.Printf("[SendDailyStatsForUser] Send error for %s: %v", userID, err)
	}
}

func CalculateRate(db *gorm.DB, date string) (map[string]float64, int64, int64) {
	rates := make(map[string]float64)

	//Step 1: Retrieve the min/max heights
	var minHeight, maxHeight int64
	err := db.Raw(`
		SELECT MIN(block_height), MAX(block_height)
		FROM daily_participations
		WHERE date = ?
	`, date).Row().Scan(&minHeight, &maxHeight)

	if err != nil {
		log.Printf("[CalculateRate] Error retrieving block range: %v", err)
		return rates, 0, 0
	}

	// Step 2: Request for participation
	rows, err := db.Raw(`
		SELECT 
			addr,
			moniker,
			COUNT(*) AS total_blocks,
			SUM(CASE WHEN participated THEN 1 ELSE 0 END) AS participated_blocks
		FROM daily_participations
		WHERE date = ?
		GROUP BY addr, moniker
	`, date).Rows()
	if err != nil {
		log.Printf("[CalculateRate] Error querying participation: %v", err)
		return rates, minHeight, maxHeight
	}
	defer rows.Close()

	// Step 3: Processing the results

	for rows.Next() {
		var addr, moniker string
		var total, participated int

		if err := rows.Scan(&addr, &moniker, &total, &participated); err != nil {
			log.Printf("[CalculateRate] Scan error: %v", err)
			continue
		}

		if total > 0 {
			rates[addr] = float64(participated) / float64(total) * 100
		}
	}

	return rates, minHeight, maxHeight
}

func GetLastStoredHeight(db *gorm.DB) (int64, error) {
	var result struct {
		MaxHeight int64
	}
	err := db.Raw(`SELECT MAX(block_height) AS max_height FROM daily_participations`).Scan(&result).Error
	if err != nil {
		return 0, fmt.Errorf("error reading last stored block: %w", err)
	}
	fmt.Printf("last block: %d\n", result.MaxHeight)
	return result.MaxHeight, nil
}
