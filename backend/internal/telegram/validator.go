package telegram

import (
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

// BuildTelegramHandlers retourne la map de handlers
func BuildTelegramHandlers(token string, db *gorm.DB) map[string]func(int64, string) {
	period_default := "current_month"

	limit_default := int64(10)
	return map[string]func(int64, string){

		"/status": func(chatID int64, args string) {
			params := parseParams(args)
			period := params["period"]
			if period == "" {
				period = period_default
			}

			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit value : %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := formatParticipationRAte(db, period, limitint)
			if err != nil {
				log.Printf("error get particpate Rate%s", err)
			}

			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/status", err)
			}

		},
		"/uptime": func(chatID int64, args string) {
			params := parseParams(args)
			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit: %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := formatUptime(db, limitint)
			if err != nil {
				log.Printf("error get uptime metrics: %s", err)
			}
			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/uptime", err)
			}

		}, "/tx_contrib": func(chatID int64, args string) {
			params := parseParams(args)
			period := params["period"]
			if period == "" {
				period = period_default
			}
			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit: %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := FormatTxcontrib(db, period, limitint)
			if err != nil {
				log.Printf("error get tx_contribe%s", err)
			}

			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/tx_contrib", err)
			}

		},
		"/missing": func(chatID int64, args string) {
			params := parseParams(args)

			period := params["period"]
			if period == "" {
				period = period_default
			}
			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit: %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := formatMissing(db, period, limitint)
			if err != nil {
				log.Printf("error get missingg block%s", err)
			}

			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/missing", err)
			}

		},

		"/help": func(chatID int64, _ string) {

			msg := formatHelp()
			_ = SendMessageTelegram(token, chatID, msg)
		},

		"*": func(chatID int64, _ string) {
			if err := SendMessageTelegram(token, chatID,
				"Unknown command ❓ try /help"); err != nil {
				log.Printf("send %s failed: %v", "/status", err)
			}
		},
	}
}

func formatParticipationRAte(db *gorm.DB, period string, limit int) (msg string, err error) {
	rates, err := database.GetCurrentPeriodParticipationRate(db, period)
	if err != nil {
		return "", fmt.Errorf("failed to get participation rate: %v", err)

	}
	if len(rates) == 0 {
		return fmt.Sprintf("📊 <b>Participation rates — %s</b>\nNo data.", html.EscapeString(period)), nil
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("📊 <b>Participation rates for %s</b>\n\n", period))

	// limit
	if len(rates) < limit {
		limit = len(rates)
	}

	for i, r := range rates {

		if i >= limit {
			break
		}

		emoji := "🟢"
		if r.ParticipationRate < 95.0 {
			emoji = "🟡"
		}
		if r.ParticipationRate < 70.0 {
			emoji = "🟠"

		}
		if r.ParticipationRate < 50.0 {
			emoji = "🔴"

		}
		builder.WriteString(fmt.Sprintf(
			"%s  <b>%s </b> \n addr:  %s \n %.2f%%\n\n",
			emoji, html.EscapeString(r.Moniker), html.EscapeString(r.Addr), r.ParticipationRate,
		))

	}

	return builder.String(), nil
}
func formatUptime(db *gorm.DB, limit int) (msg string, err error) {

	results, err := database.UptimeMetricsaddr(db)
	if err != nil {
		return "", fmt.Errorf("failed to get uptime metrics: %v", err)

	}
	if len(results) == 0 {
		return "🕘 <b>Update Metrics</b>\nNo data.", nil
	}

	// Sort by  (desc)
	sort.Slice(results, func(i, j int) bool { return results[i].DaysDiff > results[j].DaysDiff })

	var builder strings.Builder
	builder.WriteString("🕘 <b>Uptime metrics </b>\n\n")

	// limit
	if len(results) < limit {
		limit = len(results)
	}

	for i, r := range results {
		if i >= limit {
			break
		}

		builder.WriteString(fmt.Sprintf(
			"<b> %s </b> \n addr: %s \n uptime : %.2f days \n\n",
			html.EscapeString(r.Moniker), html.EscapeString(r.Addr), r.DaysDiff,
		))

	}

	return builder.String(), err
}
func FormatTxcontrib(db *gorm.DB, period string, limit int) (msg string, err error) {
	txcontrib, err := database.TxContrib(db, period)
	if err != nil {
		return "", fmt.Errorf("failed to get tx_contrib: %v", err)

	}
	if len(txcontrib) == 0 {
		return fmt.Sprintf("⚙️ <b>Tx Contrib — %s</b>\nNo data.", html.EscapeString(period)), nil
	}

	// Sort by  (desc)
	sort.Slice(txcontrib, func(i, j int) bool { return txcontrib[i].TxContrib > txcontrib[j].TxContrib })

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("⚙️ <b>Tx Contrib metrics for %s</b>\n\n", period))

	// limit
	if len(txcontrib) < limit {
		limit = len(txcontrib)
	}

	for i, r := range txcontrib {
		if i >= limit {
			break
		}

		builder.WriteString(fmt.Sprintf(
			"<b> %s </b> \n addr %s  \n contrib : %.2f%%\n\n",
			html.EscapeString(r.Moniker), html.EscapeString(r.Addr), r.TxContrib,
		))

	}

	return builder.String(), nil
}

