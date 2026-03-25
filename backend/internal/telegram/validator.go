package telegram

import (
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

const (
	periodDefault = "current_month"
	limitDefault  = 10
	limitMax      = 50
	sortDefault   = "desc"
	searchTTL     = 2 * time.Minute
)

const cacheTTL = 45 * time.Second

type cacheEntry struct {
	data      any
	expiresAt time.Time
}

var (
	metricsCache   = map[string]cacheEntry{}
	metricsCacheMu sync.Mutex
)

// chatChainState stores the per-chat active chain override (validator bot).
// Protected by chatChainMu.
var chatChainState = map[int64]string{}
var chatChainMu sync.RWMutex

// govdaoChatChainState stores the per-chat active chain override (govdao bot).
// Separate from validator bot to prevent cross-bot conflicts.
// Protected by govdaoChatChainMu.
var govdaoChatChainState = map[int64]string{}
var govdaoChatChainMu sync.RWMutex

// getActiveChain returns the chain ID for the given chat. If no per-chat
// override has been set it falls back to defaultChainID.
func getActiveChain(chatID int64, defaultChainID string) string {
	chatChainMu.RLock()
	id, ok := chatChainState[chatID]
	chatChainMu.RUnlock()
	if ok && id != "" {
		return id
	}
	return defaultChainID
}

// setActiveChain stores a per-chat chain override. Passing an empty string
// clears the override so subsequent calls to getActiveChain fall back to the
// default.
func setActiveChain(chatID int64, chainID string) {
	chatChainMu.Lock()
	defer chatChainMu.Unlock()
	if chainID == "" {
		delete(chatChainState, chatID)
	} else {
		chatChainState[chatID] = chainID
	}
}

// getGovdaoActiveChain returns the chain ID for the given govdao chat. If no per-chat
// override has been set it falls back to defaultChainID.
func getGovdaoActiveChain(chatID int64, defaultChainID string) string {
	govdaoChatChainMu.RLock()
	id, ok := govdaoChatChainState[chatID]
	govdaoChatChainMu.RUnlock()
	if ok && id != "" {
		return id
	}
	return defaultChainID
}

// setGovdaoActiveChain stores a per-chat chain override for govdao. Passing an empty string
// clears the override so subsequent calls to getGovdaoActiveChain fall back to the default.
func setGovdaoActiveChain(chatID int64, chainID string) {
	govdaoChatChainMu.Lock()
	defer govdaoChatChainMu.Unlock()
	if chainID == "" {
		delete(govdaoChatChainState, chatID)
	} else {
		govdaoChatChainState[chatID] = chainID
	}
}

// validateChainID returns true if chainID is in the enabledChains slice.
func validateChainID(chainID string, enabledChains []string) bool {
	for _, id := range enabledChains {
		if id == chainID {
			return true
		}
	}
	return false
}

func getCached(key string) (any, bool) {
	metricsCacheMu.Lock()
	defer metricsCacheMu.Unlock()
	e, ok := metricsCache[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.data, true
}

func setCached(key string, data any, ttl time.Duration) {
	metricsCacheMu.Lock()
	defer metricsCacheMu.Unlock()
	metricsCache[key] = cacheEntry{data: data, expiresAt: time.Now().Add(ttl)}
}

// BuildTelegramHandlers retourne la map de handlers.
// defaultChainID is used as fallback when a chat has no per-chat chain
// override. enabledChains is the list of valid chain IDs (used by /chain and
// /setchain).
func BuildTelegramHandlers(token string, db *gorm.DB, defaultChainID string, enabledChains []string) map[string]func(int64, string) {
	startSearchStateCleanup()
	return map[string]func(int64, string){

		"/status": func(chatID int64, args string) {
			chainID := getActiveChain(chatID, defaultChainID)
			params := parseParams(args)
			period := params["period"]
			if period == "" {
				period = periodDefault
			}

			limit := parseIntWithDefault(params["limit"], limitDefault, "limit")
			page := parseIntWithDefault(params["page"], 1, "page")
			filter := params["filter"]
			sortOrder := normalizeSort(params["sort"])

			sendPaginatedMessage(token, chatID, db, chainID, "status", period, filter, page, limit, sortOrder)

		},
		"/subscribe": func(chatID int64, args string) {
			chainID := getActiveChain(chatID, defaultChainID)
			handleSubscribe(token, db, chatID, chainID, args)
		},
		"/uptime": func(chatID int64, args string) {
			chainID := getActiveChain(chatID, defaultChainID)
			params := parseParams(args)
			limit := parseIntWithDefault(params["limit"], limitDefault, "limit")
			page := parseIntWithDefault(params["page"], 1, "page")
			filter := params["filter"]
			sortOrder := normalizeSort(params["sort"])

			sendPaginatedMessage(token, chatID, db, chainID, "uptime", "", filter, page, limit, sortOrder)

		}, "/operation_time": func(chatID int64, args string) {
			chainID := getActiveChain(chatID, defaultChainID)
			params := parseParams(args)
			limit := parseIntWithDefault(params["limit"], limitDefault, "limit")
			page := parseIntWithDefault(params["page"], 1, "page")
			filter := params["filter"]
			sortOrder := normalizeSort(params["sort"])

			sendPaginatedMessage(token, chatID, db, chainID, "operation_time", "", filter, page, limit, sortOrder)

		},
		"/tx_contrib": func(chatID int64, args string) {
			chainID := getActiveChain(chatID, defaultChainID)
			params := parseParams(args)
			period := params["period"]
			if period == "" {
				period = periodDefault
			}
			limit := parseIntWithDefault(params["limit"], limitDefault, "limit")
			page := parseIntWithDefault(params["page"], 1, "page")
			filter := params["filter"]
			sortOrder := normalizeSort(params["sort"])

			sendPaginatedMessage(token, chatID, db, chainID, "tx_contrib", period, filter, page, limit, sortOrder)

		},
		"/missing": func(chatID int64, args string) {
			chainID := getActiveChain(chatID, defaultChainID)
			params := parseParams(args)

			period := params["period"]
			if period == "" {
				period = periodDefault
			}
			limit := parseIntWithDefault(params["limit"], limitDefault, "limit")
			page := parseIntWithDefault(params["page"], 1, "page")
			filter := params["filter"]
			sortOrder := normalizeSort(params["sort"])

			sendPaginatedMessage(token, chatID, db, chainID, "missing", period, filter, page, limit, sortOrder)

		},
		"/report": func(chatID int64, args string) {
			chainID := getActiveChain(chatID, defaultChainID)
			params := parseParams(args)

			activate := params["activate"]

			msg, err := reportActivate(db, chatID, chainID, activate)
			if err != nil {
				log.Printf("[telegram] report activate error: %v", err)
			}

			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("[telegram] send /report failed: %v", err)
			}

		},

		"/chain": func(chatID int64, _ string) {
			current := getActiveChain(chatID, defaultChainID)
			var b strings.Builder
			b.WriteString(fmt.Sprintf("Current chain: <code>%s</code>\n\nAvailable chains:\n", html.EscapeString(current)))
			for _, id := range enabledChains {
				b.WriteString(fmt.Sprintf("• <code>%s</code>\n", html.EscapeString(id)))
			}
			b.WriteString("\nUse <code>/setchain chain=&lt;id&gt;</code> to switch.")
			_ = SendMessageTelegram(token, chatID, b.String())
		},

		"/setchain": func(chatID int64, args string) {
			params := parseParams(args)
			requested := strings.TrimSpace(params["chain"])
			if requested == "" {
				_ = SendMessageTelegram(token, chatID, "Usage: <code>/setchain chain=&lt;chain_id&gt;</code>")
				return
			}
			if !validateChainID(requested, enabledChains) {
				var b strings.Builder
				b.WriteString(fmt.Sprintf("Unknown chain <code>%s</code>. Available:\n", html.EscapeString(requested)))
				for _, id := range enabledChains {
					b.WriteString(fmt.Sprintf("• <code>%s</code>\n", html.EscapeString(id)))
				}
				_ = SendMessageTelegram(token, chatID, b.String())
				return
			}
			setActiveChain(chatID, requested)
			if err := database.UpdateChatChain(db, chatID, requested); err != nil {
				log.Printf("[telegram] UpdateChatChain chat=%d: %v", chatID, err)
			}
			_ = SendMessageTelegram(token, chatID, fmt.Sprintf("Chain set to <code>%s</code>.", html.EscapeString(requested)))
		},

		"/help": func(chatID int64, _ string) {

			msg := formatHelp()
			_ = SendMessageTelegram(token, chatID, msg)
		},

		"*": func(chatID int64, _ string) {
			if err := SendMessageTelegram(token, chatID,
				"Unknown command ❓ try /help"); err != nil {
				log.Printf("[telegram] send failed: %v", err)
			}
		},
	}
}

func formatParticipationRAte(db *gorm.DB, chainID, period string, page, limit int, filter, sortOrder string) (msg string, pageOut, totalPages int, err error) {
	var rates []database.ParticipationRate
	cacheKey := chainID + ":status:" + period
	if cached, ok := getCached(cacheKey); ok {
		if v, ok := cached.([]database.ParticipationRate); ok {
			rates = v
		}
	} else {
		var fetchErr error
		rates, fetchErr = database.GetCurrentPeriodParticipationRate(db, chainID, period)
		if fetchErr != nil {
			return "", 1, 1, fmt.Errorf("failed to get participation rate: %v", fetchErr)
		}
		setCached(cacheKey, rates, cacheTTL)
	}
	rates = filterParticipationRates(rates, filter)
	if len(rates) == 0 {
		return fmt.Sprintf("📊 <b>Participation rates — %s</b>\nNo data.", html.EscapeString(period)), 1, 1, nil
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("📊 <b>Participation rates for %s</b>\n\n", period))

	// Sort by participation rate
	sort.Slice(rates, func(i, j int) bool {
		return compareFloat(rates[i].ParticipationRate, rates[j].ParticipationRate, sortOrder)
	})

	pageOut, start, end, totalPages := paginate(len(rates), page, limit)
	builder.WriteString(pageInfoLine(pageOut, totalPages))
	builder.WriteString(filterInfoLine(filter))

	for _, r := range rates[start:end] {

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

	return builder.String(), pageOut, totalPages, nil
}
func formatUptime(db *gorm.DB, chainID string, page, limit int, filter, sortOrder string) (msg string, pageOut, totalPages int, err error) {

	var results []database.UptimeMetrics
	cacheKey := chainID + ":uptime"
	if cached, ok := getCached(cacheKey); ok {
		if v, ok := cached.([]database.UptimeMetrics); ok {
			results = v
		}
	} else {
		var fetchErr error
		results, fetchErr = database.UptimeMetricsaddr(db, chainID)
		if fetchErr != nil {
			return "", 1, 1, fmt.Errorf("failed to get uptime metrics: %v", fetchErr)
		}
		setCached(cacheKey, results, cacheTTL)
	}
	results = filterUptimeMetrics(results, filter)
	if len(results) == 0 {
		return "🕘 <b>Uptime metrics</b>\nNo data.", 1, 1, nil
	}

	// Sort by uptime
	sort.Slice(results, func(i, j int) bool { return compareFloat(results[i].Uptime, results[j].Uptime, sortOrder) })

	var builder strings.Builder
	builder.WriteString("🕘 <b>Uptime metrics </b>\n\n")

	pageOut, start, end, totalPages := paginate(len(results), page, limit)
	builder.WriteString(pageInfoLine(pageOut, totalPages))
	builder.WriteString(filterInfoLine(filter))

	for _, r := range results[start:end] {

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

	return builder.String(), pageOut, totalPages, err
}
func formatOperationTime(db *gorm.DB, chainID string, page, limit int, filter string) (msg string, pageOut, totalPages int, err error) {

	var results []database.OperationTimeMetrics
	cacheKey := chainID + ":operation_time"
	if cached, ok := getCached(cacheKey); ok {
		if v, ok := cached.([]database.OperationTimeMetrics); ok {
			results = v
		}
	} else {
		var fetchErr error
		results, fetchErr = database.OperationTimeMetricsaddr(db, chainID)
		if fetchErr != nil {
			return "", 1, 1, fmt.Errorf("failed to get operation time metrics: %v", fetchErr)
		}
		setCached(cacheKey, results, cacheTTL)
	}
	results = filterOperationTimeMetrics(results, filter)
	if len(results) == 0 {
		return "🕘 <b>Operation time metrics</b>\nNo data.", 1, 1, nil
	}

	// Sort by  (desc)
	sort.Slice(results, func(i, j int) bool { return results[i].DaysDiff > results[j].DaysDiff })

	var builder strings.Builder
	builder.WriteString("🕘 <b>Operation time metrics </b>\n\n")

	pageOut, start, end, totalPages := paginate(len(results), page, limit)
	builder.WriteString(pageInfoLine(pageOut, totalPages))
	builder.WriteString(filterInfoLine(filter))

	for _, r := range results[start:end] {

		builder.WriteString(fmt.Sprintf(
			" <b> %s </b> \n addr: %s \n Operation Time : %.2f days\n\n",
			html.EscapeString(r.Moniker), html.EscapeString(r.Addr), r.DaysDiff,
		))

	}

	return builder.String(), pageOut, totalPages, err
}
func FormatTxcontrib(db *gorm.DB, chainID, period string, page, limit int, filter, sortOrder string) (msg string, pageOut, totalPages int, err error) {
	var txcontrib []database.TxContribMetrics
	cacheKey := chainID + ":tx_contrib:" + period
	if cached, ok := getCached(cacheKey); ok {
		if v, ok := cached.([]database.TxContribMetrics); ok {
			txcontrib = v
		}
	} else {
		var fetchErr error
		txcontrib, fetchErr = database.TxContrib(db, chainID, period)
		if fetchErr != nil {
			return "", 1, 1, fmt.Errorf("failed to get tx_contrib: %v", fetchErr)
		}
		setCached(cacheKey, txcontrib, cacheTTL)
	}
	txcontrib = filterTxContrib(txcontrib, filter)
	if len(txcontrib) == 0 {
		return fmt.Sprintf("⚙️ <b>Tx Contrib — %s</b>\nNo data.", html.EscapeString(period)), 1, 1, nil
	}

	// Sort by contribution
	sort.Slice(txcontrib, func(i, j int) bool { return compareFloat(txcontrib[i].TxContrib, txcontrib[j].TxContrib, sortOrder) })

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("⚙️ <b>Tx Contrib metrics for %s</b>\n\n", period))

	pageOut, start, end, totalPages := paginate(len(txcontrib), page, limit)
	builder.WriteString(pageInfoLine(pageOut, totalPages))
	builder.WriteString(filterInfoLine(filter))

	for _, r := range txcontrib[start:end] {

		builder.WriteString(fmt.Sprintf(
			"<b> %s </b> \n addr %s  \n contrib : %.2f%%\n\n",
			html.EscapeString(r.Moniker), html.EscapeString(r.Addr), r.TxContrib,
		))

	}

	return builder.String(), pageOut, totalPages, nil
}

func formatMissing(db *gorm.DB, chainID, period string, page, limit int, filter string) (msg string, pageOut, totalPages int, err error) {
	var rows []database.MissingBlockMetrics
	cacheKey := chainID + ":missing:" + period
	if cached, ok := getCached(cacheKey); ok {
		if v, ok := cached.([]database.MissingBlockMetrics); ok {
			rows = v
		}
	} else {
		var fetchErr error
		rows, fetchErr = database.MissingBlock(db, chainID, period)
		if fetchErr != nil {
			return "", 1, 1, fmt.Errorf("failed to get missing block: %w", fetchErr)
		}
		setCached(cacheKey, rows, cacheTTL)
	}
	rows = filterMissing(rows, filter)
	if len(rows) == 0 {
		return fmt.Sprintf("🕵️ <b>Missing blocks — %s</b>\nNo data.", html.EscapeString(period)), 1, 1, nil
	}

	// Sort by missed blocks (desc)
	sort.Slice(rows, func(i, j int) bool { return rows[i].MissingBlock > rows[j].MissingBlock })

	var b strings.Builder

	b.WriteString(fmt.Sprintf("🕵️ <b>Missing Blocks — %s</b>\n\n", html.EscapeString(period)))

	pageOut, start, end, totalPages := paginate(len(rows), page, limit)
	b.WriteString(pageInfoLine(pageOut, totalPages))
	b.WriteString(filterInfoLine(filter))

	for i, r := range rows[start:end] {
		emoji := "🟢"
		switch {
		case r.MissingBlock > 50:
			emoji = "🔴"
		case r.MissingBlock > 20:
			emoji = "🟠"
		case r.MissingBlock > 5:
			emoji = "🟡"
		}

		b.WriteString(fmt.Sprintf(
			"%d. %s <b>%s</b>\n<code>%s</code>\nMissing: <b>%d blocks</b> \n\n",
			start+i+1, emoji, html.EscapeString(r.Moniker), r.Addr, r.MissingBlock,
		))
	}
	return b.String(), pageOut, totalPages, nil
}
func reportActivate(db *gorm.DB, chatID int64, chainID, isActivate string) (string, error) {
	if isActivate == "" {
		status, err := database.GetTelegramReportStatus(db, chatID, chainID)
		if err != nil {
			return "", fmt.Errorf("failed to get status report: %w", err)
		}

		if status {
			return fmt.Sprintf("📝 The daily report is activated ✅ (chain: <code>%s</code>)", html.EscapeString(chainID)), nil
		}
		return fmt.Sprintf("📝 The daily report is disabled ❌ (chain: <code>%s</code>)", html.EscapeString(chainID)), nil
	}

	switch strings.ToLower(isActivate) {
	case "true", "on", "enable", "activate":
		if err := database.ActivateTelegramReport(db, true, chatID, chainID); err != nil {
			return "", fmt.Errorf("failed to activate report: %w", err)
		}
		return fmt.Sprintf("✅ The daily report has been activated (chain: <code>%s</code>).", html.EscapeString(chainID)), nil

	case "false", "off", "disable", "deactivate":
		if err := database.ActivateTelegramReport(db, false, chatID, chainID); err != nil {
			return "", fmt.Errorf("failed to deactivate report: %w", err)
		}
		return fmt.Sprintf("🚫 The daily report has been disabled (chain: <code>%s</code>).", html.EscapeString(chainID)), nil

	default:
		return "⚠️ Invalid argument. Use `/report activate=true` or `/report activate=false`.", nil
	}
}
func handleSubscribe(token string, db *gorm.DB, chatID int64, chainID, args string) {
	fields := strings.Fields(args)
	if len(fields) == 0 || fields[0] == "help" {
		_ = SendMessageTelegram(token, chatID, subscribeUsage())
		return
	}

	cmd := strings.ToLower(fields[0])
	rest := fields[1:]

	switch cmd {
	case "list":
		subs, err := database.GetValidatorStatusList(db, chatID, chainID)
		if err != nil {
			log.Printf("[telegram] subscribe list failed: %v", err)
			_ = SendMessageTelegram(token, chatID, "⚠️ Unable to fetch list of validators.")
			return
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("🧾 <b>Your subscriptions</b> (chain: <code>%s</code>)\n", html.EscapeString(chainID)))
		for _, s := range subs {

			b.WriteString(fmt.Sprintf("• %s \n (%s)\n<b>%s</b>\n", s.Moniker, s.Addr, s.Status))
		}
		_ = SendMessageTelegram(token, chatID, b.String())
		return

	case "on":
		if len(rest) == 0 {
			_ = SendMessageTelegram(token, chatID, "Usage: /subscribe on <addr> [more...]")
			return
		}
		if strings.ToLower(rest[0]) == "all" {
			vals, err := database.GetAllValidators(db, chainID)
			if err != nil {
				_ = SendMessageTelegram(token, chatID, "⚠️ Unable to fetch validator list.")
				return
			}
			changed := 0
			for _, v := range vals {
				if err := database.UpdateTelegramValidatorSubStatus(db, chatID, chainID, v.Addr, v.Moniker, "subscribe"); err == nil {
					changed++
				}
			}
			_ = SendMessageTelegram(token, chatID, fmt.Sprintf("✅ Enabled alerts for <b>%d</b> validators.", changed))
			return
		}

		moniker, err := database.ResolveAddrs(db, chainID, rest)
		if err != nil {
			_ = SendMessageTelegram(token, chatID, "⚠️ Some validators could not be resolved.")
		}
		if len(moniker) == 0 {
			_ = SendMessageTelegram(token, chatID, "No valid validators found.")
			return
		}
		var ok, fail int
		for _, m := range moniker {
			if err := database.UpdateTelegramValidatorSubStatus(db, chatID, chainID, m.Addr, m.Moniker, "subscribe"); err != nil {
				fail++
			} else {
				_ = SendMessageTelegram(token, chatID, fmt.Sprintf("✅ Enabled alerts for <b>%s</b> validators.", m.Moniker))
				ok++
			}
		}
		// _ = SendMessageTelegram(token, chatID, fmt.Sprintf("✅ Unsubscribed: %d | ❌ Failed: %d", ok, fail))

		return

	case "off":
		if len(rest) == 0 {
			_ = SendMessageTelegram(token, chatID, "Usage: /subscribe off <addr|moniker>|all")
			return
		}
		if strings.ToLower(rest[0]) == "all" {
			subs, err := database.GetTelegramValidatorSub(db, chatID, chainID, true)
			if err != nil {
				_ = SendMessageTelegram(token, chatID, "⚠️ Unable to fetch your active subscriptions.")
				return
			}
			var ok int
			for _, s := range subs {
				if err := database.UpdateTelegramValidatorSubStatus(db, chatID, chainID, s.Addr, s.Moniker, "unsubscribe"); err == nil {
					ok++
				}
			}
			_ = SendMessageTelegram(token, chatID, fmt.Sprintf("🛑 Disabled alerts for <b>%d</b> validators.", ok))
			return
		}
		moniker, err := database.ResolveAddrs(db, chainID, rest)
		if err != nil {
			_ = SendMessageTelegram(token, chatID, "⚠️ Some validators could not be resolved.")
		}
		if len(moniker) == 0 {
			_ = SendMessageTelegram(token, chatID, "No valid validators found.")
			return
		}
		var ok, fail int
		for _, m := range moniker {
			if err := database.UpdateTelegramValidatorSubStatus(db, chatID, chainID, m.Addr, m.Moniker, "unsubscribe"); err != nil {
				fail++
			} else {
				_ = SendMessageTelegram(token, chatID, fmt.Sprintf("🛑 Disabled alerts for <b>%s</b> validators.", m.Moniker))
				ok++
			}
		}

	default:
		_ = SendMessageTelegram(token, chatID, subscribeUsage())
		return
	}
}

func subscribeUsage() string {
	return `📬 <b>Subscribe command</b>
/subscribe list  — show your subscriptions
/subscribe on addr addr2 — enable alerts
/subscribe off addr addr2 — disable alerts
/subscribe on all — enable all
/subscribe off all — disable all`
}

func BuildTelegramCallbackHandler(token string, db *gorm.DB, defaultChainID string) func(int64, int, string) {
	return func(chatID int64, messageID int, data string) {
		chainID := getActiveChain(chatID, defaultChainID)
		cmdKey, page, limit, period, filter, sortOrder, action, ok := parseCallbackData(data)
		if !ok {
			return
		}
		if action == "search" {
			setSearchState(chatID, SearchState{
				BotType:   "validator",
				ChainID:   chainID,
				Cmd:       cmdKey,
				Page:      1,
				Limit:     limit,
				Period:    period,
				SortOrder: sortOrder,
				ExpiresAt: time.Now().Add(searchTTL),
			})
			_ = SendMessageTelegram(token, chatID, "🔎 Tapez un moniker ou une adresse (ou /cancel).")
			return
		}

		msg, markup, err := buildPaginatedResponse(db, chainID, cmdKey, period, filter, page, limit, sortOrder)
		if err != nil {
			log.Printf("[telegram] paginated response error cmd=%s: %v", cmdKey, err)
			return
		}
		if err := EditMessageTelegramWithMarkup(token, chatID, messageID, msg, markup); err != nil {
			log.Printf("[telegram] edit message failed cmd=%s: %v", cmdKey, err)
		}
	}
}

func sendPaginatedMessage(token string, chatID int64, db *gorm.DB, chainID, cmdKey, period, filter string, page, limit int, sortOrder string) {
	msg, markup, err := buildPaginatedResponse(db, chainID, cmdKey, period, filter, page, limit, sortOrder)
	if err != nil {
		log.Printf("[telegram] paginated response error cmd=%s: %v", cmdKey, err)
		_ = SendMessageTelegram(token, chatID, "⚠️ Unable to fetch data.")
		return
	}
	if err := SendMessageTelegramWithMarkup(token, chatID, msg, markup); err != nil {
		log.Printf("[telegram] send failed cmd=%s: %v", cmdKey, err)
	}
}

func buildPaginatedResponse(db *gorm.DB, chainID, cmdKey, period, filter string, page, limit int, sortOrder string) (string, *InlineKeyboardMarkup, error) {
	limit = clampLimit(limit)
	sortOrder = normalizeSort(sortOrder)
	switch cmdKey {
	case "status":
		if period == "" {
			period = periodDefault
		}
		msg, pageOut, totalPages, err := formatParticipationRAte(db, chainID, period, page, limit, filter, sortOrder)
		if err != nil {
			return "", nil, err
		}
		return msg, buildPaginationMarkup(cmdKey, pageOut, totalPages, limit, period, filter, sortOrder), nil

	case "uptime":
		msg, pageOut, totalPages, err := formatUptime(db, chainID, page, limit, filter, sortOrder)
		if err != nil {
			return "", nil, err
		}
		return msg, buildPaginationMarkup(cmdKey, pageOut, totalPages, limit, "", filter, sortOrder), nil

	case "operation_time":
		msg, pageOut, totalPages, err := formatOperationTime(db, chainID, page, limit, filter)
		if err != nil {
			return "", nil, err
		}
		return msg, buildPaginationMarkup(cmdKey, pageOut, totalPages, limit, "", filter, sortOrder), nil

	case "tx_contrib":
		if period == "" {
			period = periodDefault
		}
		msg, pageOut, totalPages, err := FormatTxcontrib(db, chainID, period, page, limit, filter, sortOrder)
		if err != nil {
			return "", nil, err
		}
		return msg, buildPaginationMarkup(cmdKey, pageOut, totalPages, limit, period, filter, sortOrder), nil

	case "missing":
		if period == "" {
			period = periodDefault
		}
		msg, pageOut, totalPages, err := formatMissing(db, chainID, period, page, limit, filter)
		if err != nil {
			return "", nil, err
		}
		return msg, buildPaginationMarkup(cmdKey, pageOut, totalPages, limit, period, filter, sortOrder), nil

	default:
		return "", nil, fmt.Errorf("unknown command key: %s", cmdKey)
	}
}

func buildPaginationMarkup(cmdKey string, page, totalPages, limit int, period, filter, sortOrder string) *InlineKeyboardMarkup {
	if totalPages <= 1 {
		if supportsSearch(cmdKey) || supportsPercentSort(cmdKey) {
			return buildSecondaryButtons(cmdKey, page, totalPages, limit, period, filter, sortOrder)
		}
		return nil
	}
	var rows [][]InlineKeyboardButton
	var row []InlineKeyboardButton
	if page > 1 {
		row = append(row, InlineKeyboardButton{
			Text:         "⬅️ Prev",
			CallbackData: encodeCallbackData(cmdKey, page-1, limit, period, filter, sortOrder, ""),
		})
	}
	if page < totalPages {
		row = append(row, InlineKeyboardButton{
			Text:         "➡️ Next",
			CallbackData: encodeCallbackData(cmdKey, page+1, limit, period, filter, sortOrder, ""),
		})
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if secondary := buildSecondaryButtons(cmdKey, page, totalPages, limit, period, filter, sortOrder); secondary != nil {
		rows = append(rows, secondary.InlineKeyboard...)
	}
	if len(rows) == 0 {
		return nil
	}
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func parseCallbackData(data string) (cmdKey string, page, limit int, period, filter, sortOrder, action string, ok bool) {
	params := parseCallbackParams(data)
	cmdKey = codeToCmd(params["c"])
	if cmdKey == "" {
		return "", 1, limitDefault, "", "", sortDefault, "", false
	}
	page = parseIntWithDefault(params["p"], 1, "page")
	limit = parseIntWithDefault(params["l"], limitDefault, "limit")
	period = decodePeriod(params["r"])
	filter = params["f"]
	sortOrder = decodeSort(params["s"])
	action = decodeAction(params["a"])
	return cmdKey, page, limit, period, filter, sortOrder, action, true
}

func parseCallbackParams(data string) map[string]string {
	out := map[string]string{}
	for _, tok := range strings.Split(data, "&") {
		kv := strings.SplitN(tok, "=", 2)
		if len(kv) == 2 {
			out[kv[0]] = kv[1]
		}
	}
	return out
}

func encodeCallbackData(cmdKey string, page, limit int, period, filter, sortOrder, action string) string {
	cmdCode := cmdToCode(cmdKey)
	if cmdCode == "" {
		cmdCode = cmdKey
	}
	var b strings.Builder
	b.WriteString("c=")
	b.WriteString(cmdCode)
	b.WriteString("&p=")
	b.WriteString(strconv.Itoa(page))
	b.WriteString("&l=")
	b.WriteString(strconv.Itoa(limit))
	if period != "" {
		if p := encodePeriod(period); p != "" {
			b.WriteString("&r=")
			b.WriteString(p)
		}
	}
	if s := encodeSort(sortOrder); s != "" {
		b.WriteString("&s=")
		b.WriteString(s)
	}
	if a := encodeAction(action); a != "" {
		b.WriteString("&a=")
		b.WriteString(a)
	}
	if filter != "" {
		f := sanitizeCallbackValue(filter, 20)
		if f != "" {
			b.WriteString("&f=")
			b.WriteString(f)
		}
	}
	return b.String()
}

func buildSecondaryButtons(cmdKey string, page, totalPages, limit int, period, filter, sortOrder string) *InlineKeyboardMarkup {
	var row []InlineKeyboardButton
	if supportsPercentSort(cmdKey) {
		nextSort := toggleSort(sortOrder)
		row = append(row, InlineKeyboardButton{
			Text:         "↕️ Sort %",
			CallbackData: encodeCallbackData(cmdKey, 1, limit, period, filter, nextSort, ""),
		})
	}
	if supportsSearch(cmdKey) {
		row = append(row, InlineKeyboardButton{
			Text:         "🔎 Search",
			CallbackData: encodeCallbackData(cmdKey, page, limit, period, filter, sortOrder, "search"),
		})
	}
	if len(row) == 0 {
		return nil
	}
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{row}}
}

func supportsPercentSort(cmdKey string) bool {
	switch cmdKey {
	case "status", "uptime", "tx_contrib":
		return true
	default:
		return false
	}
}

func supportsSearch(cmdKey string) bool {
	switch cmdKey {
	case "status", "uptime", "operation_time", "tx_contrib", "missing":
		return true
	default:
		return false
	}
}

func cmdToCode(cmdKey string) string {
	switch cmdKey {
	case "status":
		return "st"
	case "uptime":
		return "up"
	case "operation_time":
		return "op"
	case "tx_contrib":
		return "tx"
	case "missing":
		return "ms"
	default:
		return ""
	}
}

func codeToCmd(code string) string {
	switch code {
	case "st", "status":
		return "status"
	case "up", "uptime":
		return "uptime"
	case "op", "operation_time":
		return "operation_time"
	case "tx", "tx_contrib":
		return "tx_contrib"
	case "ms", "missing":
		return "missing"
	default:
		return ""
	}
}

func encodePeriod(period string) string {
	switch period {
	case "current_week":
		return "cw"
	case "current_month":
		return "cm"
	case "current_year":
		return "cy"
	case "all_time":
		return "all"
	default:
		return ""
	}
}

func decodePeriod(code string) string {
	switch code {
	case "cw":
		return "current_week"
	case "cm":
		return "current_month"
	case "cy":
		return "current_year"
	case "all":
		return "all_time"
	default:
		return ""
	}
}

func sanitizeCallbackValue(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "&", "")
	s = strings.ReplaceAll(s, "=", "")
	return truncateRunes(s, maxLen)
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxLen {
			return s[:i]
		}
		count++
	}
	return s
}

type SearchState struct {
	BotType   string
	ChainID   string
	Cmd       string
	Page      int
	Limit     int
	Period    string
	SortOrder string
	ExpiresAt time.Time
}

var searchStateMu sync.Mutex
var searchState = map[int64]SearchState{}

func setSearchState(chatID int64, state SearchState) {
	searchStateMu.Lock()
	defer searchStateMu.Unlock()
	searchState[chatID] = state
}

func startSearchStateCleanup() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			searchStateMu.Lock()
			for chatID, s := range searchState {
				if time.Now().After(s.ExpiresAt) {
					delete(searchState, chatID)
				}
			}
			searchStateMu.Unlock()
		}
	}()
}

