package gnovalidator

import (
	"sort"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

// alert_logs levels and addr of the BFT margin alert. The levels are
// dedicated (like LATENCY), so every query filtered on WARNING/CRITICAL/
// RESOLVED — the score, the missed-block dedup, SendResolveAlerts, the
// active-alert count — ignores these rows without further change.
const (
	bftAlertAddr     = "bft"
	bftLevelWarning  = "BFT_WARNING"
	bftLevelCritical = "BFT_CRITICAL"
	bftLevelResolved = "BFT_RESOLVED"
)

// bftResolveChecks is the hysteresis: an open BFT alert only resolves after
// this many consecutive checks found the margin healthy, so a validator
// signing around the online threshold does not flip the alert every cycle.
const bftResolveChecks = 2

// bftStaleIntervals: when the window's latest block is older than this many
// check intervals (see bftStaleAfter), the chain is stalled (or collection
// is): the frozen
// window says nothing about who is online now, so no verdict is drawn from
// it — in particular no false resolve during a halt. The halt itself is the
// stagnation alert's job.
const bftStaleIntervals = 2

type bftSeverity int

const (
	bftOK bftSeverity = iota
	bftWarning
	bftCritical
)

// bftOffline is one voting-power holder counted offline in the window.
type bftOffline struct {
	Addr        string
	Moniker     string
	VotingPower int64
}

// bftEval is the verdict of one BFT margin check plus the numbers behind it,
// which the alert message quotes.
type bftEval struct {
	// Evaluated is false when no voting-power snapshot exists: nothing is
	// known about the quorum, so nothing is decided.
	Evaluated bool
	Severity  bftSeverity
	// Live is the margin over the window; Structural is the same valset with
	// every validator online, i.e. the best the chain can do.
	Live       BFTMargin
	Structural BFTMargin
	// Offline is sorted by voting power, largest first.
	Offline   []bftOffline
	MinHeight int64
	MaxHeight int64
}

// bftEntries converts the window rows into the report entries
// computeBFTMargin expects. Every voting-power holder stays in the total,
// as it does for consensus: a validator that just joined and has no row
// yet counts as offline until it is seen signing. A validator that missed
// its last missedTip blocks in a row (critical_threshold, the missed-block
// CRITICAL level: a short hiccup does not count) is offline too, sign rate
// forced to 0, however well it signed earlier in the window: it stopped,
// and waiting for its window-wide rate to fall under 50% would delay the
// alert by half a window.
func bftEntries(w database.BFTWindow, missedTip int) []database.ValidatorReportEntry {
	entries := make([]database.ValidatorReportEntry, 0, len(w.Entries))
	for _, e := range w.Entries {
		var rate float64
		stopped := missedTip > 0 && e.TrailingMissed >= int64(missedTip)
		if e.Blocks > 0 && !stopped {
			rate = float64(e.Signed) / float64(e.Blocks) * 100
		}
		entries = append(entries, database.ValidatorReportEntry{
			Addr: e.Addr, Moniker: e.Moniker, VotingPower: e.VotingPower, SignRate: rate,
		})
	}
	return entries
}

// bftSinglePointsOfFailure counts the online validators whose loss alone
// would take the online voting power to 2/3 of the total or below.
func bftSinglePointsOfFailure(entries []database.ValidatorReportEntry) int {
	var total, online int64
	for _, e := range entries {
		if e.VotingPower <= 0 {
			continue
		}
		total += e.VotingPower
		if e.SignRate >= bftOnlineSignRate {
			online += e.VotingPower
		}
	}
	n := 0
	for _, e := range entries {
		if e.VotingPower > 0 && e.SignRate >= bftOnlineSignRate && 3*(online-e.VotingPower) <= 2*total {
			n++
		}
	}
	return n
}

// bftStaleAfter is how old the window's latest block may be before the
// window counts as frozen: two check intervals, and never under 10 minutes,
// so a short interval does not turn ordinary clock skew into silence.
func bftStaleAfter(t Thresholds) time.Duration {
	d := bftStaleIntervals * t.BFTAlertCheckInterval()
	if d < 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// evaluateBFT grades the chain's BFT margin over the window. Two criteria,
// the worse one wins:
//
//   - margin: the online voting power above 2/3, as a share of the total.
//     With every validator online it is always 33.3%, so it only ever drops
//     because validators went offline.
//   - losses: CRITICAL when no further loss (largest first) is survivable
//     and offline validators made it so — the complete set survived at least
//     one loss, or more validators are now each a single point of failure
//     than when everyone is online; WARNING when at most
//     bft_alert_warning_losses losses are left and offline validators
//     lowered that count. A small or concentrated valset that is fragile
//     even when complete is a structural fact, not an incident, and does not
//     alert on that alone.
//
// Nothing is evaluated without a voting-power snapshot, when less than 80%
// of the window's possible heights were collected, or when the latest block
// is stale (see bftStaleAfter).
func evaluateBFT(w database.BFTWindow, t Thresholds, now time.Time) bftEval {
	span := w.MaxHeight - w.MinHeight + 1
	if w.Heights == 0 || 5*w.Heights < 4*span {
		return bftEval{}
	}
	if now.Sub(w.LastBlockTime) > bftStaleAfter(t) {
		return bftEval{}
	}
	entries := bftEntries(w, t.CriticalThreshold)
	live := computeBFTMargin(entries)
	if !live.Available {
		return bftEval{}
	}

	all := make([]database.ValidatorReportEntry, len(entries))
	copy(all, entries)
	for i := range all {
		all[i].SignRate = 100
	}

	ev := bftEval{
		Evaluated:  true,
		Live:       live,
		Structural: computeBFTMargin(all),
		MinHeight:  w.MinHeight,
		MaxHeight:  w.MaxHeight,
	}
	for _, e := range entries {
		if e.VotingPower > 0 && e.SignRate < bftOnlineSignRate {
			ev.Offline = append(ev.Offline, bftOffline{Addr: e.Addr, Moniker: e.Moniker, VotingPower: e.VotingPower})
		}
	}
	sort.SliceStable(ev.Offline, func(i, j int) bool { return ev.Offline[i].VotingPower > ev.Offline[j].VotingPower })

	margin := live.MarginPercent()
	switch {
	case !live.AboveQuorum() || margin < t.BFTAlertCriticalMarginPct:
		ev.Severity = bftCritical
	case margin < t.BFTAlertWarningMarginPct:
		ev.Severity = bftWarning
	}
	switch {
	case live.TolerableLosses == 0 && (ev.Structural.TolerableLosses > 0 ||
		bftSinglePointsOfFailure(entries) > bftSinglePointsOfFailure(all)):
		ev.Severity = bftCritical
	case live.TolerableLosses <= t.BFTAlertWarningLosses && live.TolerableLosses < ev.Structural.TolerableLosses &&
		ev.Severity < bftWarning:
		ev.Severity = bftWarning
	}
	return ev
}

// bftStreak is the per-chain hysteresis state decideBFT carries between
// checks. Height is the window tip of the last check that counted: a check
// only counts towards a streak when the chain moved since, so two checks of
// the same frozen height prove nothing.
type bftStreak struct {
	OK     int
	Bad    int
	Height int64
}

// decideBFT turns a verdict into the level to dispatch ("" for none), given
// the window tip, the chain's latest BFT alert_logs row and the streak so
// far. It returns the new streak.
//
//   - no open alert: send the verdict's level, if any — but right after a
//     resolve (inside the warning resend window) only once
//     bftResolveChecks checks in a row agree, so a margin hovering around a
//     threshold does not flip between alert and resolve;
//   - open WARNING, CRITICAL verdict (escalation): send it now;
//   - open CRITICAL, WARNING verdict (de-escalation) or same level: send only
//     once the open level's resend window (alert_warning_resend_hours /
//     alert_critical_resend_hours) elapsed;
//   - open alert, healthy verdict: resolve after bftResolveChecks in a row.
func decideBFT(sev bftSeverity, height int64, state database.BFTAlertState, st bftStreak, now time.Time, t Thresholds) (string, bftStreak) {
	active := state.Level == bftLevelWarning || state.Level == bftLevelCritical
	moved := height > st.Height
	reset := bftStreak{Height: height}

	if sev == bftOK {
		if !active {
			return "", reset
		}
		if moved {
			st = bftStreak{OK: st.OK + 1, Height: height}
		}
		if st.OK >= bftResolveChecks {
			return bftLevelResolved, reset
		}
		return "", st
	}

	level := bftLevelWarning
	if sev == bftCritical {
		level = bftLevelCritical
	}
	if !active {
		justResolved := state.Level == bftLevelResolved &&
			now.Sub(state.SentAt) < time.Duration(t.ResendHoursForLevel("WARNING"))*time.Hour
		if !justResolved {
			return level, reset
		}
		if moved {
			st = bftStreak{Bad: st.Bad + 1, Height: height}
		}
		if st.Bad >= bftResolveChecks {
			return level, reset
		}
		return "", bftStreak{Bad: st.Bad, Height: st.Height}
	}
	if level == bftLevelCritical && state.Level == bftLevelWarning {
		return level, reset
	}
	openLevel := "WARNING"
	if state.Level == bftLevelCritical {
		openLevel = "CRITICAL"
	}
	if now.Sub(state.SentAt) >= time.Duration(t.ResendHoursForLevel(openLevel))*time.Hour {
		return level, reset
	}
	return "", reset
}
