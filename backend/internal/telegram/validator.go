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

// schedulerReloader is a narrow interface so telegram does not need to import
// the scheduler package (which would create an import cycle).
type schedulerReloader interface {
	ReloadForTelegram(chatID int64, chainID string, db *gorm.DB) error
}

// SchedulerInstance must be set by main.go before BuildTelegramHandlers is called.
var SchedulerInstance schedulerReloader

// enabledChainsSnapshot is populated by BuildTelegramHandlers and used by the
// interactive /cmd menu to list and validate chain IDs without changing the
// public API of BuildTelegramCallbackHandler.
var enabledChainsSnapshot []string

const (
	periodDefault = "current_month"
	limitDefault  = 10
	limitMax      = 50
	sortDefault   = "desc"
	searchTTL     = 2 * time.Minute
)

const cacheTTL = 45 * time.Second

// ChainHealthSnapshot mirrors gnovalidator.ChainHealthSnapshot. It is defined
// here to avoid a circular import (gnovalidator imports the internal package
// which imports telegram). The concurrent agent populates this via
// SetChainHealthFetcher once health.go is ready.
type ChainHealthSnapshot struct {
	LatestBlockHeight int64
	LatestBlockTime   time.Time
	ConsensusRound    int
	RPCReachable      bool
	IsStuck           bool
	IsDisabled        bool
	// ValidatorLiveness holds liveness from the last committed block's precommits.
	// true = validator signed; false = validator did not sign (MISSING).
	// nil means the data was unavailable (RPC unreachable or block data missing).
	ValidatorLiveness map[string]bool
	// Monikers maps validator address to display name for liveness formatting.
	Monikers       map[string]string
	ValidatorRates map[string]ValidatorRate
	MinBlock       int64
	MaxBlock       int64
	AlertsLast24h  []database.AlertSummary
}

// ValidatorRate mirrors gnovalidator.ValidatorRate.
type ValidatorRate struct {
	Rate    float64
	Moniker string
}

// ChainHealthFetcher is the function called by the /status handler to obtain a
// live snapshot. It is nil until SetChainHealthFetcher is called (typically
// from main.go after gnovalidator is initialised). When nil, /status falls
// back to a "not available" message.
var ChainHealthFetcher func(chainID string) ChainHealthSnapshot

// ChainDisabledFormatter and ChainStuckFormatter are format helpers provided
// by the gnovalidator package. They are set alongside ChainHealthFetcher.
var ChainDisabledFormatter func(chainID string, snap ChainHealthSnapshot) string
var ChainStuckFormatter func(chainID string, snap ChainHealthSnapshot) string

// AlertsFormatter formats the last-24h alert section. Set from main.go to
// gnovalidator.FormatAlertsLast24h to avoid a circular import.
var AlertsFormatter func(alerts []database.AlertSummary) string

