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

var MonikerMutex sync.RWMutex
var lastRPCErrorAlert time.Time //anti spam for error RPC
var (
	lastProgressHeight int64 = -1
	lastProgressTime         = time.Now()
	alertSent          bool
	restoredNotified   bool
)

type BlockParticipation struct {
	Height     int64
	Validators map[string]bool
}

var MonikerMap = make(map[string]string)

type Participation struct {
	Participated   bool
	Timestamp      time.Time
	TxContribution bool
}

func CollectParticipation(db *gorm.DB, client gnoclient.Client) {
	// simulateCount := 0
	// simulateMax := 4   // for test
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("🔥 Panic recovered in CollectParticipation: %v", r)
			}
		}()

		lastStored, err := GetLastStoredHeight(db)

		println("return lastStored:", lastStored)
		if lastStored == 0 {
			log.Printf("⚠️ Database empty get last block: %v", err)
			lastStored = 0
			// lastStored, err = client.LatestBlockHeight()
			if err != nil {
				log.Printf("❌ Failed to get latest block height: %v", err)
				return
			}
		}

		currentHeight := lastStored + 1
		println(currentHeight)
		for {

			latest, err := client.LatestBlockHeight()
			if err != nil {
				log.Printf("Error retrieving last block: %v", err)

				if time.Since(lastRPCErrorAlert) > 10*time.Minute {
					msg := fmt.Sprintf("⚠️ Error when querying latest block height: %v", err)
					msg += fmt.Sprintf("\nLast known block height: %d", currentHeight)
					log.Println(msg)
					lastRPCErrorAlert = time.Now()
				}
				time.Sleep(10 * time.Second)
				continue
			}
			// Stagnation detection
			if lastProgressHeight != -1 && latest == lastProgressHeight {
				if !alertSent && time.Since(lastProgressTime) > 2*time.Minute {

					blockTime, err := database.GetTimeOfBlock(db, latest)
					if err != nil {
						log.Printf("⚠️ Impossible de récupérer la date du block %d: %v", latest, err)
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

					send_at, err := database.GetTimeOfAlert(db, latest)
					if err != nil {
						log.Printf("⚠️ Impossible de récupérer la date du block %d: %v", latest, err)
						return
					}
					if send_at.IsZero() {

						internal.SendInfoValidator(msg, "CRITICAL", db)
						database.InsertAlertlog(db, "all", "all", "CRITICAL", latest, latest, false, time.Now(), msg)
					}

					alertSent = true
					restoredNotified = false
					lastProgressTime = time.Now()
				}
			} else {
				lastProgressHeight = latest
				lastProgressTime = time.Now()

				if alertSent && !restoredNotified {
					msg := "✅ Activity Restored: Gno.land is back to normal."
					internal.SendInfoValidator(msg, "INFO", db)
					database.InsertAlertlog(db, "all", "all", "RESOLVED", latest, latest, false, time.Now(), msg)
					restoredNotified = true
					alertSent = false
				}
			}

			lastRPCErrorAlert = time.Time{}

			if latest <= currentHeight {
				time.Sleep(3 * time.Second)
				continue
			}
			// *** RATTRAPAGE BLOQUANT SI GROS RETARD ***
			if latest-currentHeight > 500 {
				// on rattrape jusqu’à latest-200 pour laisser un tampon
				// (évite la course avec le flux temps réel ensuite)
				stop := latest - 200
				if stop < currentHeight {
					stop = latest // au pire, rattrape tout
				}
				log.Printf("⏳ Backfill [%d..%d] (gap=%d)", currentHeight, stop, latest-currentHeight)
				if err := BackfillParallel(db, client, currentHeight, stop, MonikerMap); err != nil {
					log.Printf("❌ backfill error: %v", err)
					// si backfill échoue, on ne bloque pas indéfiniment
				} else {
					// on saute directement à la fin du backfill
					currentHeight = stop + 1
					log.Printf("✅ Backfill done up to %d, switch to realtime", stop)
				}
				// on ne passe pas au “temps réel” tant que l’écart reste gros
				continue
			}
			// log.Println("last block ", latest)

			for h := currentHeight; h <= latest; h++ {
				block, err := client.Block(h)
				if err != nil || block == nil || block.Block == nil || block.Block.LastCommit == nil {
					log.Printf("Erreur bloc %d: %v", h, err)
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

				err = SaveParticipation(db, h, participating, MonikerMap, timeStp)
				if err != nil {
					log.Printf("❌ Failed to save participation at height %d: %v", h, err)
				}
			}

			currentHeight = latest
		}
	}()
}

