package gnovalidator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

// MonikerMap[chainID][addr] = moniker
var MonikerMap = make(map[string]map[string]string)
var MonikerMutex sync.RWMutex

// FirstActiveBlockMap[chainID][addr] = first block height where the validator participated.
// -1 means unknown (not yet seen or not yet populated).
var FirstActiveBlockMap = make(map[string]map[string]int64)
var FirstActiveBlockMutex sync.RWMutex

func GetFirstActiveBlock(chainID, addr string) int64 {
	FirstActiveBlockMutex.RLock()
	defer FirstActiveBlockMutex.RUnlock()
	if chain, ok := FirstActiveBlockMap[chainID]; ok {
		if fab, ok := chain[addr]; ok {
			return fab
		}
	}
	return -1
}

func SetFirstActiveBlock(chainID, addr string, block int64) {
	FirstActiveBlockMutex.Lock()
	defer FirstActiveBlockMutex.Unlock()
	if _, ok := FirstActiveBlockMap[chainID]; !ok {
		FirstActiveBlockMap[chainID] = make(map[string]int64)
	}
	FirstActiveBlockMap[chainID][addr] = block
}

// SetFirstActiveBlockIfEarlier atomically records height as the validator's
// first_active_block if none is recorded yet (-1), or lowers the recorded
// value if height is earlier than what's currently stored. Returns true when
// the in-memory value changed. Unlike a plain Get-then-Set pair, the check
// and the write happen under a single lock acquisition, so concurrent
// callers (e.g. BackfillParallel's workers, which process blocks without a
// guaranteed ascending-height order) can never both observe "unset" and
// race to write a later, non-minimal height as the final answer.
func SetFirstActiveBlockIfEarlier(chainID, addr string, height int64) bool {
	FirstActiveBlockMutex.Lock()
	defer FirstActiveBlockMutex.Unlock()
	if _, ok := FirstActiveBlockMap[chainID]; !ok {
		FirstActiveBlockMap[chainID] = make(map[string]int64)
	}
	current, exists := FirstActiveBlockMap[chainID][addr]
	if exists && current != -1 && current <= height {
		return false
	}
	FirstActiveBlockMap[chainID][addr] = height
	return true
}

// GetFirstActiveBlockMap returns a snapshot of the first_active_block map for a chain.
func GetFirstActiveBlockMap(chainID string) map[string]int64 {
	FirstActiveBlockMutex.RLock()
	defer FirstActiveBlockMutex.RUnlock()
	chain, ok := FirstActiveBlockMap[chainID]
	if !ok {
		return make(map[string]int64)
	}
	snapshot := make(map[string]int64, len(chain))
	for addr, fab := range chain {
		snapshot[addr] = fab
	}
	return snapshot
}

// timeMu protects the per-chain time maps below since time.Time is not atomic-safe.
var timeMu sync.Mutex
var lastRPCErrorAlert = make(map[string]time.Time)      // per-chain RPC error anti-spam
var lastProgressTime = make(map[string]time.Time)       // per-chain last block progress time
var lastStagnationAlertTime = make(map[string]time.Time) // per-chain last stagnation alert time

// lastProgressHeight[chainID] = block height
var lastProgressHeight = make(map[string]int64)
var heightMutex sync.RWMutex

// alertSent[chainID][addr] = bool
var alertSent = make(map[string]map[string]bool)
var alertMutex sync.RWMutex

// restoredNotified[chainID][addr] = bool
var restoredNotified = make(map[string]map[string]bool)
var restoreMutex sync.RWMutex

var chainRPCClients = make(map[string]*FallbackRPCClient)
var chainRPCClientsMu sync.RWMutex

// chainSynced[chainID] = true once the backfill gap drops below the threshold.
// WatchValidatorAlerts skips processing while false to avoid historical alert spam.
var chainSynced = make(map[string]bool)
var chainSyncedMu sync.RWMutex

func setChainSynced(chainID string, v bool) {
	chainSyncedMu.Lock()
	chainSynced[chainID] = v
	chainSyncedMu.Unlock()
}

