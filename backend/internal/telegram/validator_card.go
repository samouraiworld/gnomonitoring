package telegram

import (
	"fmt"
	"html"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

const (
	// validatorCardLatencyTTL caches the chain-wide precommit-lag query, a
	// 7-day scan of daily_participations, between two /validator calls.
	validatorCardLatencyTTL = 5 * time.Minute
	// validatorCardReportTTL caches the chain's last-24h report and last
	// alert times, so repeated /validator calls do not rebuild them.
	validatorCardReportTTL = time.Minute
	// validatorCardMaxQueryLen bounds the query echoed back in a reply.
	validatorCardMaxQueryLen = 64
	// validatorCardMaxMatches is how many candidates an ambiguous query lists.
	validatorCardMaxMatches = 10
	// validatorCardSearchCmd is the SearchState command of the /cmd "Validator
	// card" button, which asks for the moniker as free text.
	validatorCardSearchCmd = "card"
)

func validatorCardUsage() string {
	return "Usage: <code>/validator &lt;moniker or address&gt;</code>\n" +
		"Example: <code>/validator samourai</code>"
}

// matchValidators returns the entries query designates. An exact address or
// moniker match (case-insensitive) wins; otherwise every entry whose moniker
// or address contains query. The result is sorted by moniker, then address.
func matchValidators(entries []database.ValidatorReportEntry, query string) []database.ValidatorReportEntry {
	q := strings.TrimSpace(query)
	var exact, partial []database.ValidatorReportEntry
	for _, e := range entries {
		switch {
		case strings.EqualFold(e.Addr, q) || strings.EqualFold(e.Moniker, q):
			exact = append(exact, e)
		case matchesFilter(q, e.Moniker, e.Addr):
			partial = append(partial, e)
		}
	}
	out := partial
	if len(exact) > 0 {
		out = exact
	}
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Moniker, out[j].Moniker) {
			return strings.ToLower(out[i].Moniker) < strings.ToLower(out[j].Moniker)
		}
		return out[i].Addr < out[j].Addr
	})
	return out
}

// chainPrecommitLatency returns the chain's precommit-lag percentiles keyed
// by address, cached for validatorCardLatencyTTL.
func chainPrecommitLatency(db *gorm.DB, chainID string) (map[string]database.PrecommitLatencyMultiWindow, error) {
	cacheKey := "validator_card_latency:" + chainID
	if cached, ok := getCached(cacheKey); ok {
		if byAddr, ok := cached.(map[string]database.PrecommitLatencyMultiWindow); ok {
			return byAddr, nil
		}
	}
	rows, err := database.GetPrecommitLatencyMultiWindow(db, chainID)
	if err != nil {
		return nil, err
	}
	byAddr := make(map[string]database.PrecommitLatencyMultiWindow, len(rows))
	for _, r := range rows {
		byAddr[r.Addr] = r
	}
	setCached(cacheKey, byAddr, validatorCardLatencyTTL)
	return byAddr, nil
}

// validatorCardReport is the cached chain-wide part of a /validator answer.
type validatorCardReport struct {
	entries    []database.ValidatorReportEntry
	lastAlerts map[string]time.Time
	alertsErr  error
}

// chainValidatorCardReport returns chainID's last-24h validator report (the
// active set, as /status uses) and last alert times, cached for
// validatorCardReportTTL.
func chainValidatorCardReport(db *gorm.DB, chainID string) (validatorCardReport, error) {
	cacheKey := "validator_card_report:" + chainID
	if cached, ok := getCached(cacheKey); ok {
		if report, ok := cached.(validatorCardReport); ok {
			return report, nil
		}
	}
	ctx, err := database.LoadValidatorReportContext(db, chainID)
	if err != nil {
		return validatorCardReport{}, fmt.Errorf("load report context: %w", err)
	}
	entries, err := database.BuildChainValidatorReport(db, ctx, chainID, "last_24h", "")
	if err != nil {
		return validatorCardReport{}, fmt.Errorf("build validator report: %w", err)
	}
	report := validatorCardReport{entries: entries}
	report.lastAlerts, report.alertsErr = database.GetLastAlertTimes(db, chainID)
	if report.alertsErr == nil {
		setCached(cacheKey, report, validatorCardReportTTL)
	}
	return report, nil
}