// SetChainHealthFetcher registers the live-health fetch function and its
// format helpers. Called once from main.go.
func SetChainHealthFetcher(
	fetcher func(chainID string) ChainHealthSnapshot,
	disabledFmt func(chainID string, snap ChainHealthSnapshot) string,
	stuckFmt func(chainID string, snap ChainHealthSnapshot) string,
) {
	ChainHealthFetcher = fetcher
	ChainDisabledFormatter = disabledFmt
	ChainStuckFormatter = stuckFmt
}

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
	enabledChainsSnapshot = enabledChains
	return map[string]func(int64, string){

		"/status": func(chatID int64, args string) {
			chainID := getActiveChain(chatID, defaultChainID)
			if ChainHealthFetcher == nil {
				_ = SendMessageTelegram(token, chatID, "⚠️ Chain health data is not available yet.")
				return
			}
			snap := ChainHealthFetcher(chainID)
			msg := formatChainHealthMessage(chainID, snap)
			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("[telegram] send /status failed: %v", err)
			}
		},

		"/rate": func(chatID int64, args string) {
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
			sendPaginatedMessage(token, chatID, db, chainID, "rate", period, filter, page, limit, sortOrder)
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

			// Schedule change: hour= or minute= or timezone= present → route to reportSchedule.
			if params["hour"] != "" || params["minute"] != "" || params["timezone"] != "" {
				msg := reportSchedule(db, SchedulerInstance, chatID, chainID, params)
				_ = SendMessageTelegram(token, chatID, msg)
				return
			}

			// Existing activate/deactivate/status path.
			msg, err := reportActivate(db, chatID, chainID, params["activate"])
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

		"/cmd": func(chatID int64, _ string) {
			markup := buildCmdRootMarkup()
			_ = SendMessageTelegramWithMarkup(token, chatID, "🎛 What do you want to do?", markup)
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
		return fmt.Sprintf("📊 <b>Participation rates — %s</b> — <code>%s</code>\nNo data.", html.EscapeString(period), html.EscapeString(chainID)), 1, 1, nil
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("📊 <b>Participation rates — %s</b> — <code>%s</code>\n\n", html.EscapeString(period), html.EscapeString(chainID)))

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
		return fmt.Sprintf("🕘 <b>Uptime metrics</b> — <code>%s</code>\nNo data.", html.EscapeString(chainID)), 1, 1, nil
	}

	// Sort by uptime
	sort.Slice(results, func(i, j int) bool { return compareFloat(results[i].Uptime, results[j].Uptime, sortOrder) })

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("🕘 <b>Uptime metrics</b> — <code>%s</code>\n\n", html.EscapeString(chainID)))

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
		return fmt.Sprintf("🕘 <b>Operation time metrics</b> — <code>%s</code>\nNo data.", html.EscapeString(chainID)), 1, 1, nil
	}

	// Sort by  (desc)
	sort.Slice(results, func(i, j int) bool { return results[i].DaysDiff > results[j].DaysDiff })

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("🕘 <b>Operation time metrics</b> — <code>%s</code>\n\n", html.EscapeString(chainID)))

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
		return fmt.Sprintf("⚙️ <b>Tx Contrib — %s</b> — <code>%s</code>\nNo data.", html.EscapeString(period), html.EscapeString(chainID)), 1, 1, nil
	}

	// Sort by contribution
	sort.Slice(txcontrib, func(i, j int) bool { return compareFloat(txcontrib[i].TxContrib, txcontrib[j].TxContrib, sortOrder) })

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("⚙️ <b>Tx Contrib — %s</b> — <code>%s</code>\n\n", html.EscapeString(period), html.EscapeString(chainID)))

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
		return fmt.Sprintf("🕵️ <b>Missing blocks — %s</b> — <code>%s</code>\nNo data.", html.EscapeString(period), html.EscapeString(chainID)), 1, 1, nil
	}

	// Sort by missed blocks (desc)
	sort.Slice(rows, func(i, j int) bool { return rows[i].MissingBlock > rows[j].MissingBlock })

	var b strings.Builder

	b.WriteString(fmt.Sprintf("🕵️ <b>Missing Blocks — %s</b> — <code>%s</code>\n\n", html.EscapeString(period), html.EscapeString(chainID)))

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
// reportSchedule updates the daily report schedule (hour, minute, timezone) for a chat.
// Unspecified params keep their current DB values. The scheduler goroutine is reloaded
// immediately so the new time takes effect without a process restart.
func reportSchedule(db *gorm.DB, sched schedulerReloader, chatID int64, chainID string, params map[string]string) string {
	current, err := database.GetHourTelegramReport(db, chatID, chainID)
	if err != nil {
		return "⚠️ No active report schedule found. Run <code>/report activate=true</code> first, then set the schedule."
	}

	hour := current.DailyReportHour
	minute := current.DailyReportMinute
	tz := current.Timezone

	if v := params["hour"]; v != "" {
		h, err := strconv.Atoi(v)
		if err != nil || h < 0 || h > 23 {
			return "⚠️ Invalid hour. Must be an integer between 0 and 23."
		}
		hour = h
	}
	if v := params["minute"]; v != "" {
		m, err := strconv.Atoi(v)
		if err != nil || m < 0 || m > 59 {
			return "⚠️ Invalid minute. Must be an integer between 0 and 59."
		}
		minute = m
	}
	if v := params["timezone"]; v != "" {
		if _, err := time.LoadLocation(v); err != nil {
			return fmt.Sprintf("⚠️ Unknown timezone <code>%s</code>. Use IANA format, e.g. <code>Europe/Paris</code>.", html.EscapeString(v))
		}
		tz = v
	}

	if err := database.UpdateTelegramScheduleAdmin(db, chatID, chainID, hour, minute, tz, current.Activate); err != nil {
		return fmt.Sprintf("❌ Failed to update schedule: %v", err)
	}

	if sched != nil {
		if err := sched.ReloadForTelegram(chatID, chainID, db); err != nil {
			log.Printf("[telegram] scheduler reload failed (chat %d chain %s): %v", chatID, chainID, err)
		}
	}

	return fmt.Sprintf(
		"✅ Report schedule updated (chain: <code>%s</code>)\nTime: <b>%02d:%02d</b> — Timezone: <code>%s</code>",
		html.EscapeString(chainID), hour, minute, html.EscapeString(tz),
	)
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

		// Parse raw params first so menu callbacks can read them directly.
		rawParams := parseCallbackParams(data)
		cmdKey := codeToCmd(rawParams["c"])

		// Dispatch interactive menu callbacks before touching paginated paths.
		if cmdKey == "menu" {
			handleCmdMenuCallback(token, db, chatID, messageID, chainID, defaultChainID, enabledChainsSnapshot, rawParams)
			return
		}

		_, page, limit, period, filter, sortOrder, action, ok := parseCallbackData(data)
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

	case "rate":
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
	case "status", "rate", "uptime", "tx_contrib":
		return true
	default:
		return false
	}
}

func supportsSearch(cmdKey string) bool {
	switch cmdKey {
	case "status", "rate", "uptime", "operation_time", "tx_contrib", "missing":
		return true
	default:
		return false
	}
}

func cmdToCode(cmdKey string) string {
	switch cmdKey {
	case "status":
		return "st"
	case "rate":
		return "rt"
	case "uptime":
		return "up"
	case "operation_time":
		return "op"
	case "tx_contrib":
		return "tx"
	case "missing":
		return "ms"
	case "menu":
		return "mn"
	case "confirm":
		return "cf"
	case "cancel":
		return "cx"
	case "subscribe":
		return "sb"
	case "report":
		return "rp"
	case "validators":
		return "vl"
	case "chain":
		return "ch"
	case "vsel":
		return "vs"
	default:
		return ""
	}
}

