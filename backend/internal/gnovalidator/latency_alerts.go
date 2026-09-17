package gnovalidator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

// latencyNote is the explanatory line every latency alert carries. It names
// the three consensus settings behind every case seen so far, and states
// plainly that no block was missed so the alert is not mistaken for downtime.
const latencyNote = "No blocks missed. Check flush_throttle_timeout (10ms), " +
	"peer_gossip_sleep_duration (10ms), timeout_commit (3s) and NTP sync."

func latencyLagField(ev latencyEval, windowMinutes int) internal.AlertField {
	return internal.AlertField{
		Name: fmt.Sprintf("precommit lag (%dm)", windowMinutes),
		Value: fmt.Sprintf("p50 %.0f ms / p90 %.0f ms (peers median p50: %.0f ms)",
			ev.Stat.P50, ev.Stat.P90, ev.PeerMedianP50),
	}
}

// buildLatencyAlertData renders one LATENCY alert. No mentions: a lagging
// validator misses no block, so it never pings a contact.
func buildLatencyAlertData(chainID string, ev latencyEval, windowMinutes int) internal.AlertData {
	fields := []internal.AlertField{
		{Name: "validator", Value: fmt.Sprintf("%s (%s)", ev.Stat.Moniker, ev.Stat.Addr)},
		latencyLagField(ev, windowMinutes),
	}
	if ev.Stat.LateRatio != nil {
		fields = append(fields, internal.AlertField{
			Name:  "late for quorum",
			Value: fmt.Sprintf("%.0f%% of %d signed blocks", *ev.Stat.LateRatio*100, ev.Stat.Samples),
		})
	}
	fields = append(fields,
		internal.AlertField{Name: "blocks", Value: fmt.Sprintf("%d -> %d", ev.Stat.MinHeight, ev.Stat.MaxHeight)},
		internal.AlertField{Name: "note", Value: latencyNote},
	)

	return internal.AlertData{
		ChainID: chainID,
		Level:   internal.AlertLatency,
		Emoji:   "🐢",
		Title:   "LATENCY — signing late for quorum",
		Fields:  fields,
	}
}

// buildLatencyResolvedData renders the paired LATENCY_RESOLVED notice.
func buildLatencyResolvedData(chainID string, ev latencyEval, windowMinutes int) internal.AlertData {
	return internal.AlertData{
		ChainID: chainID,
		Level:   internal.AlertResolved,
		Emoji:   "✅",
		Title:   "LATENCY RESOLVED",
		Fields: []internal.AlertField{
			{Name: "validator", Value: fmt.Sprintf("%s (%s)", ev.Stat.Moniker, ev.Stat.Addr)},
			latencyLagField(ev, windowMinutes),
		},
	}
}

// latencyJob is one pending latency notification: the level to send, the
// evaluation behind it, and everything the dispatch needs.
type latencyJob struct {
	db            *gorm.DB
	chainID       string
	level         string // "LATENCY" or "LATENCY_RESOLVED"
	eval          latencyEval
	windowMinutes int
}

// latencyDispatch performs the actual notification plus the alert_logs row.
// Swappable via setLatencyDispatch so tests observe dispatches without doing
// network I/O; guarded by a mutex so swapping it in a test while a loop
// goroutine reads it stays race-free (same pattern as rpcDispatch).
var (
	latencyDispatchMu sync.RWMutex
	latencyDispatch   = defaultLatencyDispatch
)

func getLatencyDispatch() func(latencyJob) {
	latencyDispatchMu.RLock()
	defer latencyDispatchMu.RUnlock()
	return latencyDispatch
}

func setLatencyDispatch(fn func(latencyJob)) {
	latencyDispatchMu.Lock()
	defer latencyDispatchMu.Unlock()
	latencyDispatch = fn
}