func HandleSearchInput(token string, db *gorm.DB, botType string, chatID int64, text string) bool {
	searchStateMu.Lock()
	state, ok := searchState[chatID]
	if !ok {
		searchStateMu.Unlock()
		return false
	}
	if state.BotType != botType {
		searchStateMu.Unlock()
		return false
	}
	if time.Now().After(state.ExpiresAt) {
		delete(searchState, chatID)
		searchStateMu.Unlock()
		return false
	}
	delete(searchState, chatID)
	searchStateMu.Unlock()

	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	if strings.EqualFold(t, "/cancel") {
		_ = SendMessageTelegram(token, chatID, "❌ Search annulée.")
		return true
	}
	if strings.HasPrefix(t, "/") {
		return false
	}
	sendPaginatedMessage(token, chatID, db, state.ChainID, state.Cmd, state.Period, t, 1, state.Limit, state.SortOrder)
	return true
}

func parseIntWithDefault(v string, def int, name string) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("[telegram] parse error param=%s: %v", name, err)
		return def
	}
	return n
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return limitDefault
	}
	if limit > limitMax {
		return limitMax
	}
	return limit
}

func normalizeSort(sortOrder string) string {
	switch strings.ToLower(sortOrder) {
	case "asc", "a":
		return "asc"
	case "desc", "d", "":
		return sortDefault
	default:
		return sortDefault
	}
}

