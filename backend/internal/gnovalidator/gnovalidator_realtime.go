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
	"github.com/samouraiworld/gnomonitoring/backend/internal/jobclock"
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
var lastProgressTime = make(map[string]time.Time)        // per-chain last block progress time
var lastStagnationAlertTime = make(map[string]time.Time) // per-chain last stagnation alert time
var regressionSince = make(map[string]time.Time)         // per-chain: when the current height regression episode started (zero = none in progress)

// lastProgressHeight[chainID] = block height
var lastProgressHeight = make(map[string]int64)
var heightMutex sync.RWMutex

// alertSent[chainID][addr] = bool
var alertSent = make(map[string]map[string]bool)
var alertMutex sync.RWMutex

// restoredNotified[chainID][addr] = bool
var restoredNotified = make(map[string]map[string]bool)
var restoreMutex sync.RWMutex

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
	// PrecommitLagMs and LateForQuorum are nil unless the validator signed the
	// committed BlockID (see computePrecommitLatency).
	PrecommitLagMs *int64
	LateForQuorum  *bool
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
				// The pool has already tried every endpoint and, on a full
				// outage, dispatched the CRITICAL alert through its observer
				// (see NewRPCObserver). Nothing to alert on here.
				log.Printf("[monitor][%s] error fetching latest height: %v", chainID, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
				continue
			}
			// Stagnation detection
			lph := GetLastHeight(chainID)

			// The pool may have failed over to an endpoint a few blocks
			// behind the previous one. That is not chain progress, and
			// counting it as such would reset the stagnation timer on every
			// flip-flop between two endpoints at different heights.
			//
			// Trade-off: never lowering lph on a regression is what makes this
			// immune to that oscillation, but taken alone it would also make a
			// *permanent* rewind invisible forever — e.g. an operator restores
			// a node from an older snapshot and that node then genuinely
			// halts. latest could then never again reach lph, so this branch
			// would `continue` forever and the CRITICAL "Blockchain stuck"
			// alert would never fire — the same class of bug this guard
			// exists to fix, just triggered from the other side. regressionSince
			// bounds how long a regression may be ignored: once it has
			// persisted past GetThresholds().StagnationFirstAlert() — the same
			// threshold that already governs "no forward progress for too
			// long" — the lower height is accepted as the new baseline via
			// SetLastHeight, and classification resumes normally so a
			// genuinely halted rewound node reaches the stalled branch below
			// and raises its alert.
			obs := classifyHeightObservation(latest, lph)
			if obs == heightRegressed {
				now := time.Now()
				timeMu.Lock()
				since := regressionSince[chainID]
				accept, next := evaluateRegression(since, now, GetThresholds().StagnationFirstAlert())
				regressionSince[chainID] = next
				timeMu.Unlock()

				if !accept {
					log.Printf("[monitor][%s] ignoring regressed height %d (highest seen %d); the active endpoint is behind",
						chainID, latest, lph)
					select {
					case <-ctx.Done():
						return
					case <-time.After(3 * time.Second):
					}
					continue
				}

				log.Printf("[monitor][%s] height %d has stayed regressed (highest seen was %d) for %s, longer than the stagnation threshold; accepting it as the new baseline instead of ignoring it forever",
					chainID, latest, lph, now.Sub(since))
				SetLastHeight(chainID, latest)
				// Reset the stagnation clock instead of falling straight
				// into the stalled branch below: lastProgressTime was
				// deliberately NOT updated while this regression was being
				// ignored (this path `continue`s before reaching the
				// progress branch), so stuckFor := time.Since(lpt) would
				// already exceed StagnationFirstAlert() on this very line —
				// firing a CRITICAL "Blockchain stuck" instantly on every
				// acceptance. That is correct for a node truly rewound and
				// halted, but the far more common trigger for reaching this
				// branch is the pool failing over to a healthy backup that
				// is merely a few blocks behind and advancing at the same
				// rate as before, which never "catches up" to lph and so
				// always ends up here. Treating the accepted height as a
				// fresh baseline and letting the *next* poll decide means a
				// healthy lagging backup (which advances next poll) never
				// alerts, while a genuinely halted rewound node (which does
				// not advance) still reaches the stalled branch and alerts,
				// just one StagnationFirstAlert() window later than an
				// instant-fire would have. Reuses `now`, captured above at
				// the top of this observation, rather than taking a fresh
				// timestamp.
				timeMu.Lock()
				lastProgressTime[chainID] = now
				lastStagnationAlertTime[chainID] = time.Time{}
				timeMu.Unlock()
				obs = heightStalled
			} else {
				timeMu.Lock()
				regressionSince[chainID] = time.Time{}
				timeMu.Unlock()
			}

			timeMu.Lock()
			lpt, lptSet := lastProgressTime[chainID]
			if !lptSet {
				lpt = time.Now()
				lastProgressTime[chainID] = lpt
			}
			lastAlert := lastStagnationAlertTime[chainID]
			timeMu.Unlock()

			if obs == heightStalled {
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

					data := internal.AlertData{
						ChainID: chainID,
						Level:   internal.AlertCritical,
						Emoji:   "🚨",
						Title:   "Blockchain stuck",
						Fields: []internal.AlertField{
							{Name: "height", Value: fmt.Sprintf("%d", latest)},
							{Name: "since", Value: blockTime.Format(time.RFC822)},
							{Name: "elapsed", Value: elapsed.String() + " ago"},
						},
					}
					if err := internal.SendInfoValidator(chainID, data, db); err != nil {
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
					data := internal.AlertData{
						ChainID:     chainID,
						Level:       internal.AlertInfo,
						Emoji:       "✅",
						Title:       "Activity Restored",
						Description: "Gno.land is back to normal.",
					}
					if err := internal.SendInfoValidator(chainID, data, db); err != nil {
						log.Printf("[monitor][%s] SendInfoValidator error: %v", chainID, err)
					}
					if err := database.InsertAlertlog(db, chainID, "all", "all", "RESOLVED", latest, latest, false, time.Now(), msg); err != nil {
						log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
					}
					SetRestoredNotified(chainID, "all", true)
					SetAlertSent(chainID, "all", false)
				}
			}

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

			votingPower := getValsetVotingPower(chainID)
			for h := currentHeight; h <= latest; h++ {
				fb, ok := fetchBlockParticipation(client, h)
				if !ok {
					log.Printf("[monitor][%s] giving up on block %d after retries", chainID, h)
					continue
				}

				participating := fb.participation(votingPower)

				if err := SaveParticipation(db, chainID, h, participating, GetMonikerMap(chainID), fb.Time); err != nil {
					log.Printf("[monitor][%s] failed to save participation at height %d: %v", chainID, h, err)
				}
			}

			currentHeight = latest
		}
	}()
}

