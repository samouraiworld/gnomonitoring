package gnovalidator

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
	"gorm.io/gorm"
)

// rpcAlertAddr is the alert_logs addr used for chain-wide RPC alerts. It
// mirrors the "all" convention used by the blockchain-stuck alert, but stays
// distinct so an RPC outage is never mistaken for a validator incident or for
// a chain halt. Every validator-scoped query in db_score.go excludes it.
const rpcAlertAddr = "rpc"

var (
	rpcAlertMu   sync.Mutex
	rpcAlertSent = make(map[string]time.Time) // chainID → last dispatched RPC outage alert
)

// shouldSendRPCAlert reports whether an RPC outage alert for chainID may be
// dispatched at now, given cooldown. It records the dispatch when it returns
// true, so callers must not call it speculatively.
func shouldSendRPCAlert(chainID string, now time.Time, cooldown time.Duration) bool {
	rpcAlertMu.Lock()
	defer rpcAlertMu.Unlock()
	last, ok := rpcAlertSent[chainID]
	if ok && now.Sub(last) < cooldown {
		return false
	}
	rpcAlertSent[chainID] = now
	return true
}

// clearRPCAlert forgets the last dispatch for chainID so the next outage
// alerts immediately instead of waiting out the cooldown of the one that
// just resolved.
func clearRPCAlert(chainID string) {
	rpcAlertMu.Lock()
	defer rpcAlertMu.Unlock()
	delete(rpcAlertSent, chainID)
}

// resetRPCAlertState clears all per-chain state. Test helper.
func resetRPCAlertState() {
	rpcAlertMu.Lock()
	defer rpcAlertMu.Unlock()
	rpcAlertSent = make(map[string]time.Time)
}

// NewRPCObserver builds the pool observer that turns endpoint transitions
// into operator-visible alerts. A rotation is log-only: the pool recovered on
// its own and there is nothing for an operator to do. Losing every endpoint
// is a CRITICAL, because from that moment on no participation is recorded and
// no validator alert can fire.
func NewRPCObserver(db *gorm.DB, chainID string) rpcpool.Observer {
	return func(ev rpcpool.Event, endpoint string, err error) {
		switch ev {
		case rpcpool.EventRotated:
			log.Printf("[rpc][%s] failed over to endpoint %s", chainID, endpoint)

		case rpcpool.EventAllDown:
			cooldown := GetThresholds().RPCErrorCooldown()
			if !shouldSendRPCAlert(chainID, time.Now(), cooldown) {
				log.Printf("[rpc][%s] all endpoints down, alert suppressed by cooldown (%v)", chainID, cooldown)
				return
			}
			msg := fmt.Sprintf("[%s] 🚨 CRITICAL: every RPC endpoint is unreachable. Block collection and validator alerts are stopped. Last error: %v", chainID, err)
			log.Println(msg)
			data := internal.AlertData{
				ChainID: chainID,
				Level:   internal.AlertCritical,
				Emoji:   "🚨",
				Title:   "RPC endpoints unreachable",
				Fields: []internal.AlertField{
					{Name: "last active endpoint", Value: endpoint},
					{Name: "error", Value: fmt.Sprintf("%v", err)},
					{Name: "impact", Value: "block collection and validator alerts are stopped"},
				},
			}
			if sendErr := internal.SendInfoValidator(chainID, data, db); sendErr != nil {
				log.Printf("[rpc][%s] SendInfoValidator error: %v", chainID, sendErr)
			}
			if logErr := database.InsertAlertlog(db, chainID, rpcAlertAddr, rpcAlertAddr, "CRITICAL", 0, 0, false, time.Now(), msg); logErr != nil {
				log.Printf("[rpc][%s] InsertAlertlog error: %v", chainID, logErr)
			}

		case rpcpool.EventRecovered:
			msg := fmt.Sprintf("[%s] ✅ RPC connectivity restored on %s.", chainID, endpoint)
			log.Println(msg)
			data := internal.AlertData{
				ChainID:     chainID,
				Level:       internal.AlertInfo,
				Emoji:       "✅",
				Title:       "RPC connectivity restored",
				Description: fmt.Sprintf("Serving from %s again.", endpoint),
			}
			if sendErr := internal.SendInfoValidator(chainID, data, db); sendErr != nil {
				log.Printf("[rpc][%s] SendInfoValidator error: %v", chainID, sendErr)
			}
			if logErr := database.InsertAlertlog(db, chainID, rpcAlertAddr, rpcAlertAddr, "RESOLVED", 0, 0, false, time.Now(), msg); logErr != nil {
				log.Printf("[rpc][%s] InsertAlertlog error: %v", chainID, logErr)
			}
			clearRPCAlert(chainID)
		}
	}
}