func isChainSynced(chainID string) bool {
	chainSyncedMu.RLock()
	defer chainSyncedMu.RUnlock()
	return chainSynced[chainID]
}

func SetChainRPCClient(chainID string, client *FallbackRPCClient) {
	chainRPCClientsMu.Lock()
	defer chainRPCClientsMu.Unlock()
	chainRPCClients[chainID] = client
}

func GetChainRPCClient(chainID string) (*FallbackRPCClient, bool) {
	chainRPCClientsMu.RLock()
	defer chainRPCClientsMu.RUnlock()
	c, ok := chainRPCClients[chainID]
	return c, ok
}

func GetLastProgressTime(chainID string) (time.Time, bool) {
	timeMu.Lock()
	defer timeMu.Unlock()
	t, ok := lastProgressTime[chainID]
	return t, ok
}

type BlockParticipation struct {
	Height     int64
	Validators map[string]bool
}

type Participation struct {
	Participated   bool
	Timestamp      time.Time
	TxContribution bool
	Proposed       bool
}

func CollectParticipation(ctx context.Context, db *gorm.DB, chainID string, client gnoclient.Client) {
	// simulateCount := 0
	// simulateMax := 4   // for test
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[monitor][%s] panic recovered: %v", chainID, r)
			}
		}()

		lastStored, err := GetLastStoredHeight(db, chainID)

		if lastStored == 0 {
			log.Printf("[monitor][%s] no stored blocks, starting from genesis", chainID)
			lastStored = 0
			// lastStored, err = client.LatestBlockHeight()
			if err != nil {
				return
			}
		}

		currentHeight := lastStored + 1
		for {

			latest, err := client.LatestBlockHeight()
			if err != nil {
				log.Printf("[monitor][%s] error fetching latest height: %v", chainID, err)

				timeMu.Lock()
				sinceRPCErr := time.Since(lastRPCErrorAlert[chainID])
				timeMu.Unlock()
				t := GetThresholds()
				if sinceRPCErr > t.RPCErrorCooldown() {
					msg := fmt.Sprintf("⚠️ Error when querying latest block height: %v", err)
					msg += fmt.Sprintf("\nLast known block height: %d", currentHeight)
					log.Println(msg)
					timeMu.Lock()
					lastRPCErrorAlert[chainID] = time.Now()
					timeMu.Unlock()
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
				continue
			}
			// Stagnation detection
			lph := GetLastHeight(chainID)
			timeMu.Lock()
			lpt, lptSet := lastProgressTime[chainID]
			if !lptSet {
				lpt = time.Now()
				lastProgressTime[chainID] = lpt
			}
			lastAlert := lastStagnationAlertTime[chainID]
			timeMu.Unlock()

			if lph != 0 && latest == lph {
				stuckFor := time.Since(lpt)
				t := GetThresholds()
				firstAlert := lastAlert.IsZero()
				shouldAlert := (firstAlert && stuckFor > t.StagnationFirstAlert()) ||
					(!firstAlert && time.Since(lastAlert) > t.StagnationRepeat())

				if shouldAlert {
					blockTime, err := database.GetTimeOfBlock(db, chainID, latest)
					if err != nil {
						log.Printf("[monitor][%s] cannot get block time for height %d: %v", chainID, latest, err)
						continue
					}
					elapsed := time.Since(blockTime).Truncate(time.Second)

					msg := fmt.Sprintf(
						"🚨 [%s] CRITICAL : Blockchain stuck at height %d since %s (%s ago)",
						chainID,
						latest,
						blockTime.Format(time.RFC822),
						elapsed,
					)
					log.Println(msg)

					if err := internal.SendInfoValidator(chainID, msg, "CRITICAL", db); err != nil {
						log.Printf("[monitor][%s] SendInfoValidator error: %v", chainID, err)
					}
					if err := database.InsertAlertlog(db, chainID, "all", "all", "CRITICAL", latest, latest, false, time.Now(), msg); err != nil {
						log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
					}

					timeMu.Lock()
					lastStagnationAlertTime[chainID] = time.Now()
					timeMu.Unlock()
					SetAlertSent(chainID, "all", true)
					SetRestoredNotified(chainID, "all", false)
				}
			} else {
				SetLastHeight(chainID, latest)
				timeMu.Lock()
				lastProgressTime[chainID] = time.Now()
				lastStagnationAlertTime[chainID] = time.Time{}
				timeMu.Unlock()

				if IsAlertSent(chainID, "all") && !IsRestoredNotified(chainID, "all") {
					msg := fmt.Sprintf("[%s] ✅ Activity Restored: Gno.land is back to normal.", chainID)
					if err := internal.SendInfoValidator(chainID, msg, "INFO", db); err != nil {
						log.Printf("[monitor][%s] SendInfoValidator error: %v", chainID, err)
					}
					if err := database.InsertAlertlog(db, chainID, "all", "all", "RESOLVED", latest, latest, false, time.Now(), msg); err != nil {
						log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
					}
					SetRestoredNotified(chainID, "all", true)
					SetAlertSent(chainID, "all", false)
				}
			}

			timeMu.Lock()
			lastRPCErrorAlert[chainID] = time.Time{}
			timeMu.Unlock()

			if latest <= currentHeight {
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			// *** BLOCKING BACKFILL IF LARGE GAP ***
			if latest-currentHeight > 500 {
				setChainSynced(chainID, false)
				// catch up to latest-200 to leave a buffer
				// (avoids race with realtime stream afterwards)
				stop := latest - 200
				if stop < currentHeight {
					stop = latest // at worst, catch up everything
				}
				log.Printf("[monitor][%s] backfill [%d..%d] (gap=%d)", chainID, currentHeight, stop, latest-currentHeight)
				if err := BackfillParallel(db, client, chainID, currentHeight, stop, GetMonikerMap(chainID)); err != nil {
					log.Printf("[monitor][%s] backfill error: %v", chainID, err)
					// if backfill fails, do not block indefinitely
				} else {
					// jump directly to the end of the backfill
					currentHeight = stop + 1
					log.Printf("[monitor][%s] backfill complete up to %d, switching to realtime", chainID, stop)
				}
				// do not switch to "realtime" while the gap is still large
				continue
			}
			// gap is small — chain is considered synced, alerts may now fire
			setChainSynced(chainID, true)
			// log.Println("last block ", latest)

			for h := currentHeight; h <= latest; h++ {
				block, err := client.Block(h)
				if err != nil || block == nil || block.Block == nil || block.Block.LastCommit == nil {
					log.Printf("[monitor][%s] error fetching block %d: %v", chainID, h, err)
					continue
				}

				// ================================ Get Participation and date ==================== //

				// Actual block proposer, resolved once. A block always has a
				// proposer; hasTx gates whether TxContribution is meaningful.
				proposerAddr := block.Block.Header.ProposerAddress.String()
				hasTx := len(block.Block.Data.Txs) > 0

				// === Get Timestamp ==

				timeStp := block.Block.Header.Time

				precommitAddrs := make([]string, 0, len(block.Block.LastCommit.Precommits))
				for _, precommit := range block.Block.LastCommit.Precommits {
					if precommit != nil {
						precommitAddrs = append(precommitAddrs, precommit.ValidatorAddress.String())
					}
				}
				participating := buildParticipation(precommitAddrs, proposerAddr, hasTx, timeStp)

				err = SaveParticipation(db, chainID, h, participating, GetMonikerMap(chainID), timeStp)
				if err != nil {
					log.Printf("[monitor][%s] failed to save participation at height %d: %v", chainID, h, err)
				}
			}

			currentHeight = latest
		}
	}()
}