func WatchNewValidators(db *gorm.DB, refreshInterval time.Duration) {
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		for range ticker.C {
			log.Println("🔁 Refresh MonikerMap...")

			//Copy old map
			oldMap := make(map[string]string)
			MonikerMutex.RLock()
			for k, v := range MonikerMap {
				oldMap[k] = v
			}
			MonikerMutex.RUnlock()

			// Refresh MonikerMap
			InitMonikerMap(db)

			// Compare with the old Monikermap
			MonikerMutex.RLock()
			for addr, moniker := range MonikerMap {
				if _, exists := oldMap[addr]; !exists {
					msg := fmt.Sprintf("✅ **New Validator detected**: %s (%s)", moniker, addr)
					log.Println(msg)
					internal.SendInfoValidator(msg, "info", db)
				}
			}
			MonikerMutex.RUnlock()
		}
	}()
}

func WatchValidatorAlerts(db *gorm.DB, checkInterval time.Duration) {
	go func() {
		for {
			today := time.Now().Format("2006-01-02")

			rows, err := db.Raw(`
				SELECT addr,moniker,start_height,end_height,missed FROM daily_missing_series`).Rows()
			if err != nil {
				log.Printf("❌ Error executing query: %v", err)
				time.Sleep(checkInterval)
				continue
			}

			for rows.Next() {
				var addr, moniker string
				var missed int
				var start_height, end_height int64

				if err := rows.Scan(&addr, &moniker, &start_height, &end_height, &missed); err != nil {
					log.Printf("❌ Error scanning row: %v", err)
					continue
				}

				var level string
				switch {
				case missed >= 30:
					level = "CRITICAL"

				case missed == 5:
					level = "WARNING"

				default:
					continue
				}
				// 2. Check if alert was recently sent
				var count int64
				err := db.Raw(`
						SELECT COUNT(*) FROM alert_logs 
						WHERE addr = ? AND level = ? 
						AND start_height= ? 
						AND skipped = 1	
						`, addr, level, start_height).Scan(&count).Error

				if err != nil {
					log.Printf("❌ DB error checking alert_logs: %v", err)

					continue
				}

				if count > 0 {
					// log.Printf("⏱️ Skipping alert for %s (%s, %s): already sent", moniker)
					database.InsertAlertlog(db, addr, moniker, level, start_height, end_height, false, time.Now(), "")

					continue
				}

				// 2. Check if this start_height is already covered by another alert range
				var countint int64
				err = db.Raw(`
						SELECT COUNT(*) 
						FROM alert_logs
						WHERE addr = ?
						AND level IN ('CRITICAL')
						AND ? BETWEEN start_height AND end_height
						`, addr, start_height).Scan(&countint).Error
				if err != nil {
					log.Printf("❌ DB error checking alert_logs: %v", err)

					continue
				}
				if countint > 0 {
					// log.Printf("⏱️ Skipping alert for %s (%s, %s): already sent", moniker)
					database.InsertAlertlog(db, addr, moniker, level, start_height, end_height, false, time.Now(), "")

					continue
				}

				// 3 check if addr is mute

				var mute int
				err = db.Raw(`
					SELECT COUNT(*) FROM alert_logs
       				WHERE addr = ? AND level = "MUTED"
       				AND strftime('%s',sent_at) >= strftime('%s','now','-60 minutes');

						`, addr).Scan(&mute).Error

				if err != nil {
					log.Printf("❌ DB error checking alert_logs: %v", err)

					continue
				}
				if mute >= 1 {
					// Activer un mute 1h
					log.Printf("🚫 Too many alerte for %s, muting for 1h", moniker)

					database.InsertAlertlog(db, addr, moniker, level, start_height, end_height, true, time.Now(), "")

					continue
				}

				internal.SendAllValidatorAlerts(missed, today, level, addr, moniker, start_height, end_height, db)
				database.InsertAlertlog(db, addr, moniker, level, start_height, end_height, true, time.Now(), "")

			}

			rows.Close()
			SendResolveAlerts(db)
			time.Sleep(checkInterval)
		}
	}()
}
func SendResolveAlerts(db *gorm.DB) {
	log.Println("==========================Start resolv Alert==========00==")

	type LastAlert struct {
		Addr        string
		Moniker     string
		StartHeight int64
		EndHeight   int64
	}

	var alerts []LastAlert

	// err := db.Raw(`
	// 	SELECT addr, moniker, max(end_height) as end_height ,start_height
	// 	FROM daily_missing_series
	// 	where missed >=5
	// 	group by start_height

	// `).Scan(&alerts).Error
	err := db.Raw(`
			SELECT addr, moniker, max(end_height) as end_height ,max(start_height)
	FROM daily_missing_series
		where missed >=5
		group by addr

	`).Scan(&alerts).Error
	if err != nil {
		log.Printf("❌ Error fetching last alerts: %v", err)
		return
	}
	for _, a := range alerts {
		// Check if alert send
		var count int64
		err := db.Raw(`
		SELECT COUNT(*) FROM alert_logs
		WHERE addr = ? and level = "RESOLVED"
		AND  end_height = ?
		`, a.Addr, a.EndHeight).Scan(&count).Error

		if err != nil {
			log.Printf("❌ DB error checking alert_logs: %v", err)
			continue
		}

		if count > 0 {
			log.Printf("⏱️ Skipping resolve alert for %s : already sent", a.Moniker)
			continue
		}
		// Backoff/mute mechanism for repeated resolves
		var recentResolves int64
		err = db.Raw(`
        SELECT COUNT(*) FROM alert_logs
        WHERE addr = ? AND level = "RESOLVED"
       AND strftime('%s',sent_at) >= strftime('%s','now','-60 minutes');
    `, a.Addr).Scan(&recentResolves).Error
		if err != nil {
			log.Printf("❌ DB error checking recent resolves: %v", err)
			continue
		}
		if recentResolves >= 4 {
			// Activer un mute d'1h
			log.Printf("🚫 Too many resolves for %s, muting for 1h", a.Moniker)
			database.InsertAlertlog(db, a.Addr, a.Moniker, "MUTED", a.StartHeight, a.EndHeight, false, time.Now(), "")
			continue
		}

		// check if participation is true after end_heigt+1
		var countparticipated int
		err = db.Raw(`
			SELECT participated FROM daily_participations
			WHERE addr = ? AND block_height= (?+1)
			`, a.Addr, a.EndHeight).Scan(&countparticipated).Error
		if err != nil {
			log.Printf("❌ DB error checking count participated: %v", err)
			continue
		}
		if countparticipated == 0 {
			// log.Printf("Not resolve error")
			continue
		}
		resolveMsg := fmt.Sprintf("✅ RESOLVED: No more missed blocks for %s (%s) at Block %d ", a.Moniker, a.Addr, a.EndHeight+1)
		internal.SendResolveValidator(resolveMsg, a.Addr, db)

		database.InsertAlertlog(db, a.Addr, a.Moniker, "RESOLVED", a.StartHeight, a.EndHeight, false, time.Now(), "")

	}

}

