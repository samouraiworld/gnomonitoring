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

// bftCheckInterval is how often the chain-level BFT safety margin is evaluated.
const bftCheckInterval = time.Minute

// bftLevelMu guards lastBFTLevel.
var bftLevelMu sync.Mutex

// lastBFTLevel records the last BFT alert level dispatched per chain ("" when
// healthy / not alerting), so alerts fire only on transitions, not every cycle.
var lastBFTLevel = make(map[string]string)

func getLastBFTLevel(chainID string) string {
	bftLevelMu.Lock()
	defer bftLevelMu.Unlock()
	return lastBFTLevel[chainID]
}

func setLastBFTLevel(chainID, level string) {
	bftLevelMu.Lock()
	defer bftLevelMu.Unlock()
	lastBFTLevel[chainID] = level
}

// bftAlertTransition decides what to do given the previously-dispatched alert
// level and the current one. Returns "alert" (dispatch the current level),
// "resolve" (dispatch a RESOLVED), or "" (nothing changed). It is a pure
// function so the transition logic can be tested without RPC/DB.
func bftAlertTransition(prev, current string) string {
	if current == prev {
		return ""
	}
	if current == "" {
		return "resolve"
	}
	return "alert"
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
	level := BFTAlertLevel(m)
	prev := getLastBFTLevel(chainID)

	switch bftAlertTransition(prev, level) {
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
		setLastBFTLevel(chainID, level)
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
		setLastBFTLevel(chainID, "")
	}
}

// bftAlertMessage builds the broadcast message for a WARNING/CRITICAL BFT alert.
func bftAlertMessage(chainID, level string, m BFTMargin) string {
	switch level {
	case "CRITICAL":
		return fmt.Sprintf("🚨 [%s] BFT CRITICAL: no safety margin — the next validator offline halts the chain (%d/%d active, power %d/%d)",
			chainID, m.ActiveCount, m.TotalCount, m.ActivePower, m.TotalPower)
	default: // WARNING
		return fmt.Sprintf("⚠️ [%s] BFT WARNING: only 1 more validator can go offline before the chain halts (%d/%d active, power %d/%d)",
			chainID, m.ActiveCount, m.TotalCount, m.ActivePower, m.TotalPower)
	}
}