func codeToCmd(code string) string {
	switch code {
	case "st", "status":
		return "status"
	case "rt", "rate":
		return "rate"
	case "up", "uptime":
		return "uptime"
	case "op", "operation_time":
		return "operation_time"
	case "tx", "tx_contrib":
		return "tx_contrib"
	case "ms", "missing":
		return "missing"
	case "mn", "menu":
		return "menu"
	case "cf", "confirm":
		return "confirm"
	case "cx", "cancel":
		return "cancel"
	case "sb", "subscribe":
		return "subscribe"
	case "rp", "report":
		return "report"
	case "vl", "validators":
		return "validators"
	case "ch", "chain":
		return "chain"
	case "vs", "vsel":
		return "vsel"
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

			cmdStateMu.Lock()
			for chatID, s := range cmdState {
				if time.Now().After(s.ExpiresAt) {
					delete(cmdState, chatID)
				}
			}
			cmdStateMu.Unlock()
		}
	}()
}

// CmdState holds the interactive menu session for a single chat.
type CmdState struct {
	Step          string    // "root", "action", "validators", "confirm"
	Command       string    // "subscribe", "report", "validators", "chain"
	Action        string    // "on", "off", "status", "schedule", etc.
	ChainID       string
	ValidatorPage []string  // addresses shown on current validator-select page
	SelectedAddrs []string  // addresses confirmed by user
	Period        string
	ExpiresAt     time.Time
}

const cmdStateTTL = 5 * time.Minute

var cmdState   = map[int64]CmdState{}
var cmdStateMu sync.Mutex

func setCmdState(chatID int64, state CmdState) {
	cmdStateMu.Lock()
	defer cmdStateMu.Unlock()
	cmdState[chatID] = state
}

func getCmdState(chatID int64) (CmdState, bool) {
	cmdStateMu.Lock()
	defer cmdStateMu.Unlock()
	s, ok := cmdState[chatID]
	return s, ok
}

func deleteCmdState(chatID int64) {
	cmdStateMu.Lock()
	defer cmdStateMu.Unlock()
	delete(cmdState, chatID)
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

// encodeCmdCallback builds a compact callback data string for menu actions.
// It always sets c=mn so the callback dispatcher routes to handleCmdMenuCallback.
// Extra key=value pairs are passed via extras (must not contain & or =).
func encodeCmdCallback(extras ...string) string {
	var b strings.Builder
	b.WriteString("c=mn")
	for i := 0; i+1 < len(extras); i += 2 {
		b.WriteByte('&')
		b.WriteString(extras[i])
		b.WriteByte('=')
		b.WriteString(extras[i+1])
	}
	return b.String()
}

// buildCmdRootMarkup returns the 4-button root menu.
func buildCmdRootMarkup() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "Subscribe", CallbackData: encodeCmdCallback("a", "sb")},
				{Text: "Report", CallbackData: encodeCmdCallback("a", "rp")},
			},
			{
				{Text: "Validators", CallbackData: encodeCmdCallback("a", "vl")},
				{Text: "Chain", CallbackData: encodeCmdCallback("a", "ch")},
			},
		},
	}
}

const cmdValidatorPageSize = 5

// buildValidatorSelectMarkup builds an inline keyboard for validator selection.
// Returns the markup and the slice of addresses for the current page (to store
// in CmdState.ValidatorPage).
func buildValidatorSelectMarkup(validators []database.AddrMoniker, page int, selectedAddrs []string) (*InlineKeyboardMarkup, []string) {
	total := len(validators)
	if total == 0 {
		return nil, nil
	}
	totalPages := (total + cmdValidatorPageSize - 1) / cmdValidatorPageSize
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * cmdValidatorPageSize
	end := start + cmdValidatorPageSize
	if end > total {
		end = total
	}
	pageSlice := validators[start:end]

	pageAddrs := make([]string, len(pageSlice))
	for i, v := range pageSlice {
		pageAddrs[i] = v.Addr
	}

	selectedSet := map[string]bool{}
	for _, a := range selectedAddrs {
		selectedSet[a] = true
	}

	var rows [][]InlineKeyboardButton

	// One button per validator on this page.
	for i, v := range pageSlice {
		prefix := ""
		if selectedSet[v.Addr] {
			prefix = "✅ "
		}
		label := v.Moniker
		if label == "" {
			label = v.Addr
		}
		rows = append(rows, []InlineKeyboardButton{
			{
				Text:         prefix + label,
				CallbackData: encodeCmdCallback("a", "vs", "i", strconv.Itoa(i)),
			},
		})
	}

	// Navigation row.
	var navRow []InlineKeyboardButton
	if page > 1 {
		navRow = append(navRow, InlineKeyboardButton{
			Text:         "⬅️ Prev",
			CallbackData: encodeCmdCallback("a", "vs", "i", "prev", "pg", strconv.Itoa(page-1)),
		})
	}
	if page < totalPages {
		navRow = append(navRow, InlineKeyboardButton{
			Text:         "➡️ Next",
			CallbackData: encodeCmdCallback("a", "vs", "i", "next", "pg", strconv.Itoa(page+1)),
		})
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}

	// Control row: [All validators] and [Done].
	rows = append(rows, []InlineKeyboardButton{
		{Text: "[All validators]", CallbackData: encodeCmdCallback("a", "vs", "i", "-1")},
		{Text: "✅ Done", CallbackData: encodeCmdCallback("a", "cf")},
	})

	return &InlineKeyboardMarkup{InlineKeyboard: rows}, pageAddrs
}

