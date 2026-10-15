package gnovalidator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/jobclock"
	"gorm.io/gorm"
)

// bftMaxOfflineListed caps the offline list in a message, so a mass outage
// on a large valset does not produce an unreadable (or oversized) alert.
const bftMaxOfflineListed = 10

func pluralValidators(n int) string {
	if n == 1 {
		return "validator"
	}
	return "validators"
}

func bftOnlineField(m BFTMargin) internal.AlertField {
	return internal.AlertField{
		Name: "online voting power",
		Value: fmt.Sprintf("%d/%d (%.1f%%), %d/%d validators",
			m.OnlineVP, m.TotalVP, float64(m.OnlineVP)/float64(m.TotalVP)*100, m.OnlineCount, m.TotalCount),
	}
}

func bftMarginField(m BFTMargin) internal.AlertField {
	value := "at or below the 2/3 quorum"
	if m.AboveQuorum() {
		value = fmt.Sprintf("%.1f%% of total voting power", m.MarginPercent())
	}
	return internal.AlertField{Name: "margin above 2/3", Value: value}
}

func bftLossesField(ev bftEval) internal.AlertField {
	return internal.AlertField{
		Name: "tolerable losses",
		Value: fmt.Sprintf("can lose %d more %s, largest first (%d with every validator online)",
			ev.Live.TolerableLosses, pluralValidators(ev.Live.TolerableLosses), ev.Structural.TolerableLosses),
	}
}

func bftOfflineText(offline []bftOffline) string {
	parts := make([]string, 0, bftMaxOfflineListed+1)
	for i, o := range offline {
		if i == bftMaxOfflineListed {
			parts = append(parts, fmt.Sprintf("and %d more", len(offline)-bftMaxOfflineListed))
			break
		}
		name := o.Moniker
		if name == "" {
			name = o.Addr
		}
		parts = append(parts, fmt.Sprintf("%s (%d VP)", name, o.VotingPower))
	}
	return strings.Join(parts, ", ")
}

// buildBFTAlertData renders a BFT_WARNING or BFT_CRITICAL alert. No contact
// mentions: the margin belongs to the whole chain, not to one validator.
func buildBFTAlertData(chainID, level string, ev bftEval, windowBlocks int) internal.AlertData {
	d := internal.AlertData{ChainID: chainID}
	if level == bftLevelCritical {
		d.Level, d.Emoji, d.Title = internal.AlertCritical, "🔴", "BFT CRITICAL — quorum at risk"
	} else {
		d.Level, d.Emoji, d.Title = internal.AlertWarning, "⚠️", "BFT WARNING — thin quorum margin"
	}
	d.Fields = []internal.AlertField{bftOnlineField(ev.Live), bftMarginField(ev.Live), bftLossesField(ev)}
	if len(ev.Offline) > 0 {
		d.Fields = append(d.Fields, internal.AlertField{
			Name:  fmt.Sprintf("offline (signed < %.0f%% of last %d blocks)", bftOnlineSignRate, windowBlocks),
			Value: bftOfflineText(ev.Offline),
		})
	}
	d.Fields = append(d.Fields, internal.AlertField{Name: "blocks", Value: fmt.Sprintf("%d -> %d", ev.MinHeight, ev.MaxHeight)})
	return d
}

// buildBFTResolvedData renders the paired BFT_RESOLVED notice.
func buildBFTResolvedData(chainID string, ev bftEval) internal.AlertData {
	return internal.AlertData{
		ChainID: chainID,
		Level:   internal.AlertResolved,
		Emoji:   "✅",
		Title:   "BFT RESOLVED — quorum margin restored",
		Fields:  []internal.AlertField{bftOnlineField(ev.Live), bftMarginField(ev.Live), bftLossesField(ev)},
	}
}

// bftJob is one pending BFT notification.
type bftJob struct {
	db           *gorm.DB
	chainID      string
	level        string // bftLevelWarning, bftLevelCritical or bftLevelResolved
	eval         bftEval
	windowBlocks int
}

// bftDispatch performs the notification plus the alert_logs row. Swappable
// via setBFTDispatch so tests observe dispatches without network I/O
// (same pattern as latencyDispatch).
var (
	bftDispatchMu sync.RWMutex
	bftDispatch   = defaultBFTDispatch
)

func getBFTDispatch() func(bftJob) {
	bftDispatchMu.RLock()
	defer bftDispatchMu.RUnlock()
	return bftDispatch
}

func setBFTDispatch(fn func(bftJob)) {
	bftDispatchMu.Lock()
	defer bftDispatchMu.Unlock()
	bftDispatch = fn
}

