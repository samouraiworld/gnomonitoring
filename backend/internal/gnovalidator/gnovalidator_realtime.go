package gnovalidator

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
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

// timeMu protects lastRPCErrorAlert and lastProgressTime since time.Time is not
// atomic-safe and must be guarded by a mutex.
var timeMu sync.Mutex
var lastRPCErrorAlert time.Time // anti spam for error RPC
var lastProgressTime = time.Now()

// lastProgressHeight[chainID] = block height
var lastProgressHeight = make(map[string]int64)
var heightMutex sync.RWMutex

// alertSent[chainID][addr] = bool
var alertSent = make(map[string]map[string]bool)
var alertMutex sync.RWMutex

// restoredNotified[chainID][addr] = bool
var restoredNotified = make(map[string]map[string]bool)
var restoreMutex sync.RWMutex

type BlockParticipation struct {
	Height     int64
	Validators map[string]bool
}

type Participation struct {
	Participated   bool
	Timestamp      time.Time
	TxContribution bool
}

func CollectParticipation(db *gorm.DB, chainID string, client gnoclient.Client) {
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
				sinceRPCErr := time.Since(lastRPCErrorAlert)
				timeMu.Unlock()
				if sinceRPCErr > 10*time.Minute {
					msg := fmt.Sprintf("⚠️ Error when querying latest block height: %v", err)
					msg += fmt.Sprintf("\nLast known block height: %d", currentHeight)
					log.Println(msg)
					timeMu.Lock()
					lastRPCErrorAlert = time.Now()
					timeMu.Unlock()
				}
				time.Sleep(10 * time.Second)
				continue
			}
			// Stagnation detection
			lph := GetLastHeight(chainID)
			timeMu.Lock()
			lpt := lastProgressTime
			timeMu.Unlock()
			if lph != 0 && latest == lph {
				if !IsAlertSent(chainID, "all") && time.Since(lpt) > 2*time.Minute {

					blockTime, err := database.GetTimeOfBlock(db, chainID, latest)
					if err != nil {
						log.Printf("[monitor][%s] cannot get block time for height %d: %v", chainID, latest, err)
						return
					}
					elapsed := time.Since(blockTime).Truncate(time.Second)

					msg := fmt.Sprintf(
						"🚨 CRITICAL : Blockchain stuck at height %d since %s (%s ago)",
						latest,
						blockTime.Format(time.RFC822),
						elapsed,
					)

					log.Println(msg)

					send_at, err := database.GetTimeOfAlert(db, chainID, latest)
					if err != nil {
						log.Printf("[monitor][%s] cannot get block time for height %d: %v", chainID, latest, err)
						return
					}
					if send_at.IsZero() {

						if err := internal.SendInfoValidator(chainID, msg, "CRITICAL", db); err != nil {
							log.Printf("[monitor][%s] SendInfoValidator error: %v", chainID, err)
						}
						if err := database.InsertAlertlog(db, chainID, "all", "all", "CRITICAL", latest, latest, false, time.Now(), msg); err != nil {
							log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
						}
					}

					SetAlertSent(chainID, "all", true)
					SetRestoredNotified(chainID, "all", false)
					timeMu.Lock()
					lastProgressTime = time.Now()
					timeMu.Unlock()
				}
			} else {
				SetLastHeight(chainID, latest)
				timeMu.Lock()
				lastProgressTime = time.Now()
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
			lastRPCErrorAlert = time.Time{}
			timeMu.Unlock()

			if latest <= currentHeight {
				time.Sleep(3 * time.Second)
				continue
			}
			// *** BLOCKING BACKFILL IF LARGE GAP ***
			if latest-currentHeight > 500 {
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
				// do not switch to “realtime” while the gap is still large
				continue
			}
			// log.Println("last block ", latest)

			for h := currentHeight; h <= latest; h++ {
				block, err := client.Block(h)
				if err != nil || block == nil || block.Block == nil || block.Block.LastCommit == nil {
					log.Printf("[monitor][%s] error fetching block %d: %v", chainID, h, err)
					continue
				}

				// ================================ Get Participation and date ==================== //

				// == IF in json return section Data, have a tx and get proposer of tx
				var txProposer string
				if len(block.Block.Data.Txs) > 0 {
					txProposer = block.Block.Header.ProposerAddress.String()

				}
				// === Get Timestamp ==

				timeStp := block.Block.Header.Time

				// log.Printf("Block %v prop: %s", h, txProposer)

				participating := make(map[string]Participation)
				for _, precommit := range block.Block.LastCommit.Precommits {
					if precommit != nil {
						var tx bool

						if precommit.ValidatorAddress.String() == txProposer {
							tx = true
						} else {
							tx = false
						}

						participating[precommit.ValidatorAddress.String()] = Participation{
							Participated:   true,
							Timestamp:      timeStp,
							TxContribution: tx,
						}

					}
				}
				// log.Printf("participating = %+v \n", participating)

				err = SaveParticipation(db, chainID, h, participating, GetMonikerMap(chainID), timeStp)
				if err != nil {
					log.Printf("[monitor][%s] failed to save participation at height %d: %v", chainID, h, err)
				}
			}

			currentHeight = latest
		}
	}()
}