// handleCmdMenuCallback routes interactive menu callbacks.
func handleCmdMenuCallback(
	token string,
	db *gorm.DB,
	chatID int64,
	messageID int,
	chainID string,
	defaultChainID string,
	enabledChains []string,
	params map[string]string,
) {
	action := params["a"]

	// Cancel from any step.
	if action == "cx" {
		deleteCmdState(chatID)
		_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "❌ Cancelled.", nil)
		return
	}

	// Confirm: execute the pending command.
	if action == "cf" {
		state, ok := getCmdState(chatID)
		if !ok || time.Now().After(state.ExpiresAt) {
			deleteCmdState(chatID)
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⏱ Session expired — run /cmd again.", nil)
			return
		}
		result := executeCmdMenuAction(token, db, chatID, chainID, state)
		deleteCmdState(chatID)
		_ = EditMessageTelegramWithMarkup(token, chatID, messageID, result, nil)
		return
	}

	// Root-level action selection: sb / rp / vl / ch.
	if action == "sb" || action == "rp" || action == "vl" || action == "ch" {
		cmdMap := map[string]string{
			"sb": "subscribe",
			"rp": "report",
			"vl": "validators",
			"ch": "chain",
		}
		command := cmdMap[action]
		setCmdState(chatID, CmdState{
			Step:    "action",
			Command: command,
			ChainID: chainID,
			ExpiresAt: time.Now().Add(cmdStateTTL),
		})
		markup := buildActionMarkup(command, enabledChains)
		msg := fmt.Sprintf("🎛 <b>%s</b> — choose an action:", html.EscapeString(command))
		_ = EditMessageTelegramWithMarkup(token, chatID, messageID, msg, markup)
		return
	}

	// Validator selection step.
	if action == "vs" {
		state, ok := getCmdState(chatID)
		if !ok || time.Now().After(state.ExpiresAt) {
			deleteCmdState(chatID)
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⏱ Session expired — run /cmd again.", nil)
			return
		}

		indexStr := params["i"]

		// Page navigation (Prev/Next buttons encode i=prev or i=next plus pg=N).
		if indexStr == "prev" || indexStr == "next" {
			newPage := parseIntWithDefault(params["pg"], 1, "pg")
			validators, err := database.GetAllValidators(db, state.ChainID)
			if err != nil || len(validators) == 0 {
				_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⚠️ No validators found.", nil)
				return
			}
			markup, pageAddrs := buildValidatorSelectMarkup(validators, newPage, state.SelectedAddrs)
			state.ValidatorPage = pageAddrs
			state.ExpiresAt = time.Now().Add(cmdStateTTL)
			setCmdState(chatID, state)
			msg := buildValidatorSelectMsg(state)
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, msg, markup)
			return
		}

		idx := parseIntWithDefault(indexStr, -2, "i")

		// Select all validators.
		if idx == -1 {
			state.SelectedAddrs = []string{"all"}
			state.ExpiresAt = time.Now().Add(cmdStateTTL)
			setCmdState(chatID, state)
			markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
				{
					{Text: "✅ Confirm", CallbackData: encodeCmdCallback("a", "cf")},
					{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")},
				},
			}}
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "All validators selected. Confirm?", markup)
			return
		}

		// Toggle selection for index idx.
		if idx >= 0 && idx < len(state.ValidatorPage) {
			addr := state.ValidatorPage[idx]
			found := false
			for i, a := range state.SelectedAddrs {
				if a == addr {
					// Deselect.
					state.SelectedAddrs = append(state.SelectedAddrs[:i], state.SelectedAddrs[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				state.SelectedAddrs = append(state.SelectedAddrs, addr)
			}
		}
		state.ExpiresAt = time.Now().Add(cmdStateTTL)
		setCmdState(chatID, state)

		validators, err := database.GetAllValidators(db, state.ChainID)
		if err != nil {
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⚠️ Unable to fetch validators.", nil)
			return
		}
		// Determine current page from ValidatorPage.
		currentPage := 1
		if len(state.ValidatorPage) > 0 && len(validators) > 0 {
			// Find the page that contains the first address of ValidatorPage.
			for p := 1; p <= (len(validators)+cmdValidatorPageSize-1)/cmdValidatorPageSize; p++ {
				s := (p - 1) * cmdValidatorPageSize
				e := s + cmdValidatorPageSize
				if e > len(validators) {
					e = len(validators)
				}
				if len(validators[s:e]) > 0 && validators[s].Addr == state.ValidatorPage[0] {
					currentPage = p
					break
				}
			}
		}
		markup, pageAddrs := buildValidatorSelectMarkup(validators, currentPage, state.SelectedAddrs)
		state.ValidatorPage = pageAddrs
		setCmdState(chatID, state)
		msg := buildValidatorSelectMsg(state)
		_ = EditMessageTelegramWithMarkup(token, chatID, messageID, msg, markup)
		return
	}

	// Period selection: user picked a period for a period-aware validator command.
	if action == "pd" {
		state, ok := getCmdState(chatID)
		if !ok || time.Now().After(state.ExpiresAt) {
			deleteCmdState(chatID)
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⏱ Session expired — run /cmd again.", nil)
			return
		}
		state.Period = params["v"]
		state.Step = "confirm"
		state.ExpiresAt = time.Now().Add(cmdStateTTL)
		setCmdState(chatID, state)
		markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "✅ Confirm", CallbackData: encodeCmdCallback("a", "cf")},
				{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")},
			},
		}}
		_ = EditMessageTelegramWithMarkup(token, chatID, messageID,
			fmt.Sprintf("Show <b>%s</b> — period: <b>%s</b> — chain: <code>%s</code>?",
				html.EscapeString(state.Action), html.EscapeString(state.Period), html.EscapeString(state.ChainID)),
			markup)
		return
	}

	// Action-level sub-actions from buildActionMarkup buttons.
	state, hasState := getCmdState(chatID)
	if !hasState || time.Now().After(state.ExpiresAt) {
		deleteCmdState(chatID)
		_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⏱ Session expired — run /cmd again.", nil)
		return
	}

	switch state.Command {
	case "subscribe":
		switch action {
		case "on", "off":
			state.Action = action
			if action == "off" {
				// off all goes straight to confirm.
				state.SelectedAddrs = []string{"all"}
				state.ExpiresAt = time.Now().Add(cmdStateTTL)
				setCmdState(chatID, state)
				markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
					{
						{Text: "✅ Confirm", CallbackData: encodeCmdCallback("a", "cf")},
						{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")},
					},
				}}
				_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "Disable alerts for all subscribed validators?", markup)
				return
			}
			// on → show validator selection.
			validators, err := database.GetAllValidators(db, state.ChainID)
			if err != nil || len(validators) == 0 {
				deleteCmdState(chatID)
				_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⚠️ No validators found.", nil)
				return
			}
			state.Step = "validators"
			state.ExpiresAt = time.Now().Add(cmdStateTTL)
			markup, pageAddrs := buildValidatorSelectMarkup(validators, 1, state.SelectedAddrs)
			state.ValidatorPage = pageAddrs
			setCmdState(chatID, state)
			msg := buildValidatorSelectMsg(state)
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, msg, markup)
		case "list":
			state.Action = "list"
			state.ExpiresAt = time.Now().Add(cmdStateTTL)
			setCmdState(chatID, state)
			markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
				{
					{Text: "✅ Confirm", CallbackData: encodeCmdCallback("a", "cf")},
					{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")},
				},
			}}
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "Show your current subscriptions?", markup)
		default:
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⚠️ Unknown action.", nil)
		}

	case "report":
		switch action {
		case "enable", "disable":
			state.Action = action
			state.ExpiresAt = time.Now().Add(cmdStateTTL)
			setCmdState(chatID, state)
			markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
				{
					{Text: "✅ Confirm", CallbackData: encodeCmdCallback("a", "cf")},
					{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")},
				},
			}}
			verb := "Enable"
			if action == "disable" {
				verb = "Disable"
			}
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID,
				fmt.Sprintf("%s the daily report for chain <code>%s</code>?", verb, html.EscapeString(state.ChainID)), markup)
		case "schedule":
			deleteCmdState(chatID)
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID,
				"Use <code>/report hour=H minute=M timezone=TZ</code> directly to set the schedule.", nil)
		case "status":
			state.Action = "status"
			state.ExpiresAt = time.Now().Add(cmdStateTTL)
			setCmdState(chatID, state)
			markup := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
				{
					{Text: "✅ Confirm", CallbackData: encodeCmdCallback("a", "cf")},
					{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")},
				},
			}}
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "Show report status?", markup)
		default:
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⚠️ Unknown action.", nil)
		}

	case "validators":
		switch action {
		case "status":
			// status is read-only and instant — execute directly, no confirm step needed.
			deleteCmdState(chatID)
			if ChainHealthFetcher == nil {
				_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⚠️ Chain health data is not available yet.", nil)
				return
			}
			snap := ChainHealthFetcher(state.ChainID)
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, formatChainHealthMessage(state.ChainID, snap), nil)
		case "uptime", "rate", "missing", "operation_time":
			// Period-aware commands: show period picker before confirm.
			state.Action = action
			state.Step = "period"
			state.ExpiresAt = time.Now().Add(cmdStateTTL)
			setCmdState(chatID, state)
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID,
				fmt.Sprintf("Choose period for <b>%s</b>:", html.EscapeString(action)),
				buildPeriodMarkup())
		default:
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⚠️ Unknown action.", nil)
		}

	case "chain":
		// Tapping a chain button sets it directly and confirms.
		requested := action
		if !validateChainID(requested, enabledChains) {
			_ = EditMessageTelegramWithMarkup(token, chatID, messageID,
				fmt.Sprintf("⚠️ Unknown chain <code>%s</code>.", html.EscapeString(requested)), nil)
			return
		}
		setActiveChain(chatID, requested)
		if err := database.UpdateChatChain(db, chatID, requested); err != nil {
			log.Printf("[telegram/cmd] UpdateChatChain chat=%d: %v", chatID, err)
		}
		deleteCmdState(chatID)
		_ = EditMessageTelegramWithMarkup(token, chatID, messageID,
			fmt.Sprintf("✅ Chain switched to <code>%s</code>.", html.EscapeString(requested)), nil)

	default:
		_ = EditMessageTelegramWithMarkup(token, chatID, messageID, "⚠️ Unknown command.", nil)
	}
}

