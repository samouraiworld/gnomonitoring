package gnovalidator

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
)

func StartDailyReport(db *sql.DB) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("⚠️ Panic in daily stats goroutine: %v", r)
			}
		}()

		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), internal.Config.DailyReportHour, internal.Config.DailyReportMinute, 0, 0, now.Location())
			if next.Before(now) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(next.Sub(now))

			log.Println("⏰ Time reached. Sending daily stats...")
			SendDailyStats(db)
			err := internal.PruneOldParticipationData(db, 30)
			if err != nil {
				log.Printf("Prune error: %v", err)
			}
		}
	}()
}
func SendDailyStats(db *sql.DB) {
	MonikerMutex.RLock()
	defer MonikerMutex.RUnlock()

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	rates, minblock, maxblock := CalculateRate(db, yesterday)
	// rates, minblock, maxblock := CalculateRate(db, "2025-07-14") // for test
	var buffer bytes.Buffer
	buffer.WriteString(fmt.Sprintf("📊 *Daily Participation Summary* for %s (Blocks %d → %d):\n\n", yesterday, minblock, maxblock))
	for addr, rate := range rates {
		moniker := MonikerMap[addr]
		if moniker == "" {
			moniker = "inconnu"
		}

		emoji := "🟢"
		if rate < 95.0 {
			emoji = "🔴"
		}

		buffer.WriteString(fmt.Sprintf("  %s Validator : %s addr: (%s) rate : %.2f%%\n", emoji, moniker, addr, rate))
	}

	msg := buffer.String()
	err := internal.SendAllValidatorAlerts(msg, db)
	if err != nil {
		log.Printf("[SendDailyStats] Discord alert failed: %v", err)
	}
	// err = internal.SendSlackAlertValidator(user_id, msg, db)
	// if err != nil {
	// 	log.Printf("[SendDailyStats] Slack alert failed: %v", err)
	// }
}

func CalculateRate(db *sql.DB, date string) (map[string]float64, int64, int64) {
	rates := make(map[string]float64)
	// Get Min and max block use for calculate rate

	var minHeight, maxHeight int64
	err := db.QueryRow(`
		SELECT MIN(block_height), MAX(block_height)
		FROM daily_participation
		WHERE date = ?
	`, date).Scan(&minHeight, &maxHeight)
	if err != nil {
		log.Printf("[CalculateRate] Error retrieving block range: %v", err)
		return rates, 0, 0
	}

	// get participated block of one days
	rows, err := db.Query(`
		SELECT 
			addr,
			moniker,
			COUNT(*) AS total_blocks,
			SUM(CASE WHEN participated THEN 1 ELSE 0 END) AS participated_blocks
		FROM daily_participation
		WHERE date = ?
		GROUP BY addr, moniker
	`, date)

	if err != nil {
		log.Printf("[CalculateRate] Error querying participation: %v", err)
		return rates, minHeight, maxHeight
	}
	defer rows.Close()

	for rows.Next() {
		var addr, moniker string
		var total, participated int

		if err := rows.Scan(&addr, &moniker, &total, &participated); err != nil {
			log.Printf("[CalculateRate] Scan error: %v", err)
			continue
		}

		rate := float64(participated) / float64(total) * 100
		rates[addr] = rate

	}

	return rates, minHeight, maxHeight
}

func GetLastStoredHeight(db *sql.DB) (int64, error) {
	var height int64
	err := db.QueryRow(`SELECT MAX(block_height) FROM daily_participation`).Scan(&height)
	if err != nil {
		return 0, fmt.Errorf("error reading last stored block: %w", err)
	}
	return height, nil
}