func SaveParticipation(db *gorm.DB, blockHeight int64, participating map[string]Participation, monikerMap map[string]string, timeStp time.Time) error {
	// today := time.Now().UTC().Format("2006-01-02 15:04:05")

	tx := db.Begin()
	if tx.Error != nil {
		log.Printf("❌ Error starting transaction: %v", tx.Error)
		return tx.Error
	}

	stmt := `
		INSERT OR REPLACE INTO daily_participations
		(date, block_height, moniker, addr, participated,tx_contribution)
		VALUES (?, ?, ?, ?, ?,?)
	`

	for valAddr, moniker := range monikerMap {
		participated := participating[valAddr] // false if not find

		if err := tx.Exec(stmt, timeStp, blockHeight, moniker, valAddr, participated.Participated, participated.TxContribution).Error; err != nil {
			log.Printf("❌ Error saving participation for %s: %v", valAddr, err)
			tx.Rollback()
			return err
		}

		log.Printf("✅ Saved participation for %s (%s) at height %d: %v", valAddr, moniker, blockHeight, participated)
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("❌ Commit error: %v", err)
		return err
	}

	return nil
}

func StartValidatorMonitoring(db *gorm.DB) {
	rpcClient, err := rpcclient.NewHTTPClient(internal.Config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}
	client := gnoclient.Client{RPCClient: rpcClient}

	InitMonikerMap(db) // init validator Map
	WatchNewValidators(db, 5*time.Minute)
	CollectParticipation(db, client)         // collect participant
	WatchValidatorAlerts(db, 20*time.Second) // DB-based of alerts

}