func WatchNewValidators(db *gorm.DB, chainID string, client gnoclient.Client, rpcEndpoint string, refreshInterval time.Duration) {
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		for range ticker.C {

			// Copy old map
			oldMap := GetMonikerMap(chainID)

			// Refresh MonikerMap
			InitMonikerMap(db, chainID, client, rpcEndpoint)

			// Compare with the old Monikermap
			for addr, moniker := range GetMonikerMap(chainID) {
				if _, exists := oldMap[addr]; !exists {
					msg := fmt.Sprintf("[%s] ✅ **New Validator detected**: %s (%s)", chainID, moniker, addr)
					log.Println(msg)
					if err := internal.SendInfoValidator(chainID, msg, "info", db); err != nil {
						log.Printf("[monitor][%s] SendInfoValidator error: %v", chainID, err)
					}
				}
			}
		}
	}()
}

func WatchValidatorAlerts(db *gorm.DB, chainID string, checkInterval time.Duration) {
	type missedWindow struct {
		Addr        string
		Moniker     string
		StartHeight int64
		EndHeight   int64
		Missed      int
	}

	go func() {
		for {
			today := time.Now().Format("2006-01-02")

			var windows []missedWindow
			err := db.Raw(`
				WITH ranked AS (
					SELECT
						addr,
						moniker,
						date,
						block_height,
						participated,
						CASE
							WHEN participated = 0 AND LAG(participated) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) = 1
							THEN 1
							WHEN participated = 0 AND LAG(participated) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) IS NULL
							THEN 1
							ELSE 0
						END AS new_seq
					FROM daily_participations
					WHERE chain_id = ? AND date >= datetime('now', '-2 hours')
				),
				grouped AS (
					SELECT
						*,
						SUM(new_seq) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) AS seq_id
					FROM ranked
				)
				SELECT
					addr,
					moniker,
					MIN(block_height) OVER (PARTITION BY addr, moniker, DATE(date), seq_id) AS start_height,
					block_height AS end_height,
					SUM(1) OVER (
						PARTITION BY addr, moniker, DATE(date), seq_id
						ORDER BY block_height
						ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
					) AS missed
				FROM grouped
				WHERE participated = 0
				ORDER BY addr, moniker, date, seq_id, block_height
			`, chainID).Scan(&windows).Error
			if err != nil {
				log.Printf("[validator][%s] error executing missed blocks query: %v", chainID, err)
				time.Sleep(checkInterval)
				continue
			}

			for _, w := range windows {
				addr := w.Addr
				moniker := w.Moniker
				start_height := w.StartHeight
				end_height := w.EndHeight
				missed := w.Missed

				var level string
				switch {
				case missed >= 30:
					level = "CRITICAL"

				case missed >= 5:
					level = "WARNING"

				default:
					continue
				}
				// 2. Check if alert was recently sent
				var count int64
				err := db.Raw(`
						SELECT COUNT(*) FROM alert_logs
						WHERE chain_id = ? AND addr = ? AND level = ?
						AND start_height= ?
						AND skipped = 1
						`, chainID, addr, level, start_height).Scan(&count).Error

				if err != nil {
					log.Printf("[validator][%s] DB error checking alert_logs: %v", chainID, err)

					continue
				}

				if count > 0 {
					// log.Printf("⏱️ Skipping alert for %s (%s, %s): already sent", moniker)
					if err := database.InsertAlertlog(db, chainID, addr, moniker, level, start_height, end_height, false, time.Now(), ""); err != nil {
						log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
					}

					continue
				}

				// 2. Check if this start_height is already covered by another alert range
				var countint int64
				err = db.Raw(`
						SELECT COUNT(*)
						FROM alert_logs
						WHERE chain_id = ? AND addr = ?
						AND level IN ('CRITICAL')
						AND ? BETWEEN start_height AND end_height
						`, chainID, addr, start_height).Scan(&countint).Error
				if err != nil {
					log.Printf("[validator][%s] DB error checking alert_logs: %v", chainID, err)

					continue
				}
				if countint > 0 {
					// log.Printf("⏱️ Skipping alert for %s (%s, %s): already sent", moniker)
					if err := database.InsertAlertlog(db, chainID, addr, moniker, level, start_height, end_height, false, time.Now(), ""); err != nil {
						log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
					}

					continue
				}

				// 3 check if addr is mute

				var mute int
				err = db.Raw(`
					SELECT COUNT(*) FROM alert_logs
       				WHERE chain_id = ? AND addr = ? AND level = "MUTED"
       				AND strftime('%s',sent_at) >= strftime('%s','now','-60 minutes');

						`, chainID, addr).Scan(&mute).Error

				if err != nil {
					log.Printf("[validator][%s] DB error checking alert_logs: %v", chainID, err)

					continue
				}
				if mute >= 1 {
					// Enable 1h mute
					log.Printf("[validator][%s] muting %s for 1h — too many alerts", chainID, moniker)

					if err := database.InsertAlertlog(db, chainID, addr, moniker, level, start_height, end_height, true, time.Now(), ""); err != nil {
						log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
					}

					continue
				}

				if err := internal.SendAllValidatorAlerts(chainID, missed, today, level, addr, moniker, start_height, end_height, db); err != nil {
					log.Printf("[validator][%s] SendAllValidatorAlerts error: %v", chainID, err)
				}
				if err := database.InsertAlertlog(db, chainID, addr, moniker, level, start_height, end_height, true, time.Now(), ""); err != nil {
					log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
				}

			}

			SendResolveAlerts(db, chainID)
			time.Sleep(checkInterval)
		}
	}()
}
func SendResolveAlerts(db *gorm.DB, chainID string) {
	type LastAlert struct {
		Addr        string
		Moniker     string
		StartHeight int64
		EndHeight   int64
	}

	var alerts []LastAlert

	err := db.Raw(`
		WITH ranked AS (
			SELECT
				addr,
				moniker,
				date,
				block_height,
				participated,
				CASE
					WHEN participated = 0 AND LAG(participated) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) = 1
					THEN 1
					WHEN participated = 0 AND LAG(participated) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) IS NULL
					THEN 1
					ELSE 0
				END AS new_seq
			FROM daily_participations
			WHERE chain_id = ? AND date >= datetime('now', '-2 hours')
		),
		grouped AS (
			SELECT
				*,
				SUM(new_seq) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) AS seq_id
			FROM ranked
		),
		series AS (
			SELECT
				addr,
				moniker,
				MIN(block_height) OVER (PARTITION BY addr, moniker, DATE(date), seq_id) AS start_height,
				block_height AS end_height,
				SUM(1) OVER (
					PARTITION BY addr, moniker, DATE(date), seq_id
					ORDER BY block_height
					ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
				) AS missed
			FROM grouped
			WHERE participated = 0
		)
		SELECT addr, moniker, max(end_height) AS end_height, max(start_height) AS start_height
		FROM series
		WHERE missed >= 5
		GROUP BY addr
	`, chainID).Scan(&alerts).Error
	if err != nil {
		log.Printf("[validator][%s] error fetching last alerts: %v", chainID, err)
		return
	}
	for _, a := range alerts {
		// Check if alert send
		var count int64
		err := db.Raw(`
		SELECT COUNT(*) FROM alert_logs
		WHERE chain_id = ? AND addr = ? and level = "RESOLVED"
		AND  end_height = ?
		`, chainID, a.Addr, a.EndHeight).Scan(&count).Error

		if err != nil {
			log.Printf("[validator][%s] DB error checking alert_logs: %v", chainID, err)
			continue
		}

		if count > 0 {
			continue
		}
		// Backoff/mute mechanism for repeated resolves
		var recentResolves int64
		err = db.Raw(`
        SELECT COUNT(*) FROM alert_logs
        WHERE chain_id = ? AND addr = ? AND level = "RESOLVED"
       AND strftime('%s',sent_at) >= strftime('%s','now','-60 minutes');
    `, chainID, a.Addr).Scan(&recentResolves).Error
		if err != nil {
			log.Printf("[validator][%s] DB error checking recent resolves: %v", chainID, err)
			continue
		}
		if recentResolves >= 4 {
			// Enable 1h mute
			log.Printf("[validator][%s] muting %s for 1h — too many resolves", chainID, a.Moniker)
			if err := database.InsertAlertlog(db, chainID, a.Addr, a.Moniker, "MUTED", a.StartHeight, a.EndHeight, false, time.Now(), ""); err != nil {
				log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
			}
			continue
		}

		// check if participation is true after end_heigt+1
		var countparticipated int
		err = db.Raw(`
			SELECT participated FROM daily_participations
			WHERE chain_id = ? AND addr = ? AND block_height= (?+1)
			`, chainID, a.Addr, a.EndHeight).Scan(&countparticipated).Error
		if err != nil {
			log.Printf("[validator][%s] DB error checking participated: %v", chainID, err)
			continue
		}
		if countparticipated == 0 {
			// log.Printf("Not resolve error")
			continue
		}
		resolveMsg := fmt.Sprintf("[%s] ✅ RESOLVED: No more missed blocks for %s (%s) at Block %d", chainID, a.Moniker, a.Addr, a.EndHeight+1)
		if err := internal.SendResolveValidator(chainID, resolveMsg, a.Addr, db); err != nil {
			log.Printf("[validator][%s] SendResolveValidator error: %v", chainID, err)
		}

		if err := database.InsertAlertlog(db, chainID, a.Addr, a.Moniker, "RESOLVED", a.StartHeight, a.EndHeight, false, time.Now(), ""); err != nil {
			log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
		}

	}

}