func formatMissing(db *gorm.DB, period string, limit int) (string, error) {
	rows, err := database.MissingBlock(db, period)
	if err != nil {
		return "", fmt.Errorf("failed to get missing block: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Sprintf("🕵️ <b>Missing blocks — %s</b>\nNo data.", html.EscapeString(period)), nil
	}

	// Sort by missed blocks (desc)
	sort.Slice(rows, func(i, j int) bool { return rows[i].MissingBlock > rows[j].MissingBlock })

	var b strings.Builder

	b.WriteString(fmt.Sprintf("🕵️ <b>Missing Blocks — %s</b>\n\n", html.EscapeString(period)))

	// limit
	if len(rows) < limit {
		limit = len(rows)
	}

	for i, r := range rows {
		if i >= limit {
			break
		}
		emoji := "🟢"
		if r.MissingBlock > 50 {
			emoji = "🔴"
		} else if r.MissingBlock > 20 {
			emoji = "🟠"
		} else if r.MissingBlock > 5 {
			emoji = "🟡"
		}

		b.WriteString(fmt.Sprintf(
			"%d. %s <b>%s</b>\n<code>%s</code>\nMissing: <b>%d blocks</b> \n\n",
			i+1, emoji, html.EscapeString(r.Moniker), r.Addr, r.MissingBlock,
		))
	}
	return b.String(), nil
}

func formatHelp() string {
	var b strings.Builder
	b.WriteString("🆘 <b>Help</b>\n\n")

	b.WriteString("⏱️ <b>Available periods</b>\n")
	b.WriteString("• <code>current_week</code>\n")
	b.WriteString("• <code>current_month</code>\n")
	b.WriteString("• <code>current_year</code>\n")
	b.WriteString("• <code>all_time</code>\n\n")

	b.WriteString("📡 <b>Commands</b>\n")

	b.WriteString("<code>/status [period=...] [limit=N]</code>\n")
	b.WriteString("Shows the participation rate of validators for a given period.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/status</code> (defaults: period=current_month, limit=10)\n")
	b.WriteString("• <code>/status period=current_month limit=5</code>\n\n")

	b.WriteString("<code>/uptime [limit=N]</code>\n")
	b.WriteString("Displays uptime statistics of validator.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/uptime</code> (default: limit=10)\n")
	b.WriteString("• <code>/uptime limit=3</code>\n\n")

	b.WriteString("<code>/tx_contrib [period=...] [limit=N]</code>\n")
	b.WriteString("Shows each validator’s contribution to transaction inclusion.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/tx_contrib</code> (defaults: period=current_month, limit=10)\n")
	b.WriteString("• <code>/tx_contrib period=current_year limit=20</code>\n\n")

	b.WriteString("<code>/missing [period=...] [limit=N]</code>\n")
	b.WriteString("Displays how many blocks each validator missed for a given period.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/missing</code> (defaults: period=current_month, limit=10)\n")
	b.WriteString("• <code>/missing period=all_time limit=50</code>\n\n")

	b.WriteString("ℹ️ Parameters must be written as <code>key=value</code> (e.g. <code>period=current_week</code>).\n")

	return b.String()
}