func WatchNewValidators(ctx context.Context, db *gorm.DB, chainID string, client gnoclient.Client, chainCfg *internal.ChainConfig, refreshInterval time.Duration) {
	go func() {
		if refreshInterval <= 0 {
			refreshInterval = jobclock.Cycle
		}
		log.Printf("[monitor][%s] valset refresh every %v at +%v", chainID, refreshInterval,
			jobclock.ValsetOffset(chainID, internal.EnabledChainsSnapshot()))

		for {
			select {
			case <-ctx.Done():
				log.Printf("[monitor][%s] WatchNewValidators stopped", chainID)
				return
			case <-time.After(jobclock.UntilNext(time.Now(), refreshInterval,
				jobclock.ValsetOffset(chainID, internal.EnabledChainsSnapshot()))):
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
					var data internal.AlertData
					switch ev.Kind {
					case ValidatorJoined:
						msg = fmt.Sprintf("[%s] ✅ **New Validator detected**: %s (%s)", chainID, ev.Moniker, ev.NewAddr)
						data = internal.AlertData{
							ChainID: chainID,
							Level:   internal.AlertInfo,
							Emoji:   "✅",
							Title:   "New Validator detected",
							Fields: []internal.AlertField{
								{Name: "moniker", Value: ev.Moniker},
								{Name: "address", Value: ev.NewAddr},
							},
						}
					case ValidatorLeft:
						msg = fmt.Sprintf("[%s] ⚠️ **Validator left the valset**: %s (%s)", chainID, ev.Moniker, ev.OldAddr)
						departed = true
						data = internal.AlertData{
							ChainID: chainID,
							Level:   internal.AlertInfo,
							Emoji:   "⚠️",
							Title:   "Validator left the valset",
							Fields: []internal.AlertField{
								{Name: "moniker", Value: ev.Moniker},
								{Name: "address", Value: ev.OldAddr},
							},
						}
					case ValidatorAddressChanged:
						msg = fmt.Sprintf("[%s] 🔄 **Validator address changed**: %s (%s → %s)", chainID, ev.Moniker, ev.OldAddr, ev.NewAddr)
						departed = true
						data = internal.AlertData{
							ChainID: chainID,
							Level:   internal.AlertInfo,
							Emoji:   "🔄",
							Title:   "Validator address changed",
							Fields: []internal.AlertField{
								{Name: "moniker", Value: ev.Moniker},
								{Name: "old address", Value: ev.OldAddr},
								{Name: "new address", Value: ev.NewAddr},
							},
						}
					}
					log.Println(msg)
					if err := internal.SendInfoValidator(chainID, data, db); err != nil {
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

// ReconcileGaps periodically scans for block heights within the last
// lookbackDays that have no daily_participations row at all — the silent
// hole left behind when fetchBlockParticipation exhausts its retries — and
// backfills exactly those heights. It runs once immediately (so a freshly
// restarted service self-heals recent gaps right away) and then on every
// tick, for as long as ctx stays alive.
func ReconcileGaps(ctx context.Context, db *gorm.DB, chainID string, client gnoclient.Client, interval time.Duration, lookbackDays int) {
	reconcileOnce := func() {
		since := time.Now().UTC().AddDate(0, 0, -lookbackDays)
		missing, err := database.FindMissingHeights(db, chainID, since)
		if err != nil {
			log.Printf("[monitor][%s] reconcile: FindMissingHeights error: %v", chainID, err)
			return
		}
		if len(missing) == 0 {
			return
		}
		log.Printf("[monitor][%s] reconcile: found %d missing heights, backfilling", chainID, len(missing))
		if err := BackfillHeights(db, client, chainID, missing, GetMonikerMap(chainID)); err != nil {
			log.Printf("[monitor][%s] reconcile: backfill error: %v", chainID, err)
		}
	}

	go func() {
		if ctx.Err() != nil {
			return
		}
		reconcileOnce()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Printf("[monitor][%s] ReconcileGaps stopped", chainID)
				return
			case <-ticker.C:
				reconcileOnce()
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
		// Each chain checks on its own slot of the interval, so the per-chain
		// loops do not hit the database in lockstep. The slot is recomputed
		// every cycle to follow chains added or removed at runtime.
		log.Printf("[validator][%s] alert checks every %v at +%v", chainID, checkInterval,
			jobclock.AlertOffset(chainID, internal.EnabledChainsSnapshot(), checkInterval))
		nextCheck := func() <-chan time.Time {
			offset := jobclock.AlertOffset(chainID, internal.EnabledChainsSnapshot(), checkInterval)
			return time.After(jobclock.UntilNext(time.Now(), checkInterval, offset))
		}
		for {
			// During backfill, skip new-alert detection but still dispatch
			// pending RESOLVED alerts so the dedup window is not permanently
			// blocked by un-resolved WARNING/CRITICAL entries.
			if !isChainSynced(chainID) {
				SendResolveAlerts(db, chainID)
				select {
				case <-ctx.Done():
					return
				case <-nextCheck():
				}
				continue
			}

			runValidatorAlertCycle(db, chainID, GetThresholds())

			SendResolveAlerts(db, chainID)
			select {
			case <-ctx.Done():
				log.Printf("[monitor][%s] WatchValidatorAlerts stopped", chainID)
				return
			case <-nextCheck():
			}
		}
	}()
}

// runValidatorAlertCycle runs one missed-block alert check for chainID:
// it classifies each recent missed window, applies the resend dedup and the
// dead-validator silence, then dispatches and records the alerts that remain.
// t is snapshotted once per cycle by the caller, so the SQL filter and the
// level classification below always agree on the thresholds.
func runValidatorAlertCycle(db *gorm.DB, chainID string, t Thresholds) {
	today := time.Now().Format("2006-01-02")

	// Query returns one row per contiguous missed sequence within the
	// recent window. The window is kept short (30 min) to avoid
	// surfacing dozens of already-resolved historical sequences that
	// would each trigger a dedup DB round-trip. Active incidents are
	// always visible because new blocks are always within the window.
	// The NOT EXISTS filter on alert_logs skips sequences already
	// covered by a RESOLVED, eliminating the dedup check entirely for
	// those sequences.
	windows, err := database.GetMissedWindows(db, chainID, t.WarningThreshold)
	if err != nil {
		log.Printf("[validator][%s] error executing missed blocks query: %v", chainID, err)
		return
	}
	log.Printf("[validator][%s] alert check: found %d missed-block window(s) above threshold=%d",
		chainID, len(windows), t.WarningThreshold)

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

		// A sequence the validator already recovered from is recorded for the
		// score and the incident history, but never notified: a notification
		// now would only be followed by an immediate RESOLVED.
		resumeHeight, err := resumeHeightAfter(db, chainID, addr, end_height)
		if err != nil {
			log.Printf("[validator][%s] DB error checking participation: %v", chainID, err)
			continue
		}
		if resumeHeight > 0 {
			log.Printf("[validator][%s] closed: recording %s for %s (%s) without notification start=%d end=%d missed=%d (participated again at %d)",
				chainID, level, moniker, addr, start_height, end_height, missed, resumeHeight)
			if err := recordClosedIncident(db, chainID, addr, moniker, level, start_height, end_height); err != nil {
				log.Printf("[validator][%s] recording closed incident: %v", chainID, err)
			}
			continue
		}

		// Silence permanently dead validators: skip if no participation in the
		// last N days — but only for a REPEAT alert. A validator's very first
		// WARNING and first CRITICAL must always go through regardless of this
		// window, otherwise a validator that has never signed a single block
		// since joining the valset (no participation in "last N days" is then
		// trivially always true) would never generate a single alert, ever.
		if t.DeadValidatorSilenceDays > 0 {
			hasPrior, err := database.HasPriorAlert(db, chainID, addr, level)
			if err != nil {
				log.Printf("[validator][%s] DB error checking prior alerts: %v", chainID, err)
				continue
			}
			if hasPrior {
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
		}

		if err := getValidatorAlertDispatch()(chainID, missed, today, level, addr, moniker, start_height, end_height, db); err != nil {
			log.Printf("[validator][%s] SendAllValidatorAlerts error: %v", chainID, err)
		}
		if err := database.InsertAlertlog(db, chainID, addr, moniker, level, start_height, end_height, true, time.Now(), ""); err != nil {
			log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
		}
	}
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
	// DISTINCT ON and LIMIT pick the alerts first; the moniker is resolved
	// afterwards, once per returned validator rather than once per pending row.
	err := db.Raw(`
		SELECT p.addr,
		       `+database.MonikerSQL("p.chain_id", "p.addr", database.MonikerFallbackEmpty)+` AS moniker,
		       p.start_height,
		       p.end_height
		FROM (
			SELECT DISTINCT ON (al.addr)
			       al.chain_id, al.addr, al.start_height, al.end_height
			FROM alert_logs al
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
		) p
		ORDER BY p.addr
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
		resumeHeight, err := resumeHeightAfter(db, chainID, a.Addr, a.EndHeight)
		if err != nil {
			log.Printf("[validator][%s] DB error checking participation: %v", chainID, err)
			continue
		}
		if resumeHeight == 0 {
			log.Printf("[validator][%s] RESOLVED pending for %s (%s): no participation found after block %d yet",
				chainID, a.Moniker, a.Addr, a.EndHeight)
			continue
		}

		if err := internal.SendResolveValidator(chainID, a.Addr, a.Moniker, resumeHeight, db); err != nil {
			log.Printf("[validator][%s] SendResolveValidator error: %v", chainID, err)
		}
		// The RESOLVED ends where the sequence really ended, not where it had
		// reached when the alert was dispatched, so it covers the whole
		// sequence in GetMissedWindows and in the resend dedup.
		if err := database.InsertAlertlog(db, chainID, a.Addr, a.Moniker, "RESOLVED", a.StartHeight, resumeHeight-1, false, time.Now(), ""); err != nil {
			log.Printf("[monitor][%s] InsertAlertlog RESOLVED error: %v", chainID, err)
		}
	}
}

// recordClosedIncident writes the alert_logs rows of a missed sequence the
// validator already recovered from, without notifying anyone. Both rows are
// stored with skipped=false: nothing was dispatched, so neither the resend
// dedup, SendResolveAlerts nor HasPriorAlert treats them as notified. When an
// alert dispatched earlier still awaits its RESOLVED, SendResolveAlerts
// announces the recovery and its RESOLVED, stored at the real end of the
// sequence, covers this row too. Otherwise the row gets its own RESOLVED.
func recordClosedIncident(db *gorm.DB, chainID, addr, moniker, level string, startHeight, endHeight int64) error {
	var state struct {
		Recorded int64
		Pending  int64
	}
	err := db.Raw(`
		SELECT
			COUNT(*) FILTER (WHERE al.level = ? AND al.end_height >= ?) AS recorded,
			COUNT(*) FILTER (WHERE al.skipped = true AND NOT EXISTS (
				SELECT 1 FROM alert_logs r
				WHERE r.chain_id   = al.chain_id
				  AND r.addr       = al.addr
				  AND r.level      = 'RESOLVED'
				  AND r.end_height >= al.end_height
			)) AS pending
		FROM alert_logs al
		WHERE al.chain_id = ? AND al.addr = ?
		  AND al.level IN ('WARNING', 'CRITICAL')
	`, level, endHeight, chainID, addr).Scan(&state).Error
	if err != nil {
		return fmt.Errorf("checking existing alerts: %w", err)
	}
	if state.Recorded > 0 {
		// Already recorded by an earlier cycle whose RESOLVED is still to come.
		return nil
	}
	now := time.Now()
	if state.Pending > 0 {
		return database.InsertAlertlog(db, chainID, addr, moniker, level, startHeight, endHeight, false, now, "")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := database.InsertAlertlog(tx, chainID, addr, moniker, level, startHeight, endHeight, false, now, ""); err != nil {
			return err
		}
		// One microsecond later (the precision Postgres keeps) so that queries
		// picking a validator's latest row by sent_at see the RESOLVED last.
		return database.InsertAlertlog(tx, chainID, addr, moniker, "RESOLVED", startHeight, endHeight, false, now.Add(time.Microsecond), "")
	})
}

// validatorAlertDispatch notifies a WARNING/CRITICAL validator alert.
// Swappable via setValidatorAlertDispatch so tests observe dispatches without
// doing network I/O; guarded by a mutex like latencyDispatch.
type validatorAlertDispatchFunc func(chainID string, missed int, today, level, addr, moniker string, startHeight, endHeight int64, db *gorm.DB) error

var defaultValidatorAlertDispatch validatorAlertDispatchFunc = internal.SendAllValidatorAlerts

var (
	validatorAlertDispatchMu sync.RWMutex
	validatorAlertDispatch   = defaultValidatorAlertDispatch
)

func getValidatorAlertDispatch() validatorAlertDispatchFunc {
	validatorAlertDispatchMu.RLock()
	defer validatorAlertDispatchMu.RUnlock()
	return validatorAlertDispatch
}

func setValidatorAlertDispatch(fn validatorAlertDispatchFunc) {
	validatorAlertDispatchMu.Lock()
	defer validatorAlertDispatchMu.Unlock()
	validatorAlertDispatch = fn
}

// resumeHeightAfter returns the first height above afterHeight at which addr
// participated on chainID, or 0 when it has not participated again yet.
func resumeHeightAfter(db *gorm.DB, chainID, addr string, afterHeight int64) (int64, error) {
	var resumeHeight int64
	err := db.Raw(`
		SELECT COALESCE(MIN(block_height), 0)
		FROM daily_participations
		WHERE chain_id    = ?
		  AND addr        = ?
		  AND block_height > ?
		  AND participated = true
	`, chainID, addr, afterHeight).Scan(&resumeHeight).Error
	return resumeHeight, err
}

func SaveParticipation(db *gorm.DB, chainID string, blockHeight int64, participating map[string]Participation, monikerMap map[string]string, timeStp time.Time) error {
	rows := make([]dpRow, 0, len(monikerMap))
	for valAddr, moniker := range monikerMap {
		participated := participating[valAddr] // zero value => not participated

		if RecordActivationOrSkip(db, chainID, valAddr, blockHeight, participated.Participated) {
			continue
		}

		rows = append(rows, newDPRow(chainID, blockHeight, timeStp, moniker, valAddr, participated))
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

	rpcClient, ok := GetChainRPCClient(chainID)
	if !ok {
		// main.startChainMonitoring registers the pool before launching this
		// goroutine; a missing entry means a caller skipped that step.
		log.Printf("[monitor][%s] no RPC pool registered, monitoring not started", chainID)
		return
	}
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
	WatchLatencyAlerts(ctx, db, chainID)
	WatchBFTAlerts(ctx, db, chainID)
	ReconcileGaps(ctx, db, chainID, client, t.GapReconciliationInterval(), t.GapReconciliationLookbackDays)
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
