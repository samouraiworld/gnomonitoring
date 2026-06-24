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

const (
	// bftCheckInterval is how often the chain-level BFT safety margin is evaluated.
	bftCheckInterval = time.Minute

	// bftConfirmCycles is how many consecutive evaluation cycles must agree on a
	// new level before it is broadcast. This rides out transient blips — an RPC
	// hiccup, DB/participation lag, or a freshly-added validator whose
	// participation rows trail the live validator set — that would otherwise
	// fire a false CRITICAL and flap.
	bftConfirmCycles = 3
)

// bftAlertState is the per-chain confirmation state machine for BFT alerts.
// Exactly one WatchBFTMargin goroutine mutates a given chain's state, so the
// fields need no per-instance lock; only the map of states is guarded.
type bftAlertState struct {
	dispatched     string // last broadcast level: "" healthy, "WARNING", "CRITICAL"
	candidate      string // level currently being confirmed
	candidateCount int    // consecutive observations of candidate
}

// observe advances the state machine by one observed level (current ∈
// {"", "WARNING", "CRITICAL"}) and returns the action to broadcast: "alert"
// (dispatch the current level), "resolve" (dispatch a RESOLVED), or "" (nothing
// yet). A new level must be observed `threshold` cycles in a row before it is
// dispatched, so single-cycle blips never alert.
func (s *bftAlertState) observe(current string, threshold int) string {
	if current == s.candidate {
		s.candidateCount++
	} else {
		s.candidate = current
		s.candidateCount = 1
	}
	if s.candidateCount < threshold || s.candidate == s.dispatched {
		return ""
	}
	prev := s.dispatched
	s.dispatched = s.candidate
	return bftAlertTransition(prev, s.candidate)
}

// bftAlertTransition decides what to do given the previously-dispatched alert
// level and the newly-confirmed one. Returns "alert" (dispatch the current
// level), "resolve" (dispatch a RESOLVED), or "" (nothing changed). Pure, so the
// transition logic can be tested without RPC/DB.
func bftAlertTransition(prev, current string) string {
	if current == prev {
		return ""
	}
	if current == "" {
		return "resolve"
	}
	return "alert"
}

// bftStateMu guards bftStates.
var bftStateMu sync.Mutex

// bftStates holds the per-chain confirmation state machine.
var bftStates = make(map[string]*bftAlertState)

func bftStateFor(chainID string) *bftAlertState {
	bftStateMu.Lock()
	defer bftStateMu.Unlock()
	s, ok := bftStates[chainID]
	if !ok {
		s = &bftAlertState{}
		bftStates[chainID] = s
	}
	return s
}

// WatchBFTMargin periodically evaluates the chain's BFT safety margin (live
// validator set crossed with recent-blocks participation) and emits
// chain-level WARNING/CRITICAL/RESOLVED alerts on transitions. It reuses the
// same dispatch (SendInfoValidator) and history (alert_logs, keyed "bft") as the
// stuck-chain alert. Evaluation is skipped while the chain is backfilling.
func WatchBFTMargin(ctx context.Context, db *gorm.DB, chainID string, rpcClient *FallbackRPCClient, interval time.Duration) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[bft][%s] WatchBFTMargin panic: %v", chainID, r)
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Printf("[bft][%s] WatchBFTMargin stopped", chainID)
				return
			case <-ticker.C:
				if !isChainSynced(chainID) {
					continue
				}
				evaluateBFTMargin(db, chainID, rpcClient)
			}
		}
	}()
}

// evaluateBFTMargin computes the current BFT margin and dispatches an alert when
// the alert level changes since the last cycle.
func evaluateBFTMargin(db *gorm.DB, chainID string, rpcClient *FallbackRPCClient) {
	set := fetchValidatorSet(rpcClient, chainID)
	if len(set) == 0 {
		return
	}
	recentRates, _, _, err := CalculateRecentValidatorStatus(db, chainID, GetThresholds().RecentBlocksWindow)
	if err != nil {
		log.Printf("[bft][%s] CalculateRecentValidatorStatus error: %v", chainID, err)
		return
	}

	m := ComputeBFTMargin(set, recentRates)

	// Not evaluable: unknown power, or a set too small for BFT to mean anything.
	// Hold the current alert state instead of treating it as healthy, so a
	// shrinking or partial validator set never triggers a false RESOLVED.
	if m.TotalPower <= 0 || m.TotalCount < bftMinValidatorsForAlert {
		return
	}

	level := BFTAlertLevel(m) // "", "WARNING", or "CRITICAL" for an evaluable set
	state := bftStateFor(chainID)

	switch state.observe(level, bftConfirmCycles) {
	case "alert":
		msg := bftAlertMessage(chainID, level, m)
		log.Println(msg)
		if err := internal.SendInfoValidator(chainID, msg, level, db); err != nil {
			log.Printf("[bft][%s] SendInfoValidator error: %v", chainID, err)
		}
		height := GetLastHeight(chainID)
		if err := database.InsertAlertlog(db, chainID, "bft", "bft", level, height, height, false, time.Now(), msg); err != nil {
			log.Printf("[bft][%s] InsertAlertlog error: %v", chainID, err)
		}
	case "resolve":
		msg := fmt.Sprintf("[%s] ✅ BFT margin restored: can tolerate %d more offline (%d/%d active)",
			chainID, m.TolerableOffline, m.ActiveCount, m.TotalCount)
		log.Println(msg)
		if err := internal.SendInfoValidator(chainID, msg, "INFO", db); err != nil {
			log.Printf("[bft][%s] SendInfoValidator error: %v", chainID, err)
		}
		height := GetLastHeight(chainID)
		if err := database.InsertAlertlog(db, chainID, "bft", "bft", "RESOLVED", height, height, false, time.Now(), msg); err != nil {
			log.Printf("[bft][%s] InsertAlertlog error: %v", chainID, err)
		}
	}
}

// bftAlertMessage builds the broadcast message for a WARNING/CRITICAL BFT alert.
func bftAlertMessage(chainID, level string, m BFTMargin) string {
	switch level {
	case "CRITICAL":
		return fmt.Sprintf("🚨 [%s] BFT CRITICAL: no safety margin — the next validator offline halts the chain (%d/%d active, power %d/%d)",
			chainID, m.ActiveCount, m.TotalCount, m.ActivePower, m.TotalPower)
	case "WARNING":
		return fmt.Sprintf("⚠️ [%s] BFT WARNING: only 1 more validator can go offline before the chain halts (%d/%d active, power %d/%d)",
			chainID, m.ActiveCount, m.TotalCount, m.ActivePower, m.TotalPower)
	default:
		log.Printf("[bft][%s] bftAlertMessage called with unexpected level %q", chainID, level)
		return fmt.Sprintf("⚠️ [%s] BFT alert (%d/%d active, power %d/%d)",
			chainID, m.ActiveCount, m.TotalCount, m.ActivePower, m.TotalPower)
	}
}