// buildActionMarkup returns the action-level inline keyboard for the given command.
func buildActionMarkup(command string, enabledChains []string) *InlineKeyboardMarkup {
	cancelBtn := InlineKeyboardButton{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")}
	switch command {
	case "subscribe":
		return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "Enable (on)", CallbackData: encodeCmdCallback("a", "on")},
				{Text: "Disable (off)", CallbackData: encodeCmdCallback("a", "off")},
			},
			{
				{Text: "List subscriptions", CallbackData: encodeCmdCallback("a", "list")},
				cancelBtn,
			},
		}}
	case "report":
		return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "Enable", CallbackData: encodeCmdCallback("a", "enable")},
				{Text: "Disable", CallbackData: encodeCmdCallback("a", "disable")},
			},
			{
				{Text: "Status", CallbackData: encodeCmdCallback("a", "status")},
				{Text: "Schedule…", CallbackData: encodeCmdCallback("a", "schedule")},
			},
			{cancelBtn},
		}}
	case "validators":
		return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "Status",         CallbackData: encodeCmdCallback("a", "status")},
				{Text: "Uptime",         CallbackData: encodeCmdCallback("a", "uptime")},
			},
			{
				{Text: "Rate",           CallbackData: encodeCmdCallback("a", "rate")},
				{Text: "Missing blocks", CallbackData: encodeCmdCallback("a", "missing")},
			},
			{
				{Text: "Operation time", CallbackData: encodeCmdCallback("a", "operation_time")},
			},
			{cancelBtn},
		}}
	case "chain":
		var rows [][]InlineKeyboardButton
		for _, id := range enabledChains {
			rows = append(rows, []InlineKeyboardButton{
				{Text: id, CallbackData: encodeCmdCallback("a", id)},
			})
		}
		rows = append(rows, []InlineKeyboardButton{cancelBtn})
		return &InlineKeyboardMarkup{InlineKeyboard: rows}
	default:
		return nil
	}
}