func toggleSort(sortOrder string) string {
	if normalizeSort(sortOrder) == "asc" {
		return "desc"
	}
	return "asc"
}

func encodeSort(sortOrder string) string {
	switch normalizeSort(sortOrder) {
	case "asc":
		return "a"
	case "desc":
		return "d"
	default:
		return ""
	}
}

func decodeSort(code string) string {
	switch strings.ToLower(code) {
	case "a", "asc":
		return "asc"
	case "d", "desc":
		return "desc"
	default:
		return sortDefault
	}
}

func encodeAction(action string) string {
	switch action {
	case "search":
		return "s"
	default:
		return ""
	}
}

func decodeAction(code string) string {
	switch strings.ToLower(code) {
	case "s", "search":
		return "search"
	default:
		return ""
	}
}

func compareFloat(a, b float64, sortOrder string) bool {
	if normalizeSort(sortOrder) == "asc" {
		return a < b
	}
	return a > b
}

func paginate(total, page, limit int) (pageOut, start, end, totalPages int) {
	limit = clampLimit(limit)
	if page <= 0 {
		page = 1
	}
	if total <= 0 {
		return 1, 0, 0, 1
	}
	totalPages = (total + limit - 1) / limit
	if page > totalPages {
		page = totalPages
	}
	start = (page - 1) * limit
	end = start + limit
	if end > total {
		end = total
	}
	return page, start, end, totalPages
}