func defaultLatencyDispatch(job latencyJob) {
	var data internal.AlertData
	if job.level == "LATENCY" {
		data = buildLatencyAlertData(job.chainID, job.eval, job.windowMinutes)
	} else {
		data = buildLatencyResolvedData(job.chainID, job.eval, job.windowMinutes)
	}

	if err := internal.SendLatencyValidator(job.chainID, job.eval.Stat.Addr, data, job.db); err != nil {
		log.Printf("[latency][%s] SendLatencyValidator error: %v", job.chainID, err)
	}

	msg := fmt.Sprintf("p50=%.0fms p90=%.0fms peers_median=%.0fms samples=%d",
		job.eval.Stat.P50, job.eval.Stat.P90, job.eval.PeerMedianP50, job.eval.Stat.Samples)
	// skipped=true marks a dispatched alert, matching the missed-block rows.
	// SendResolveAlerts only looks at WARNING/CRITICAL, so a LATENCY row is
	// never picked up as a pending missed-block incident.
	skipped := job.level == "LATENCY"
	if err := database.InsertAlertlog(job.db, job.chainID, job.eval.Stat.Addr, job.eval.Stat.Moniker,
		job.level, job.eval.Stat.MinHeight, job.eval.Stat.MaxHeight, skipped, time.Now(), msg); err != nil {
		log.Printf("[latency][%s] InsertAlertlog error: %v", job.chainID, err)
	}
}

// runLatencyAlertCycle performs one evaluation pass for a chain.
func runLatencyAlertCycle(db *gorm.DB, chainID string, t Thresholds) {
	if !t.LatencyAlertEnabled {
		return
	}

	dbStats, err := database.GetLatencyAlertStats(db, chainID, t.LatencyAlertWindowMinutes)
	if err != nil {
		log.Printf("[latency][%s] GetLatencyAlertStats error: %v", chainID, err)
		return
	}
	states, err := database.GetLatencyAlertStates(db, chainID)
	if err != nil {
		log.Printf("[latency][%s] GetLatencyAlertStates error: %v", chainID, err)
		return
	}

	valset := GetMonikerMap(chainID)
	stats := make([]latencyStat, 0, len(dbStats))
	for _, s := range dbStats {
		if _, inValset := valset[s.Addr]; !inValset {
			continue
		}
		stats = append(stats, latencyStat{
			Addr: s.Addr, Moniker: s.Moniker, Samples: s.Samples,
			P50: s.P50, P90: s.P90, LateRatio: s.LateRatio,
			MinHeight: s.MinHeight, MaxHeight: s.MaxHeight,
		})
	}

	evals := evaluateLatency(stats, t)
	dispatch := getLatencyDispatch()
	resendAfter := time.Duration(t.LatencyAlertResendHours) * time.Hour

	for addr, ev := range evals {
		active := states[addr].Level == "LATENCY"
		switch {
		case ev.Verdict == verdictTrigger && !active:
			dispatch(latencyJob{db: db, chainID: chainID, level: "LATENCY", eval: ev, windowMinutes: t.LatencyAlertWindowMinutes})
		case ev.Verdict == verdictTrigger && active:
			if time.Since(states[addr].SentAt) >= resendAfter {
				dispatch(latencyJob{db: db, chainID: chainID, level: "LATENCY", eval: ev, windowMinutes: t.LatencyAlertWindowMinutes})
			}
		case ev.Verdict == verdictResolve && active:
			dispatch(latencyJob{db: db, chainID: chainID, level: "LATENCY_RESOLVED", eval: ev, windowMinutes: t.LatencyAlertWindowMinutes})
		}
	}

	// An open alert for a validator that left the valset (or stopped producing
	// samples entirely) would otherwise stay active forever, since it is no
	// longer evaluated. Close it in the log without notifying anyone: there is
	// nothing for an operator to act on.
	for addr, st := range states {
		if st.Level != "LATENCY" {
			continue
		}
		if _, inValset := valset[addr]; inValset {
			continue
		}
		if err := database.InsertAlertlog(db, chainID, addr, addr, "LATENCY_RESOLVED", 0, 0, false, time.Now(),
			"closed: validator no longer in the valset"); err != nil {
			log.Printf("[latency][%s] InsertAlertlog silent resolve error: %v", chainID, err)
		}
	}
}

// WatchLatencyAlerts evaluates signing latency for one chain every
// latency_alert_check_minutes. Unlike WatchValidatorAlerts it does not need a
// short interval: signing latency is a slow signal, and each pass computes
// percentiles over the whole window.
func WatchLatencyAlerts(ctx context.Context, db *gorm.DB, chainID string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[latency][%s] WatchLatencyAlerts panic: %v", chainID, r)
			}
		}()
		for {
			t := GetThresholds()
			if isChainSynced(chainID) {
				runLatencyAlertCycle(db, chainID, t)
			}
			select {
			case <-ctx.Done():
				log.Printf("[latency][%s] WatchLatencyAlerts stopped", chainID)
				return
			case <-time.After(t.LatencyAlertCheckInterval()):
			}
		}
	}()
}