// formatValidatorCard answers /validator <query> on chainID: the card of the
// one active-valset validator query designates, the candidates when several
// match, or a "no match" message.
func formatValidatorCard(db *gorm.DB, chainID, query string, now time.Time) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return validatorCardUsage(), nil
	}

	report, err := chainValidatorCardReport(db, chainID)
	if err != nil {
		return "", err
	}

	esc := html.EscapeString
	matches := matchValidators(report.entries, query)
	shown := truncateRunes(query, validatorCardMaxQueryLen)
	switch {
	case len(matches) == 0:
		return fmt.Sprintf("🔍 No validator of the active set on <code>%s</code> matches <code>%s</code>.\n%s",
			esc(chainID), esc(shown), validatorCardUsage()), nil
	case len(matches) > 1:
		var b strings.Builder
		fmt.Fprintf(&b, "🔍 %d validators on <code>%s</code> match <code>%s</code>:\n",
			len(matches), esc(chainID), esc(shown))
		for i, m := range matches {
			if i == validatorCardMaxMatches {
				fmt.Fprintf(&b, "… and %d more\n", len(matches)-i)
				break
			}
			fmt.Fprintf(&b, "• <b>%s</b> <code>%s</code>\n", esc(m.DisplayName()), esc(m.Addr))
		}
		b.WriteString("Send <code>/validator</code> with the exact moniker or address.")
		return b.String(), nil
	}

	e := matches[0]
	var b strings.Builder
	fmt.Fprintf(&b, "🪪 <b>%s</b> [%s]\n<code>%s</code>\n\n", esc(e.DisplayName()), esc(chainID), esc(e.Addr))
	fmt.Fprintf(&b, "%s Tier: <b>%s</b> | Score: <b>%d</b> (last 24h)\n", tierEmoji(e.Tier), e.Tier, e.Score)
	fmt.Fprintf(&b, "✍️ Sign rate: %.2f%%\n", e.SignRate)
	if e.ProposerReliability != nil {
		fmt.Fprintf(&b, "🎯 Proposer reliability: %.1f%%\n", *e.ProposerReliability)
	} else {
		b.WriteString("🎯 Proposer reliability: n/a (too few expected blocks)\n")
	}
	if pct, ok := e.VotingPowerPercent(); ok {
		fmt.Fprintf(&b, "⚖️ Voting power: %d (%.1f%%)\n", e.VotingPower, pct)
	} else {
		b.WriteString("⚖️ Voting power: n/a\n")
	}

	latency, err := chainPrecommitLatency(db, chainID)
	if err != nil {
		log.Printf("[telegram] /validator latency chain=%s: %v", chainID, err)
	}
	lat, haveLat := latency[e.Addr]
	fmt.Fprintf(&b, "⏱️ Precommit lag 24h: %s\n", formatLagPair(haveLat, lat.LagP50_24h, lat.LagP90_24h))
	fmt.Fprintf(&b, "⏱️ Precommit lag 7d: %s\n", formatLagPair(haveLat, lat.LagP50_7d, lat.LagP90_7d))

	switch last, ok := report.lastAlerts[e.Addr]; {
	case report.alertsErr != nil:
		log.Printf("[telegram] /validator last alert chain=%s: %v", chainID, report.alertsErr)
		b.WriteString("🚨 Last alert: n/a\n")
	case ok:
		fmt.Fprintf(&b, "🚨 Last alert: %s UTC (%s ago)\n",
			last.UTC().Format("2006-01-02 15:04"), formatDuration(now.Sub(last)))
	default:
		b.WriteString("🚨 Last alert: none\n")
	}
	return b.String(), nil
}

// formatLagPair renders "p50 X ms · p90 Y ms", or "n/a" without samples.
func formatLagPair(have bool, p50, p90 *float64) string {
	if !have || p50 == nil || p90 == nil {
		return "n/a"
	}
	return fmt.Sprintf("p50 %.0f ms · p90 %.0f ms", *p50, *p90)
}

// sendValidatorCard replies to /validator in chatID on chainID.
func sendValidatorCard(token string, db *gorm.DB, chatID int64, chainID, query string) {
	msg, err := formatValidatorCard(db, chainID, query, time.Now())
	if err != nil {
		log.Printf("[telegram] /validator chat=%d chain=%s: %v", chatID, chainID, err)
		msg = "❌ An error occurred. Please try again."
	}
	if err := SendMessageTelegram(token, chatID, msg); err != nil {
		log.Printf("[telegram] /validator send chat=%d: %v", chatID, err)
	}
}