func SaveParticipation(db *gorm.DB, chainID string, blockHeight int64, participating map[string]Participation, monikerMap map[string]string, timeStp time.Time) error {
	// today := time.Now().UTC().Format("2006-01-02 15:04:05")

	tx := db.Begin()
	if tx.Error != nil {
		log.Printf("[monitor][%s] error starting transaction: %v", chainID, tx.Error)
		return tx.Error
	}

	stmt := `
		INSERT OR REPLACE INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated,tx_contribution)
		VALUES (?, ?, ?, ?, ?, ?,?)
	`

	for valAddr, moniker := range monikerMap {
		participated := participating[valAddr] // false if not found

		if participated.Participated {
			// Dynamic detection: record first_active_block when first seen
			if GetFirstActiveBlock(chainID, valAddr) == -1 {
				SetFirstActiveBlock(chainID, valAddr, blockHeight)
				_ = database.UpsertFirstActiveBlock(tx, chainID, valAddr, blockHeight)
			}
		} else {
			// Guard: skip rows before the validator's activation block
			if fab := GetFirstActiveBlock(chainID, valAddr); fab > 0 && blockHeight < fab {
				continue
			}
		}

		if err := tx.Exec(stmt, chainID, timeStp, blockHeight, moniker, valAddr, participated.Participated, participated.TxContribution).Error; err != nil {
			log.Printf("[monitor][%s] error saving participation for %s: %v", chainID, valAddr, err)
			tx.Rollback()
			return err
		}

	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("[monitor][%s] commit error: %v", chainID, err)
		return err
	}

	if blockHeight%100 == 0 {
		log.Printf("[monitor][%s] synced block %d", chainID, blockHeight)
	}
	return nil
}

func StartValidatorMonitoring(db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	rpcClient, err := rpcclient.NewHTTPClient(chainCfg.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}
	client := gnoclient.Client{RPCClient: rpcClient}

	InitMonikerMap(db, chainID, client, chainCfg.RPCEndpoint) // init validator Map
	WatchNewValidators(db, chainID, client, chainCfg.RPCEndpoint, 5*time.Minute)
	CollectParticipation(db, chainID, client)         // collect participant
	WatchValidatorAlerts(db, chainID, 20*time.Second) // DB-based of alerts

}

// Moniker helpers

func GetMonikerMap(chainID string) map[string]string {
	MonikerMutex.RLock()
	defer MonikerMutex.RUnlock()
	if m, ok := MonikerMap[chainID]; ok {
		return m
	}
	return make(map[string]string)
}

func SetMoniker(chainID, addr, moniker string) {
	MonikerMutex.Lock()
	defer MonikerMutex.Unlock()
	if _, ok := MonikerMap[chainID]; !ok {
		MonikerMap[chainID] = make(map[string]string)
	}
	MonikerMap[chainID][addr] = moniker
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
