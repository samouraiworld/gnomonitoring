package gnovalidator

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/telegram"
	"gorm.io/gorm"
)

type ValidatorRate struct {
	Rate    float64
	Moniker string
}

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
			SendDailyStatsForUser(db, &userID, nil, loc)
		case <-reload:
			log.Printf("♻️ Reloading schedule for user %s", userID)
			return
		}
	}
}
func SheduleTelegramReport(chat_id int64, hour, minute int, timezone string, db *gorm.DB, reload <-chan struct{}) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Printf("⚠️ Invalid timezone for user %d: %s, defaulting to UTC", chat_id, timezone)
		loc = time.UTC
	}

	for {
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if next.Before(now) {
			next = next.Add(24 * time.Hour)
		}
		wait := time.Until(next)

		log.Printf("🕓 Scheduled next report for %d at %s (%s)", chat_id, next.Format(time.RFC1123), wait)

		select {
		case <-time.After(wait):
			log.Printf("⏰ Sending report for user %d", chat_id)
			SendDailyStatsForUser(db, nil, &chat_id, loc)
		case <-reload:
			log.Printf("♻️ Reloading schedule for user %d", chat_id)
			return
		}
	}
}

func SendDailyStatsForUser(db *gorm.DB, userID *string, chatID *int64, loc *time.Location) {
	yesterday := time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")

	rates, minBlock, maxBlock := CalculateRate(db, yesterday)
	//rates, minBlock, maxBlock := CalculateRate(db, "2025-10-18")
	if len(rates) == 0 {
		log.Printf("⚠️ No participation data found on date %s", yesterday)
		return
	}

	var buffer bytes.Buffer
	buffer.WriteString(fmt.Sprintf("📊 *Daily Summary* for %s (Blocks %d → %d):\n\n", yesterday, minBlock, maxBlock))

	for addr, data := range rates {
		moniker := data.Moniker
		if moniker == "" {
			moniker = "unknown"
		}
		emoji := "🟢"
		if data.Rate < 95.0 {
			emoji = "🟡"
		}
		if data.Rate < 70.0 {
			emoji = "🟠"

		}
		if data.Rate < 50.0 {
			emoji = "🔴"

		}
		buffer.WriteString(fmt.Sprintf("  %s Validator: %s addr: (%s) rate: %.2f%%\n", emoji, moniker, addr, data.Rate))
	}
	msg := buffer.String()
	log.Println(msg)
	switch {
	case userID != nil:
		// 🔹 Rapport utilisateur interne
		SendUserReportInChunks(*userID, msg, db, 1500)

	case chatID != nil:
		// 🔹 Rapport Telegram
		if err := telegram.SendMessageTelegram(internal.Config.TokenTelegramValidator, *chatID, msg); err != nil {
			log.Printf("❌ Telegram send failed (chat %d): %v", *chatID, err)
		}

	default:
		log.Println("⚠️ Neither userID nor chatID provided — no target to send report.")
	}

}

func CalculateRate(db *gorm.DB, date string) (map[string]ValidatorRate, int64, int64) {
	rates := make(map[string]ValidatorRate)
	log.Println(date)
	// Step 1: Retrieve the min/max heights
	var minHeight, maxHeight int64
	err := db.Raw(`
		SELECT MIN(block_height), MAX(block_height)
		FROM daily_participations
		WHERE date(date) = ?
		ORDER by ASC
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
		WHERE date(date) = ?
		GROUP BY addr, moniker
	`, date).Rows()
	if err != nil {
		log.Printf("[CalculateRate] Error querying participation: %v", err)
		return rates, minHeight, maxHeight
	}
	defer rows.Close()

	// Step 3: Process the results
	for rows.Next() {
		var addr, moniker string
		var total, participated int

		if err := rows.Scan(&addr, &moniker, &total, &participated); err != nil {
			log.Printf("[CalculateRate] Scan error: %v", err)
			continue
		}

		if total > 0 {
			rate := float64(participated) / float64(total) * 100
			rates[addr] = ValidatorRate{
				Rate:    rate,
				Moniker: moniker,
			}
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

	var height int64
	if result.MaxHeight > 0 {
		height = result.MaxHeight - 1
	} else {
		height = 0
	}

	fmt.Printf("last block: %d\n", height)
	return height, nil
}
func SendUserReportInChunks(userID string, fullMsg string, db *gorm.DB, maxLen int) {
	lines := strings.Split(fullMsg, "\n")

	var buffer strings.Builder
	for _, line := range lines {
		// +1
		if buffer.Len()+len(line)+1 > maxLen {
			// send chunk
			err := internal.SendUserReportAlert(userID, buffer.String(), db)
			if err != nil {
				log.Printf("[SendUserReportInChunks] Send error for %s: %v", userID, err)
			}
			buffer.Reset()
		}

		// add line to buffer
		buffer.WriteString(line)
		buffer.WriteString("\n")
	}

	//send ultimate part
	if buffer.Len() > 0 {
		err := internal.SendUserReportAlert(userID, buffer.String(), db)
		if err != nil {
			log.Printf("[SendUserReportInChunks] Send error for %s: %v", userID, err)
		}
	}
}