func WatchNewValidators(ctx context.Context, db *gorm.DB, chainID string, client gnoclient.Client, chainCfg *internal.ChainConfig, refreshInterval time.Duration) {
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Printf("[monitor][%s] WatchNewValidators stopped", chainID)
				return
			case <-ticker.C:
				oldMap := GetMonikerMap(chainID)
				prevSigningToOperator := getSigningToOperator(chainID)

				valopers := InitMonikerMap(db, chainID, client, chainCfg)

				newMap := GetMonikerMap(chainID)
				// Only update signing-to-operator mapping (and the cached valoper
				// fallback) if we have fresh valoper data. A transient valoper-registry
				// fetch failure should not erase the correlation memory built from the
				// last successful fetch, since MonikerMap itself can still update
				// independently that same cycle.
				if len(valopers) > 0 {
					setSigningToOperator(chainID, signingToOperatorFromValopers(valopers))
					setCachedValopers(chainID, valopers)
				}
				// classifyValsetChanges needs currentValopers to correlate a rotation;
				// fall back to the last known-good snapshot on a transient fetch
				// failure instead of an empty list, for the same reason as above.
				currentValopers := valopers
				if len(currentValopers) == 0 {
					currentValopers = getCachedValopers(chainID)
				}

				var departed bool
				for _, ev := range classifyValsetChanges(oldMap, newMap, prevSigningToOperator, currentValopers) {
					var msg string
					switch ev.Kind {
					case ValidatorJoined:
						msg = fmt.Sprintf("[%s] ✅ **New Validator detected**: %s (%s)", chainID, ev.Moniker, ev.NewAddr)
					case ValidatorLeft:
						msg = fmt.Sprintf("[%s] ⚠️ **Validator left the valset**: %s (%s)", chainID, ev.Moniker, ev.OldAddr)
						departed = true
					case ValidatorAddressChanged:
						msg = fmt.Sprintf("[%s] 🔄 **Validator address changed**: %s (%s → %s)", chainID, ev.Moniker, ev.OldAddr, ev.NewAddr)
						departed = true
					}
					log.Println(msg)
					if err := internal.SendInfoValidator(chainID, msg, "info", db); err != nil {
						log.Printf("[monitor][%s] SendInfoValidator error: %v", chainID, err)
					}
				}

				// A departure/rotation just made an address stop accumulating true
				// participation; clean up its trailing ghost rows now instead of
				// waiting for the next process restart.
				if departed {
					currentAddrs := make([]string, 0, len(newMap))
					for addr := range newMap {
						currentAddrs = append(currentAddrs, addr)
					}
					if err := database.CleanupTrailingGhostParticipations(ctx, db, chainID, currentAddrs); err != nil {
						log.Printf("[monitor][%s] CleanupTrailingGhostParticipations error: %v", chainID, err)
					}
				}
			}
		}
	}()
}