func defaultBFTDispatch(job bftJob) {
	var data internal.AlertData
	if job.level == bftLevelResolved {
		data = buildBFTResolvedData(job.chainID, job.eval)
	} else {
		data = buildBFTAlertData(job.chainID, job.level, job.eval, job.windowBlocks)
	}
	if err := internal.SendInfoValidator(job.chainID, data, job.db); err != nil {
		log.Printf("[bft][%s] SendInfoValidator error: %v", job.chainID, err)
	}

	m := job.eval.Live
	msg := fmt.Sprintf("online_vp=%d/%d margin=%.1f%% tolerable=%d/%d offline=%d",
		m.OnlineVP, m.TotalVP, m.MarginPercent(), m.TolerableLosses, job.eval.Structural.TolerableLosses, len(job.eval.Offline))
	// skipped=false, like the RPC rows: this is not a validator incident, and
	// SendResolveAlerts only considers skipped=true WARNING/CRITICAL rows.
	if err := database.InsertAlertlog(job.db, job.chainID, bftAlertAddr, bftAlertAddr, job.level,
		job.eval.MinHeight, job.eval.MaxHeight, false, time.Now(), msg); err != nil {
		log.Printf("[bft][%s] InsertAlertlog error: %v", job.chainID, err)
	}
}

// bftOKStreaks counts, per chain, the consecutive healthy checks seen while
// a BFT alert is open (see decideBFT). In memory only: after a restart an
// open alert simply needs bftResolveChecks fresh healthy checks to resolve.
var (
	bftOKStreaksMu sync.Mutex
	bftOKStreaks   = map[string]int{}
)

// runBFTAlertCycle performs one evaluation pass for a chain.
func runBFTAlertCycle(db *gorm.DB, chainID string, t Thresholds) {
	if !t.BFTAlertEnabled {
		ChainBFTMarginPercent.DeleteLabelValues(chainID)
		return
	}

	window, err := database.GetBFTWindow(db, chainID, t.BFTAlertWindowBlocks)
	if err != nil {
		log.Printf("[bft][%s] GetBFTWindow error: %v", chainID, err)
		return
	}
	ev := evaluateBFT(window, t)
	if !ev.Evaluated {
		// No voting-power snapshot (or no block yet): nothing is known about
		// the quorum. Absence of data is not a recovery either, so the
		// healthy streak restarts.
		ChainBFTMarginPercent.DeleteLabelValues(chainID)
		bftOKStreaksMu.Lock()
		bftOKStreaks[chainID] = 0
		bftOKStreaksMu.Unlock()
		return
	}
	ChainBFTMarginPercent.WithLabelValues(chainID).Set(ev.Live.MarginPercent())

	state, err := database.GetBFTAlertState(db, chainID)
	if err != nil {
		log.Printf("[bft][%s] GetBFTAlertState error: %v", chainID, err)
		return
	}

	bftOKStreaksMu.Lock()
	level, streak := decideBFT(ev.Severity, state, bftOKStreaks[chainID], time.Now(), t)
	bftOKStreaks[chainID] = streak
	bftOKStreaksMu.Unlock()

	if level == "" {
		return
	}
	getBFTDispatch()(bftJob{db: db, chainID: chainID, level: level, eval: ev, windowBlocks: t.BFTAlertWindowBlocks})
}

// WatchBFTAlerts evaluates the chain's BFT margin every
// bft_alert_check_minutes and warns before the online voting power can lose
// the 2/3 quorum. A chain that already halted produces no new block: the
// window then stays frozen on the last blocks and the stagnation alert is
// what reports the halt.
func WatchBFTAlerts(ctx context.Context, db *gorm.DB, chainID string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[bft][%s] WatchBFTAlerts panic: %v", chainID, r)
			}
		}()
		log.Printf("[bft][%s] checks every %v at +%v", chainID, GetThresholds().BFTAlertCheckInterval(),
			jobclock.BFTOffset(chainID, internal.EnabledChainsSnapshot()))
		for {
			t := GetThresholds()
			if isChainSynced(chainID) {
				runBFTAlertCycle(db, chainID, t)
			}
			select {
			case <-ctx.Done():
				log.Printf("[bft][%s] WatchBFTAlerts stopped", chainID)
				ChainBFTMarginPercent.DeleteLabelValues(chainID)
				return
			case <-time.After(jobclock.UntilNext(time.Now(), t.BFTAlertCheckInterval(),
				jobclock.BFTOffset(chainID, internal.EnabledChainsSnapshot()))):
			}
		}
	}()
}
