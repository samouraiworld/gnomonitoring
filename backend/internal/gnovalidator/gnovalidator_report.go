package gnovalidator

import (
	"bytes"
	"fmt"
	"log"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"gorm.io/gorm"
)

// func StartDailyReports(db *sql.DB) {
// 	rows, err := db.Query("SELECT user_id, daily_report_hour, daily_report_minute, timezone FROM hour_report")
// 	if err != nil {
// 		log.Fatalf("❌ Failed to fetch report hours: %v", err)
// 		return
// 	}
// 	defer rows.Close()

// 	for rows.Next() {
// 		var userID, tz string
// 		var hour, minute int
// 		if err := rows.Scan(&userID, &hour, &minute, &tz); err != nil {
// 			log.Printf("⚠️ Error scanning user report config: %v", err)
// 			continue
// 		}

//			go scheduleUserReport(db, userID, hour, minute, tz)
//		}
//	}
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
			return // on sort : l’appelant relancera avec les nouvelles données
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
			moniker = "inconnu"
		}
		emoji := "🟢"
		if rate < 95.0 {
			emoji = "🔴"
		}
		buffer.WriteString(fmt.Sprintf("  %s Validator: %s addr: (%s) rate: %.2f%%\n", emoji, moniker, addr, rate))
	}

	msg := buffer.String()

	// Appel à ta méthode d'envoi (Discord, Slack)
	err := internal.SendUserReportAlert(userID, msg, db)
	if err != nil {
		log.Printf("[SendDailyStatsForUser] Send error for %s: %v", userID, err)
	}
}

// func SendDailyStats(db *sql.DB) {
// 	MonikerMutex.RLock()
// 	defer MonikerMutex.RUnlock()

// 	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
// 	rates, minblock, maxblock := CalculateRate(db, yesterday)
// 	// rates, minblock, maxblock := CalculateRate(db, "2025-07-14") // for test
// 	var buffer bytes.Buffer
// 	buffer.WriteString(fmt.Sprintf("📊 *Daily Participation Summary* for %s (Blocks %d → %d):\n\n", yesterday, minblock, maxblock))
// 	for addr, rate := range rates {
// 		moniker := MonikerMap[addr]
// 		if moniker == "" {
// 			moniker = "inconnu"
// 		}

// 		emoji := "🟢"
// 		if rate < 95.0 {
// 			emoji = "🔴"
// 		}

// 		buffer.WriteString(fmt.Sprintf("  %s Validator : %s addr: (%s) rate : %.2f%%\n", emoji, moniker, addr, rate))
// 	}

// 	msg := buffer.String()
// 	err := internal.SendAllValidatorAlerts(msg, "info", "", "", db)
// 	if err != nil {
// 		log.Printf("[SendDailyStats] Discord alert failed: %v", err)
// 	}
// 	// err = internal.SendSlackAlertValidator(user_id, msg, db)
// 	// if err != nil {
// 	// 	log.Printf("[SendDailyStats] Slack alert failed: %v", err)
// 	// }
// }

func CalculateRate(db *gorm.DB, date string) (map[string]float64, int64, int64) {
	rates := make(map[string]float64)

	// ✅ Étape 1 : récupérer les hauteurs min/max
	var minHeight, maxHeight int64
	err := db.Raw(`
		SELECT MIN(block_height), MAX(block_height)
		FROM daily_participation
		WHERE date = ?
	`, date).Row().Scan(&minHeight, &maxHeight)

	if err != nil {
		log.Printf("[CalculateRate] Error retrieving block range: %v", err)
		return rates, 0, 0
	}

	// ✅ Étape 2 : requête de participation
	rows, err := db.Raw(`
		SELECT 
			addr,
			moniker,
			COUNT(*) AS total_blocks,
			SUM(CASE WHEN participated THEN 1 ELSE 0 END) AS participated_blocks
		FROM daily_participation
		WHERE date = ?
		GROUP BY addr, moniker
	`, date).Rows()
	if err != nil {
		log.Printf("[CalculateRate] Error querying participation: %v", err)
		return rates, minHeight, maxHeight
	}
	defer rows.Close()

	// ✅ Étape 3 : traitement des résultats
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