func WatchValidatorAlerts(ctx context.Context, db *gorm.DB, chainID string, checkInterval time.Duration) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[validator][%s] WatchValidatorAlerts panic: %v", chainID, r)
			}
		}()
		for {
			// During backfill, skip new-alert detection but still dispatch
			// pending RESOLVED alerts so the dedup window is not permanently
			// blocked by un-resolved WARNING/CRITICAL entries.
			if !isChainSynced(chainID) {
				SendResolveAlerts(db, chainID)
				select {
				case <-ctx.Done():
					return
				case <-time.After(checkInterval):
				}
				continue
			}

			today := time.Now().Format("2006-01-02")

			// Query returns one row per contiguous missed sequence within the
			// recent window. The window is kept short (30 min) to avoid
			// surfacing dozens of already-resolved historical sequences that
			// would each trigger a dedup DB round-trip. Active incidents are
			// always visible because new blocks are always within the window.
			// The NOT EXISTS filter on alert_logs skips sequences already
			// covered by a RESOLVED, eliminating the dedup check entirely for
			// those sequences.
			windows, err := database.GetMissedWindows(db, chainID, GetThresholds().WarningThreshold)
			if err != nil {
				log.Printf("[validator][%s] error executing missed blocks query: %v", chainID, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(checkInterval):
				}
				continue
			}
			log.Printf("[validator][%s] alert check: found %d missed-block window(s) above threshold=%d",
				chainID, len(windows), GetThresholds().WarningThreshold)

			// Snapshot thresholds once for the whole cycle to avoid TOCTOU
			// between the SQL filter and the Go-level level classification.
			t := GetThresholds()

			for _, w := range windows {
				addr := w.Addr
				moniker := w.Moniker
				start_height := w.StartHeight
				end_height := w.EndHeight
				missed := w.Missed

				var level string
				switch {
				case missed >= t.CriticalThreshold:
					level = "CRITICAL"
				case missed >= t.WarningThreshold:
					level = "WARNING"
				default:
					continue
				}

				// Time-based dedup: skip if an alert of this level was already sent recently,
				// UNLESS a RESOLVED was dispatched after that alert AND the current missed
				// sequence starts strictly after the resolved range (i.e. a genuinely new
				// incident, not the same down-period being re-detected).
				resendHours := t.ResendHoursForLevel(level)
				window := fmt.Sprintf("%d hours", resendHours)
				var recentCount int64
				err := db.Raw(`
					SELECT COUNT(*) FROM alert_logs al
					WHERE al.chain_id = ? AND al.addr = ? AND al.level = ?
					AND al.skipped = true
					AND al.sent_at >= NOW() - ?::interval
					AND NOT EXISTS (
						SELECT 1 FROM alert_logs r
						WHERE r.chain_id = al.chain_id
						  AND r.addr = al.addr
						  AND r.level = 'RESOLVED'
						  AND r.sent_at > al.sent_at
						  AND ? > r.end_height
					)
				`, chainID, addr, level, window, start_height).Scan(&recentCount).Error
				if err != nil {
					log.Printf("[validator][%s] DB error checking alert_logs: %v", chainID, err)
					continue
				}
				if recentCount > 0 {
					log.Printf("[validator][%s] dedup: skipping %s alert for %s (%s) start=%d end=%d missed=%d (recent alert within %dh window, no RESOLVED since)",
						chainID, level, moniker, addr, start_height, end_height, missed, resendHours)
					continue
				}

				// Silence permanently dead validators: skip if no participation in the last N days.
				if t.DeadValidatorSilenceDays > 0 {
					silenceWindow := fmt.Sprintf("%d days", t.DeadValidatorSilenceDays)
					var activeRecently int64
					err = db.Raw(`
						SELECT COUNT(*) FROM daily_participations
						WHERE chain_id = ? AND addr = ? AND participated = true
						AND date >= NOW() - ?::interval
					`, chainID, addr, silenceWindow).Scan(&activeRecently).Error
					if err != nil {
						log.Printf("[validator][%s] DB error checking silence window: %v", chainID, err)
						continue
					}
					if activeRecently == 0 {
						log.Printf("[validator][%s] silence: skipping %s alert for %s (%s): no participation in last %d days",
							chainID, level, moniker, addr, t.DeadValidatorSilenceDays)
						continue
					}
				}

				if err := internal.SendAllValidatorAlerts(chainID, missed, today, level, addr, moniker, start_height, end_height, db); err != nil {
					log.Printf("[validator][%s] SendAllValidatorAlerts error: %v", chainID, err)
				}
				if err := database.InsertAlertlog(db, chainID, addr, moniker, level, start_height, end_height, true, time.Now(), ""); err != nil {
					log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
				}
			}

			SendResolveAlerts(db, chainID)
			select {
			case <-ctx.Done():
				log.Printf("[monitor][%s] WatchValidatorAlerts stopped", chainID)
				return
			case <-time.After(checkInterval):
			}
		}
	}()
}
func SendResolveAlerts(db *gorm.DB, chainID string) {
	// Source of truth: dispatched WARNING/CRITICAL alert_logs entries that have
	// no corresponding RESOLVED yet. Avoids the rolling-window race where a
	// recomputed sequence end_height never matches a previously stored RESOLVED.
	type pendingAlert struct {
		Addr        string
		Moniker     string
		StartHeight int64
		EndHeight   int64
	}

	var pending []pendingAlert
	err := db.Raw(`
		SELECT DISTINCT ON (al.addr)
		       al.addr,
		       COALESCE(am.moniker, al.moniker, '') AS moniker,
		       al.start_height,
		       al.end_height
		FROM alert_logs al
		LEFT JOIN addr_monikers am ON am.chain_id = al.chain_id AND am.addr = al.addr
		WHERE al.chain_id = ?
		  AND al.level IN ('WARNING', 'CRITICAL')
		  AND al.skipped = true
		  AND NOT EXISTS (
		      SELECT 1 FROM alert_logs r
		      WHERE r.chain_id = al.chain_id
		        AND r.addr     = al.addr
		        AND r.level    = 'RESOLVED'
		        AND r.end_height >= al.end_height
		  )
		ORDER BY al.addr, al.end_height DESC
		LIMIT 50
	`, chainID).Scan(&pending).Error
	if err != nil {
		log.Printf("[validator][%s] SendResolveAlerts query error: %v", chainID, err)
		return
	}
	log.Printf("[validator][%s] SendResolveAlerts: %d pending alert(s) without RESOLVED", chainID, len(pending))

	for _, a := range pending {
		// Find the first block where the validator participated again after the
		// missed sequence. No upper bound: the recovery may come many blocks later
		// (e.g. long maintenance). The dead-validator silence check in
		// WatchValidatorAlerts already prevents perpetual alerts for truly dead
		// validators; here we only need to know when they returned.
		var resumeHeight int64
		err := db.Raw(`
			SELECT COALESCE(MIN(block_height), 0)
			FROM daily_participations
			WHERE chain_id    = ?
			  AND addr        = ?
			  AND block_height > ?
			  AND participated = true
		`, chainID, a.Addr, a.EndHeight).Scan(&resumeHeight).Error
		if err != nil {
			log.Printf("[validator][%s] DB error checking participation: %v", chainID, err)
			continue
		}
		if resumeHeight == 0 {
			log.Printf("[validator][%s] RESOLVED pending for %s (%s): no participation found after block %d yet",
				chainID, a.Moniker, a.Addr, a.EndHeight)
			continue
		}

		resolveMsg := fmt.Sprintf("[%s] ✅ RESOLVED: No more missed blocks for %s (%s) at Block %d",
			chainID, a.Moniker, a.Addr, resumeHeight)
		if err := internal.SendResolveValidator(chainID, resolveMsg, a.Addr, db); err != nil {
			log.Printf("[validator][%s] SendResolveValidator error: %v", chainID, err)
		}
		if err := database.InsertAlertlog(db, chainID, a.Addr, a.Moniker, "RESOLVED", a.StartHeight, a.EndHeight, false, time.Now(), ""); err != nil {
			log.Printf("[monitor][%s] InsertAlertlog RESOLVED error: %v", chainID, err)
		}
	}
}

