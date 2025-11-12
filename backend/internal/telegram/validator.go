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
		"/subscribe": func(chatID int64, args string) {
			handleSubscribe(token, db, chatID, args)
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

		}, "/operation_time": func(chatID int64, args string) {
			params := parseParams(args)
			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit: %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := formatOperationTime(db, limitint)
			if err != nil {
				log.Printf("error get uptime metrics: %s", err)
			}
			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/uptime", err)
			}

		},
		"/tx_contrib": func(chatID int64, args string) {
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
		"/report": func(chatID int64, args string) {
			params := parseParams(args)

			activate := params["activate"]

			msg, err := reportActivate(db, chatID, activate)
			if err != nil {
				log.Printf("error report activate%s", err)
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
	sort.Slice(results, func(i, j int) bool { return results[i].Uptime > results[j].Uptime })

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

		emoji := "🟢"
		if r.Uptime < 95.0 {
			emoji = "🟡"
		}
		if r.Uptime < 70.0 {
			emoji = "🟠"

		}
		if r.Uptime < 50.0 {
			emoji = "🔴"

		}

		builder.WriteString(fmt.Sprintf(
			"%s <b> %s </b> \n addr: %s \n uptime : %.2f%%\n\n",
			emoji, html.EscapeString(r.Moniker), html.EscapeString(r.Addr), r.Uptime,
		))

	}

	return builder.String(), err
}
func formatOperationTime(db *gorm.DB, limit int) (msg string, err error) {

	results, err := database.OperationTimeMetricsaddr(db)
	if err != nil {
		return "", fmt.Errorf("failed to get operation time  metrics: %v", err)

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
			" <b> %s </b> \n addr: %s \n Operation Time : %.2f days\n\n",
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
func reportActivate(db *gorm.DB, chatID int64, isActivate string) (string, error) {
	if isActivate == "" {
		status, err := database.GetTelegramReportStatus(db, chatID)
		if err != nil {
			return "", fmt.Errorf("failed to get status report: %w", err)
		}

		if status {
			return "📝 The daily report is activated ✅", nil
		}
		return "📝 The daily report is disabled ❌", nil
	}

	switch strings.ToLower(isActivate) {
	case "true", "on", "enable", "activate":
		if err := database.ActivateTelegramReport(db, true, chatID); err != nil {
			return "", fmt.Errorf("failed to activate report: %w", err)
		}
		return "✅ The daily report has been activated.", nil

	case "false", "off", "disable", "deactivate":
		if err := database.ActivateTelegramReport(db, false, chatID); err != nil {
			return "", fmt.Errorf("failed to deactivate report: %w", err)
		}
		return "🚫 The daily report has been disabled.", nil

	default:
		return "⚠️ Invalid argument. Use `/report activate=true` or `/report activate=false`.", nil
	}
}
func handleSubscribe(token string, db *gorm.DB, chatID int64, args string) {
	fields := strings.Fields(args)
	if len(fields) == 0 || fields[0] == "help" {
		_ = SendMessageTelegram(token, chatID, subscribeUsage())
		return
	}

	cmd := strings.ToLower(fields[0])
	rest := fields[1:]

	switch cmd {
	case "list":
		subs, err := database.GetValidatorStatusList(db, chatID)
		if err != nil {
			log.Printf("subscribe:list fail: %v", err)
			_ = SendMessageTelegram(token, chatID, "⚠️ Unable to fetch list of validators.")
			return
		}

		var b strings.Builder
		b.WriteString("🧾 <b>Your subscriptions</b>\n")
		for _, s := range subs {

			b.WriteString(fmt.Sprintf("• %s (%s): <b>%s</b>\n", s.Moniker, s.Addr, s.Status))
		}
		_ = SendMessageTelegram(token, chatID, b.String())
		return

	case "on":
		if len(rest) == 0 {
			_ = SendMessageTelegram(token, chatID, "Usage: /subscribe on <addr> [more...]")
			return
		}
		if strings.ToLower(rest[0]) == "all" {
			vals, err := database.GetAllValidators(db)
			if err != nil {
				_ = SendMessageTelegram(token, chatID, "⚠️ Unable to fetch validator list.")
				return
			}
			changed := 0
			for _, v := range vals {
				if err := database.UpdateTelegramValidatorSubStatus(db, chatID, v.Addr, v.Moniker, "subscribe"); err == nil {
					changed++
				}
			}
			_ = SendMessageTelegram(token, chatID, fmt.Sprintf("✅ Enabled alerts for <b>%d</b> validators.", changed))
			return
		}

		moniker, err := database.ResolveAddrs(db, rest)
		if err != nil {
			_ = SendMessageTelegram(token, chatID, "⚠️ Some validators could not be resolved.")
		}
		if len(moniker) == 0 {
			_ = SendMessageTelegram(token, chatID, "No valid validators found.")
			return
		}
		var ok, fail int
		for _, m := range moniker {
			if err := database.UpdateTelegramValidatorSubStatus(db, chatID, m.Addr, m.Moniker, "subscribe"); err != nil {
				fail++
			} else {
				ok++
			}
		}
		_ = SendMessageTelegram(token, chatID, fmt.Sprintf("✅ Subscribed: %d | ❌ Failed: %d", ok, fail))

		return

	case "off":
		if len(rest) == 0 {
			_ = SendMessageTelegram(token, chatID, "Usage: /subscribe off <addr|moniker>|all")
			return
		}
		if strings.ToLower(rest[0]) == "all" {
			subs, err := database.GetTelegramValidatorSub(db, chatID, true)
			if err != nil {
				_ = SendMessageTelegram(token, chatID, "⚠️ Unable to fetch your active subscriptions.")
				return
			}
			var ok int
			for _, s := range subs {
				if err := database.UpdateTelegramValidatorSubStatus(db, chatID, s.Addr, s.Moniker, "unsubscribe"); err == nil {
					ok++
				}
			}
			_ = SendMessageTelegram(token, chatID, fmt.Sprintf("🛑 Disabled alerts for <b>%d</b> validators.", ok))
			return
		}
		moniker, err := database.ResolveAddrs(db, rest)
		if err != nil {
			_ = SendMessageTelegram(token, chatID, "⚠️ Some validators could not be resolved.")
		}
		if len(moniker) == 0 {
			_ = SendMessageTelegram(token, chatID, "No valid validators found.")
			return
		}
		var ok, fail int
		for _, m := range moniker {
			if err := database.UpdateTelegramValidatorSubStatus(db, chatID, m.Addr, m.Moniker, "unsubscribe"); err != nil {
				fail++
			} else {
				ok++
			}
		}
		_ = SendMessageTelegram(token, chatID, fmt.Sprintf("✅ Subscribed: %d | ❌ Failed: %d", ok, fail))

	default:
		_ = SendMessageTelegram(token, chatID, subscribeUsage())
		return
	}
}

func subscribeUsage() string {
	return `📬 <b>Subscribe command</b>
/subscribe list  — show your subscriptions
/subscribe on <addr> [more...] — enable alerts
/subscribe off <addr> [more...] — disable alerts
/subscribe on all — enable all
/subscribe off all — disable all`
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

	b.WriteString("<code>🚦 /status [period=...] [limit=N]</code>\n")
	b.WriteString("Shows the participation rate of validators for a given period.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/status</code> (defaults: period=current_month, limit=10)\n")
	b.WriteString("• <code>/status period=current_month limit=5</code>\n\n")

	b.WriteString("<code>🕒 /uptime [limit=N]</code>\n")
	b.WriteString("Displays uptime statistics of validator.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/uptime</code> (default: limit=10)\n")
	b.WriteString("• <code>/uptime limit=3</code>\n\n")

	b.WriteString("<code>💪 /tx_contrib [period=...] [limit=N]</code>\n")
	b.WriteString("Shows each validator’s contribution to transaction inclusion.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/tx_contrib</code> (defaults: period=current_month, limit=10)\n")
	b.WriteString("• <code>/tx_contrib period=current_year limit=20</code>\n\n")

	b.WriteString("<code>🚧 /missing [period=...] [limit=N]</code>\n")
	b.WriteString("Displays how many blocks each validator missed for a given period.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/missing</code> (defaults: period=current_month, limit=10)\n")
	b.WriteString("• <code>/missing period=all_time limit=50</code>\n\n")

	b.WriteString("📬 <b>Subscribe command</b> \n")

	b.WriteString("Show your active subscriptions and available validators\n")
	b.WriteString("• <code>/subscribe list </code>\n")

	b.WriteString("Enable alerts for one or more validators\n")
	b.WriteString("• <code>/subscribe on <addr> [more...]</code>\n")

	b.WriteString("Disable alerts for one or more validators\n")
	b.WriteString("• <code>/subscribe off <addr> [more...]</code>\n ")

	b.WriteString("Enable alerts for all validators\n")
	b.WriteString("• <code>/subscribe on all </code>\n")

	b.WriteString("Disable alerts for all validators\n")
	b.WriteString("• <code>/subscribe off all </code>\n")

	b.WriteString("ℹ️ Parameters must be written as <code>key=value</code> (e.g. <code>period=current_week</code>).\n")

	return b.String()
}