// buildPeriodMarkup returns the period-selection inline keyboard used for
// period-aware validator commands (rate, uptime, missing, operation_time).
func buildPeriodMarkup() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{
			{Text: "Current week",  CallbackData: encodeCmdCallback("a", "pd", "v", "current_week")},
			{Text: "Current month", CallbackData: encodeCmdCallback("a", "pd", "v", "current_month")},
		},
		{
			{Text: "Current year", CallbackData: encodeCmdCallback("a", "pd", "v", "current_year")},
			{Text: "All time",     CallbackData: encodeCmdCallback("a", "pd", "v", "all_time")},
		},
		{{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")}},
	}}
}

// buildValidatorSelectMsg formats the header shown during the validator select step.
func buildValidatorSelectMsg(state CmdState) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🎛 <b>%s %s</b> — select validators:\n",
		html.EscapeString(state.Command), html.EscapeString(state.Action)))
	if len(state.SelectedAddrs) > 0 {
		b.WriteString(fmt.Sprintf("Selected: <b>%d</b>\n", len(state.SelectedAddrs)))
	}
	b.WriteString("Tap to toggle. Press ✅ Done when finished.")
	return b.String()
}

// executeCmdMenuAction carries out the confirmed menu action and returns the
// result message to display to the user.
func executeCmdMenuAction(token string, db *gorm.DB, chatID int64, chainID string, state CmdState) string {
	switch state.Command {
	case "subscribe":
		switch state.Action {
		case "list":
			subs, err := database.GetValidatorStatusList(db, chatID, state.ChainID)
			if err != nil {
				return "⚠️ Unable to fetch subscriptions."
			}
			if len(subs) == 0 {
				return fmt.Sprintf("🧾 No subscriptions (chain: <code>%s</code>).", html.EscapeString(state.ChainID))
			}
			var b strings.Builder
			b.WriteString(fmt.Sprintf("🧾 <b>Your subscriptions</b> (chain: <code>%s</code>)\n", html.EscapeString(state.ChainID)))
			for _, s := range subs {
				b.WriteString(fmt.Sprintf("• %s\n  (%s) — <b>%s</b>\n", s.Moniker, s.Addr, s.Status))
			}
			return b.String()

		case "on":
			if len(state.SelectedAddrs) == 0 {
				return "⚠️ No validators selected."
			}
			if len(state.SelectedAddrs) == 1 && state.SelectedAddrs[0] == "all" {
				vals, err := database.GetAllValidators(db, state.ChainID)
				if err != nil {
					return "⚠️ Unable to fetch validators."
				}
				changed := 0
				for _, v := range vals {
					if err := database.UpdateTelegramValidatorSubStatus(db, chatID, state.ChainID, v.Addr, v.Moniker, "subscribe"); err == nil {
						changed++
					}
				}
				return fmt.Sprintf("✅ Enabled alerts for <b>%d</b> validators.", changed)
			}
			var ok, fail int
			for _, addr := range state.SelectedAddrs {
				resolved, err := database.ResolveAddrs(db, state.ChainID, []string{addr})
				if err != nil || len(resolved) == 0 {
					fail++
					continue
				}
				if err := database.UpdateTelegramValidatorSubStatus(db, chatID, state.ChainID, resolved[0].Addr, resolved[0].Moniker, "subscribe"); err != nil {
					fail++
				} else {
					ok++
				}
			}
			return fmt.Sprintf("✅ Enabled: %d | ❌ Failed: %d", ok, fail)

		case "off":
			subs, err := database.GetTelegramValidatorSub(db, chatID, state.ChainID, true)
			if err != nil {
				return "⚠️ Unable to fetch active subscriptions."
			}
			var ok int
			for _, s := range subs {
				if err := database.UpdateTelegramValidatorSubStatus(db, chatID, state.ChainID, s.Addr, s.Moniker, "unsubscribe"); err == nil {
					ok++
				}
			}
			return fmt.Sprintf("🛑 Disabled alerts for <b>%d</b> validators.", ok)
		}

	case "report":
		switch state.Action {
		case "enable":
			msg, err := reportActivate(db, chatID, state.ChainID, "true")
			if err != nil {
				return fmt.Sprintf("❌ %v", err)
			}
			return msg
		case "disable":
			msg, err := reportActivate(db, chatID, state.ChainID, "false")
			if err != nil {
				return fmt.Sprintf("❌ %v", err)
			}
			return msg
		case "status":
			msg, err := reportActivate(db, chatID, state.ChainID, "")
			if err != nil {
				return fmt.Sprintf("❌ %v", err)
			}
			return msg
		}

	case "validators":
		period := state.Period
		if period == "" {
			period = periodDefault
		}
		switch state.Action {
		case "status":
			if ChainHealthFetcher == nil {
				return "⚠️ Chain health data is not available yet."
			}
			snap := ChainHealthFetcher(state.ChainID)
			return formatChainHealthMessage(state.ChainID, snap)
		case "uptime":
			msg, _, _, err := formatUptime(db, state.ChainID, 1, limitDefault, "", sortDefault)
			if err != nil {
				return fmt.Sprintf("❌ %v", err)
			}
			return msg
		case "rate":
			msg, _, _, err := formatParticipationRAte(db, state.ChainID, period, 1, limitDefault, "", sortDefault)
			if err != nil {
				return fmt.Sprintf("❌ %v", err)
			}
			return msg
		case "missing":
			msg, _, _, err := formatMissing(db, state.ChainID, period, 1, limitDefault, "")
			if err != nil {
				return fmt.Sprintf("❌ %v", err)
			}
			return msg
		case "operation_time":
			msg, _, _, err := formatOperationTime(db, state.ChainID, 1, limitDefault, "")
			if err != nil {
				return fmt.Sprintf("❌ %v", err)
			}
			return msg
		}
	}
	return "⚠️ Nothing to execute."
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

	b.WriteString("<code>🚦 /status</code>\n")
	b.WriteString("Chain health: last block age, consensus round, validator vote count.\n\n")

	b.WriteString("<code>📊 /rate [period=...] [limit=N]</code>\n")
	b.WriteString("Shows the historical participation rate of validators for a given period.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/rate</code> (defaults: period=current_month, limit=10)\n")
	b.WriteString("• <code>/rate period=current_month limit=5</code>\n\n")

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

	b.WriteString("\n📬 <b>Daily report</b>\n")
	b.WriteString("• <code>/report</code> — show current status\n")
	b.WriteString("• <code>/report activate=true</code> — enable daily report\n")
	b.WriteString("• <code>/report activate=false</code> — disable daily report\n")
	b.WriteString("• <code>/report hour=8 minute=30</code> — set report time (keeps current timezone)\n")
	b.WriteString("• <code>/report hour=8 minute=0 timezone=Europe/Paris</code> — set time and timezone\n\n")

	b.WriteString("\n⛓️ <b>Multi-chain</b>\n")
	b.WriteString("• <code>/chain</code> — show current chain and available chains\n")
	b.WriteString("• <code>/setchain chain=&lt;id&gt;</code> — switch active chain for this chat\n\n")

	b.WriteString("🎛 <code>/cmd</code> — Interactive command menu (guided step-by-step)\n\n")

	b.WriteString("ℹ️ Parameters must be written as <code>key=value</code> (e.g. <code>period=current_week</code>).\n")

	return b.String()
}