func SaveParticipation(db *gorm.DB, chainID string, blockHeight int64, participating map[string]Participation, monikerMap map[string]string, timeStp time.Time) error {
	rows := make([]dpRow, 0, len(monikerMap))
	for valAddr, moniker := range monikerMap {
		participated := participating[valAddr] // zero value => not participated

		if RecordActivationOrSkip(db, chainID, valAddr, blockHeight, participated.Participated) {
			continue
		}

		rows = append(rows, dpRow{
			ChainID:        chainID,
			Date:           timeStp,
			BlockHeight:    blockHeight,
			Moniker:        moniker,
			Addr:           valAddr,
			Participated:   participated.Participated,
			TxContribution: participated.TxContribution,
			Proposed:       participated.Proposed,
		})
	}

	// One multi-VALUES INSERT (chunked by flushBatch) instead of one Exec per
	// validator — removes per-row parse/plan cost on the realtime hot path.
	if err := flushBatch(db, rows); err != nil {
		log.Printf("[monitor][%s] failed to save participation at height %d: %v", chainID, blockHeight, err)
		return err
	}

	if blockHeight%100 == 0 {
		log.Printf("[monitor][%s] synced block %d", chainID, blockHeight)
	}
	return nil
}

func StartValidatorMonitoring(ctx context.Context, db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	if ctx.Err() != nil {
		return
	}

	rpcClient := NewFallbackRPCClient(chainCfg.RPCEndpoints)
	SetChainRPCClient(chainID, rpcClient)
	client := gnoclient.Client{RPCClient: rpcClient}

	t := GetThresholds()
	valopers := InitMonikerMap(db, chainID, client, chainCfg)
	// Seed the signing-to-operator correlation memory (and its cached valoper
	// fallback) from this startup fetch, so the very first WatchNewValidators
	// tick can already correlate a signing-key rotation instead of starting
	// from an empty prevSigningToOperator snapshot.
	if len(valopers) > 0 {
		setSigningToOperator(chainID, signingToOperatorFromValopers(valopers))
		setCachedValopers(chainID, valopers)
	}

	if ctx.Err() != nil {
		return
	}

	currentAddrs := make([]string, 0)
	for addr := range GetMonikerMap(chainID) {
		currentAddrs = append(currentAddrs, addr)
	}
	if err := database.CleanupTrailingGhostParticipations(ctx, db, chainID, currentAddrs); err != nil {
		log.Printf("[monitor][%s] CleanupTrailingGhostParticipations error: %v", chainID, err)
	}

	WatchNewValidators(ctx, db, chainID, client, chainCfg, t.NewValidatorScan())
	CollectParticipation(ctx, db, chainID, client)
	WatchValidatorAlerts(ctx, db, chainID, t.AlertCheckInterval())
}

