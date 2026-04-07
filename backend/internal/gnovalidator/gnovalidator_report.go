package gnovalidator

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"gorm.io/gorm"
)

// SendTelegramMessage is a function variable set by the telegram package at startup
// to break the import cycle between gnovalidator and telegram.
var SendTelegramMessage func(token string, chatID int64, text string) error

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

		log.Printf("[report] next for user %s at %s (in %s)", userID, next.Format(time.RFC1123), wait)

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			log.Printf("[report] sending for user %s", userID)
			SendDailyStatsForUser(db, internal.Config.DefaultChain, &userID, nil, loc)
		case <-reload:
			timer.Stop()
			log.Printf("[report] reloading schedule for user %s", userID)
			return
		}
	}
}
func SheduleTelegramReport(chatID int64, chainID string, hour, minute int, timezone string, db *gorm.DB, reload <-chan struct{}) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Printf("⚠️ Invalid timezone for chat %d chain %s: %s, defaulting to UTC", chatID, chainID, timezone)
		loc = time.UTC
	}

	for {
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if next.Before(now) {
			next = next.Add(24 * time.Hour)
		}
		wait := time.Until(next)

		log.Printf("[report][%s] next for chat %d at %s (in %s)", chainID, chatID, next.Format(time.RFC1123), wait)

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			log.Printf("[report][%s] sending for chat %d", chainID, chatID)
			SendDailyStatsForUser(db, chainID, nil, &chatID, loc)
		case <-reload:
			timer.Stop()
			log.Printf("[report][%s] reloading schedule for chat %d", chainID, chatID)
			return
		}
	}
}

func SendDailyStatsForUser(db *gorm.DB, chainID string, userID *string, chatID *int64, loc *time.Location) {
	snap := FetchChainHealthSnapshot(db, chainID)

	var msg string
	switch {
	case snap.IsDisabled:
		msg = FormatDisabledReport(chainID, snap)
	case snap.IsStuck:
		msg = FormatStuckReport(chainID, snap)
	default:
		yesterday := time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")
		rates, minBlock, maxBlock := CalculateRate(db, chainID, yesterday)
		if len(rates) == 0 {
			log.Printf("[report][%s] no participation data for %s, skipping", chainID, yesterday)
			return
		}
		msg = FormatHealthyReport(chainID, yesterday, rates, minBlock, maxBlock, snap.AlertsLast24h)
	}

	switch {
	case userID != nil:
		SendUserReportInChunks(*userID, chainID, msg, db, 1500)
	case chatID != nil:
		if SendTelegramMessage != nil {
			if err := SendTelegramMessage(internal.Config.TokenTelegramValidator, *chatID, msg); err != nil {
				log.Printf("❌ Telegram send failed (chat %d): %v", *chatID, err)
			}
		} else {
			log.Printf("❌ Telegram send skipped (chat %d): SendTelegramMessage not initialized", *chatID)
		}
	default:
		log.Println("⚠️ Neither userID nor chatID provided — no target to send report.")
	}
}

func CalculateRate(db *gorm.DB, chainID, date string) (map[string]ValidatorRate, int64, int64) {
	rates := make(map[string]ValidatorRate)

	// Single query combining agrega (fast path) with raw fallback for days not yet aggregated.
	// Returns one row per validator with participation totals and block height range.
	rows, err := db.Raw(`
		SELECT addr, MAX(moniker) AS moniker,
			SUM(total_blocks) AS total_blocks,
			SUM(participated_count) AS participated_count,
			MIN(first_block_height) AS first_block,
			MAX(last_block_height)  AS last_block
		FROM (
			SELECT chain_id, addr, moniker, total_blocks, participated_count,
				first_block_height, last_block_height
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date = ?
			UNION ALL
			SELECT dp.chain_id, dp.addr, MAX(dp.moniker),
				COUNT(*),
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				MIN(dp.block_height), MAX(dp.block_height)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date = DATE(dp.date)
			WHERE dp.chain_id = ? AND DATE(dp.date) = ? AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
		) combined
		GROUP BY addr
	`, chainID, date, chainID, date).Rows()
	if err != nil {
		log.Printf("[report][%s] error querying participation for %s: %v", chainID, date, err)
		return rates, 0, 0
	}
	defer rows.Close()

	var minHeight, maxHeight int64
	first := true
	for rows.Next() {
		var addr, moniker string
		var total, participated int
		var firstBlock, lastBlock int64

		if err := rows.Scan(&addr, &moniker, &total, &participated, &firstBlock, &lastBlock); err != nil {
			log.Printf("[report][%s] scan error for %s: %v", chainID, date, err)
			continue
		}

		if total > 0 {
			rate := float64(participated) / float64(total) * 100
			rates[addr] = ValidatorRate{Rate: rate, Moniker: moniker}
		}

		if first || firstBlock < minHeight {
			minHeight = firstBlock
		}
		if first || lastBlock > maxHeight {
			maxHeight = lastBlock
		}
		first = false
	}

	return rates, minHeight, maxHeight
}

func GetLastStoredHeight(db *gorm.DB, chainID string) (int64, error) {
	var result struct {
		MaxHeight int64
	}

	err := db.Raw(`SELECT MAX(block_height) AS max_height FROM daily_participations WHERE chain_id = ?`, chainID).Scan(&result).Error
	if err != nil {
		return 0, fmt.Errorf("error reading last stored block: %w", err)
	}

	var height int64
	if result.MaxHeight > 0 {
		height = result.MaxHeight - 1
	} else {
		height = 0
	}

	return height, nil
}
func SendUserReportInChunks(userID, chainID string, fullMsg string, db *gorm.DB, maxLen int) {
	lines := strings.Split(fullMsg, "\n")

	var buffer strings.Builder
	for _, line := range lines {
		// +1
		if buffer.Len()+len(line)+1 > maxLen {
			// send chunk
			err := internal.SendUserReportAlert(userID, chainID, buffer.String(), db)
			if err != nil {
				log.Printf("[SendUserReportInChunks] Send error for %s: %v", userID, err)
			}
			buffer.Reset()
		}

		// add line to buffer
		buffer.WriteString(line)
		buffer.WriteString("\n")
	}

	// send ultimate part
	if buffer.Len() > 0 {
		err := internal.SendUserReportAlert(userID, chainID, buffer.String(), db)
		if err != nil {
			log.Printf("[SendUserReportInChunks] Send error for %s: %v", userID, err)
		}
	}
}