// formatChainHealthMessage formats a ChainHealthSnapshot as an HTML Telegram message.
func formatChainHealthMessage(chainID string, snap ChainHealthSnapshot) string {
	if snap.IsDisabled {
		if ChainDisabledFormatter != nil {
			return ChainDisabledFormatter(chainID, snap)
		}
		return fmt.Sprintf("⚫ <b>[%s] Chain status — MONITORING OFF</b>", html.EscapeString(chainID))
	}
	if snap.IsStuck {
		if ChainStuckFormatter != nil {
			return ChainStuckFormatter(chainID, snap)
		}
	}

	var b strings.Builder

	// Determine chain health indicator based on consensus round.
	roundLabel := consensusRoundLabel(snap.ConsensusRound)
	headerEmoji := chainHealthEmoji(snap.ConsensusRound, snap.RPCReachable)

	b.WriteString(fmt.Sprintf("%s <b>[%s] Chain status</b>", headerEmoji, html.EscapeString(chainID)))

	if !snap.RPCReachable {
		b.WriteString("\n⚠️ RPC unreachable — showing last known DB data only\n")
	} else {
		age := formatDuration(time.Since(snap.LatestBlockTime))
		b.WriteString(fmt.Sprintf(" — block <code>#%d</code> (%s ago)\n", snap.LatestBlockHeight, age))
		b.WriteString(fmt.Sprintf("Consensus: round %d — %s\n", snap.ConsensusRound, roundLabel))
	}

	if snap.ValidatorLiveness != nil {
		b.WriteString(fmt.Sprintf("\nValidator status at last block <code>#%d</code>:\n", snap.LatestBlockHeight))
		b.WriteString(formatValidatorLivenessHTML(snap.ValidatorLiveness, snap.Monikers))
	} else {
		b.WriteString("\nParticipation (last 50 blocks — RPC unreachable):\n")
		b.WriteString(formatValidatorRates(snap.ValidatorRates))
	}

	if AlertsFormatter != nil {
		b.WriteString(AlertsFormatter(snap.AlertsLast24h))
	}

	return b.String()
}