func pageInfoLine(page, totalPages int) string {
	if totalPages <= 1 {
		return ""
	}
	return fmt.Sprintf("Page %d/%d\n\n", page, totalPages)
}

func filterInfoLine(filter string) string {
	if filter == "" {
		return ""
	}
	return fmt.Sprintf("Filter: <code>%s</code>\n\n", html.EscapeString(filter))
}

func matchesFilter(filter, moniker, addr string) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(filter)
	return strings.Contains(strings.ToLower(moniker), f) || strings.Contains(strings.ToLower(addr), f)
}

func filterParticipationRates(in []database.ParticipationRate, filter string) []database.ParticipationRate {
	if filter == "" {
		return in
	}
	out := make([]database.ParticipationRate, 0, len(in))
	for _, r := range in {
		if matchesFilter(filter, r.Moniker, r.Addr) {
			out = append(out, r)
		}
	}
	return out
}

func filterUptimeMetrics(in []database.UptimeMetrics, filter string) []database.UptimeMetrics {
	if filter == "" {
		return in
	}
	out := make([]database.UptimeMetrics, 0, len(in))
	for _, r := range in {
		if matchesFilter(filter, r.Moniker, r.Addr) {
			out = append(out, r)
		}
	}
	return out
}

func filterOperationTimeMetrics(in []database.OperationTimeMetrics, filter string) []database.OperationTimeMetrics {
	if filter == "" {
		return in
	}
	out := make([]database.OperationTimeMetrics, 0, len(in))
	for _, r := range in {
		if matchesFilter(filter, r.Moniker, r.Addr) {
			out = append(out, r)
		}
	}
	return out
}

