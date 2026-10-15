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
// computeBFTMargin expects. A member with no row in the window has a sign
// rate of 0: it did not sign anything the monitor saw.
func bftEntries(w database.BFTWindow) []database.ValidatorReportEntry {
	entries := make([]database.ValidatorReportEntry, 0, len(w.Entries))
	for _, e := range w.Entries {
		var rate float64
		if e.Blocks > 0 {
			rate = float64(e.Signed) / float64(e.Blocks) * 100
		}
		entries = append(entries, database.ValidatorReportEntry{
			Addr: e.Addr, Moniker: e.Moniker, VotingPower: e.VotingPower, SignRate: rate,
		})
	}
	return entries
}

// evaluateBFT grades the chain's BFT margin over the window. Two criteria,
// the worse one wins:
//
//   - margin: the online voting power above 2/3, as a share of the total.
//     With every validator online it is always 33.3%, so it only ever drops
//     because validators went offline.
//   - tolerable losses: how many more validators (largest first) can go
//     offline before the quorum is lost. It only counts when offline
//     validators lowered it below its all-online value: a small or
//     concentrated valset that tolerates no loss even when complete is a
//     structural fact, not an incident, and must not alert forever.
func evaluateBFT(w database.BFTWindow, t Thresholds) bftEval {
	entries := bftEntries(w)
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
	if live.TolerableLosses < ev.Structural.TolerableLosses {
		switch {
		case live.TolerableLosses == 0:
			ev.Severity = bftCritical
		case live.TolerableLosses <= t.BFTAlertWarningLosses && ev.Severity < bftWarning:
			ev.Severity = bftWarning
		}
	}
	return ev
}

// decideBFT turns a verdict into the level to dispatch ("" for none), given
// the chain's latest BFT alert_logs row and the count of consecutive healthy
// checks seen so far. It returns the new count.
//
//   - no open alert: send the verdict's level, if any;
//   - open alert, different level (escalation or de-escalation): send it now;
//   - open alert, same level: resend only once the level's resend window
//     (alert_warning_resend_hours / alert_critical_resend_hours) elapsed;
//   - open alert, healthy verdict: resolve after bftResolveChecks in a row.
func decideBFT(sev bftSeverity, state database.BFTAlertState, okStreak int, now time.Time, t Thresholds) (string, int) {
	active := state.Level == bftLevelWarning || state.Level == bftLevelCritical
	if sev == bftOK {
		if !active {
			return "", 0
		}
		okStreak++
		if okStreak >= bftResolveChecks {
			return bftLevelResolved, 0
		}
		return "", okStreak
	}

	level, resendLevel := bftLevelWarning, "WARNING"
	if sev == bftCritical {
		level, resendLevel = bftLevelCritical, "CRITICAL"
	}
	if !active || state.Level != level {
		return level, 0
	}
	if now.Sub(state.SentAt) >= time.Duration(t.ResendHoursForLevel(resendLevel))*time.Hour {
		return level, 0
	}
	return "", 0
}