// Moniker helpers

func GetMonikerMap(chainID string) map[string]string {
	MonikerMutex.RLock()
	defer MonikerMutex.RUnlock()
	m, ok := MonikerMap[chainID]
	if !ok {
		return make(map[string]string)
	}
	snapshot := make(map[string]string, len(m))
	for k, v := range m {
		snapshot[k] = v
	}
	return snapshot
}

func SetMoniker(chainID, addr, moniker string) {
	MonikerMutex.Lock()
	defer MonikerMutex.Unlock()
	if _, ok := MonikerMap[chainID]; !ok {
		MonikerMap[chainID] = make(map[string]string)
	}
	MonikerMap[chainID][addr] = moniker
}

// ReplaceMonikerMap atomically replaces the entire per-chain moniker map with
// m, instead of merging into it. Used by InitMonikerMap so MonikerMap always
// reflects exactly the validators currently in the live valset — a validator
// that drops out of /validators is dropped from this map on the very next
// refresh cycle, instead of lingering forever (which used to make
// SaveParticipation keep writing participated=false rows for it long after
// it left).
func ReplaceMonikerMap(chainID string, m map[string]string) {
	MonikerMutex.Lock()
	defer MonikerMutex.Unlock()
	MonikerMap[chainID] = m
}

// Height helpers