func filterTxContrib(in []database.TxContribMetrics, filter string) []database.TxContribMetrics {
	if filter == "" {
		return in
	}
	out := make([]database.TxContribMetrics, 0, len(in))
	for _, r := range in {
		if matchesFilter(filter, r.Moniker, r.Addr) {
			out = append(out, r)
		}
	}
	return out
}

func filterMissing(in []database.MissingBlockMetrics, filter string) []database.MissingBlockMetrics {
	if filter == "" {
		return in
	}
	out := make([]database.MissingBlockMetrics, 0, len(in))
	for _, r := range in {
		if matchesFilter(filter, r.Moniker, r.Addr) {
			out = append(out, r)
		}
	}
	return out
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

	b.WriteString("<code>⏱️ /operation_time [limit=N]</code>\n")
	b.WriteString("Shows operation time since last down/up event.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/operation_time</code> (default: limit=10)\n")
	b.WriteString("• <code>/operation_time limit=3</code>\n\n")

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

	b.WriteString("📄 <b>Pagination</b>\n")
	b.WriteString("Results are paginated with inline buttons. You can also pass <code>page=N</code>.\n")
	b.WriteString("Optional filter: <code>filter=...</code> (moniker or address).\n")
	b.WriteString("Sort (percent commands): <code>sort=asc</code> or <code>sort=desc</code>.\n")
	b.WriteString("Search button prompts for a moniker or address.\n\n")

	b.WriteString("📬 <b>Subscribe command</b> \n")

	b.WriteString("Show your active subscriptions and available validators\n")
	b.WriteString("• <code>/subscribe list </code>\n")

	b.WriteString("Enable alerts for one or more validators\n")
	b.WriteString("• <code>/subscribe on [addr] [more...]</code>\n")

	b.WriteString("Disable alerts for one or more validators\n")
	b.WriteString("• <code>/subscribe off [addr] [more...]</code>\n ")

	b.WriteString("Enable alerts for all validators\n")
	b.WriteString("• <code>/subscribe on all </code>\n")

	b.WriteString("Disable alerts for all validators\n")
	b.WriteString("• <code>/subscribe off all </code>\n")

	b.WriteString("\n⛓️ <b>Multi-chain</b>\n")
	b.WriteString("• <code>/chain</code> — show current chain and available chains\n")
	b.WriteString("• <code>/setchain chain=&lt;id&gt;</code> — switch active chain for this chat\n\n")

	b.WriteString("ℹ️ Parameters must be written as <code>key=value</code> (e.g. <code>period=current_week</code>).\n")

	return b.String()
}