// consensusRoundLabel returns a human-readable label for a consensus round number.
func consensusRoundLabel(round int) string {
	switch {
	case round <= 2:
		return "Normal"
	case round <= 10:
		return "Slightly slow"
	case round <= 50:
		return "Degraded"
	default:
		return "STUCK"
	}
}

// chainHealthEmoji returns the status emoji based on consensus round and RPC reachability.
func chainHealthEmoji(round int, rpcReachable bool) string {
	if !rpcReachable {
		return "⚠️"
	}
	switch {
	case round <= 2:
		return "🟢"
	case round <= 10:
		return "🟡"
	case round <= 50:
		return "🟠"
	default:
		return "🚨"
	}
}

// formatDuration formats a duration as "Xd Yh Zm" omitting zero components.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	return strings.Join(parts, " ")
}

// formatValidatorRates formats the per-validator rate map sorted by rate descending.
func formatValidatorRates(rates map[string]ValidatorRate) string {
	if len(rates) == 0 {
		return "  No data.\n"
	}

	type entry struct {
		addr    string
		moniker string
		rate    float64
	}
	entries := make([]entry, 0, len(rates))
	for addr, vr := range rates {
		entries = append(entries, entry{addr: addr, moniker: vr.Moniker, rate: vr.Rate})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rate > entries[j].rate })

	var b strings.Builder
	for _, e := range entries {
		emoji := "🟢"
		switch {
		case e.rate < 50.0:
			emoji = "🔴"
		case e.rate < 70.0:
			emoji = "🟠"
		case e.rate < 95.0:
			emoji = "🟡"
		}
		moniker := e.moniker
		if moniker == "" {
			moniker = e.addr
		}
		addrShort := e.addr
		if len(addrShort) > 12 {
			addrShort = addrShort[:10] + "..."
		}
		b.WriteString(fmt.Sprintf("  %s <b>%-12s</b> (<code>%s</code>) %.0f%%\n",
			emoji, html.EscapeString(moniker), html.EscapeString(addrShort), e.rate))
	}
	return b.String()
}

// formatValidatorLivenessHTML formats the per-validator liveness from the last
// committed block's precommits as an HTML Telegram message fragment.
// monikers maps addr -> display name; it may be nil or empty.
// Signed validators are listed first, then missing ones, each group sorted by display name.
func formatValidatorLivenessHTML(liveness map[string]bool, monikers map[string]string) string {
	if len(liveness) == 0 {
		return "  No data.\n"
	}

	type entry struct {
		addr   string
		name   string // display name (moniker or truncated addr)
		signed bool
	}
	entries := make([]entry, 0, len(liveness))
	for addr, signed := range liveness {
		name := monikers[addr]
		if name == "" {
			if len(addr) > 10 {
				name = addr[:10] + "..."
			} else {
				name = addr
			}
		}
		entries = append(entries, entry{addr: addr, name: name, signed: signed})
	}
	// Sort: signed validators first, then alphabetically by display name.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].signed != entries[j].signed {
			return entries[i].signed
		}
		return entries[i].name < entries[j].name
	})

	var b strings.Builder
	for _, e := range entries {
		addrShort := e.addr
		if len(addrShort) > 10 {
			addrShort = addrShort[:10] + "..."
		}
		if e.signed {
			b.WriteString(fmt.Sprintf("  🟢 <b>%-12s</b> (<code>%s</code>)\n",
				html.EscapeString(e.name), html.EscapeString(addrShort)))
		} else {
			b.WriteString(fmt.Sprintf("  🔴 <b>%-12s</b> (<code>%s</code>)  MISSING\n",
				html.EscapeString(e.name), html.EscapeString(addrShort)))
		}
	}
	return b.String()
}