func GetLastHeight(chainID string) int64 {
	heightMutex.RLock()
	defer heightMutex.RUnlock()
	return lastProgressHeight[chainID]
}

func SetLastHeight(chainID string, height int64) {
	heightMutex.Lock()
	defer heightMutex.Unlock()
	lastProgressHeight[chainID] = height
}

// Alert helpers

func IsAlertSent(chainID, addr string) bool {
	alertMutex.RLock()
	defer alertMutex.RUnlock()
	if m, ok := alertSent[chainID]; ok {
		return m[addr]
	}
	return false
}

func SetAlertSent(chainID, addr string, sent bool) {
	alertMutex.Lock()
	defer alertMutex.Unlock()
	if _, ok := alertSent[chainID]; !ok {
		alertSent[chainID] = make(map[string]bool)
	}
	alertSent[chainID][addr] = sent
}

// Restored helpers

func IsRestoredNotified(chainID, addr string) bool {
	restoreMutex.RLock()
	defer restoreMutex.RUnlock()
	if m, ok := restoredNotified[chainID]; ok {
		return m[addr]
	}
	return false
}

func SetRestoredNotified(chainID, addr string, notified bool) {
	restoreMutex.Lock()
	defer restoreMutex.Unlock()
	if _, ok := restoredNotified[chainID]; !ok {
		restoredNotified[chainID] = make(map[string]bool)
	}
	restoredNotified[chainID][addr] = notified
}

