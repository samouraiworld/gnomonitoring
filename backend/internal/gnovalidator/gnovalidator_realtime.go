package gnovalidator

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"gorm.io/gorm"
)

// VARIABLE DECLARATION
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

func CollectParticipation(db *gorm.DB, client gnoclient.Client) {

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
			lastStored, err = client.LatestBlockHeight()
			if err != nil {
				log.Printf("❌ Failed to get latest block height: %v", err)
				return
			}
		}

		currentHeight := lastStored + 1

		for {
			latest, err := client.LatestBlockHeight()
			if err != nil {
				log.Printf("Erreur lors de la récupération du dernier bloc : %v", err)

				if time.Since(lastRPCErrorAlert) > 10*time.Minute {
					msg := fmt.Sprintf("⚠️ Error when querying latest block height: %v", err)
					msg += fmt.Sprintf("\nLast known block height: %d", currentHeight)
					log.Println(msg)
					lastRPCErrorAlert = time.Now()
				}
				time.Sleep(10 * time.Second)
				continue
			}
			// Détection de stagnation
			if lastProgressHeight != -1 && latest == lastProgressHeight {
				if !alertSent && time.Since(lastProgressTime) > 2*time.Minute {
					msg := fmt.Sprintf("⚠️ Blockchain stuck at height %d since %s (%s ago)", latest, lastProgressTime.Format(time.RFC822), time.Since(lastProgressTime).Truncate(time.Second))
					log.Println(msg)
					internal.SendAllValidatorAlerts(msg, "", "", "", db)

					alertSent = true
					restoredNotified = false
					lastProgressTime = time.Now()
				}
			} else {
				lastProgressHeight = latest
				lastProgressTime = time.Now()

				if alertSent && !restoredNotified {
					internal.SendAllValidatorAlerts("✅ **Activity Restored**: Gnoland is back to normal.", "", "", "", db)
					restoredNotified = true
					alertSent = false
				}
			}

			lastRPCErrorAlert = time.Time{}

			if latest <= currentHeight {
				time.Sleep(3 * time.Second)
				continue
			}

			log.Println("last block ", latest)

			for h := currentHeight + 1; h <= latest; h++ {
				block, err := client.Block(h)
				if err != nil || block == nil || block.Block == nil || block.Block.LastCommit == nil {
					log.Printf("Erreur bloc %d: %v", h, err)
					continue
				}

				participating := make(map[string]bool)
				for _, precommit := range block.Block.LastCommit.Precommits {
					if precommit != nil {
						participating[precommit.ValidatorAddress.String()] = true
					}
				}

				err = SaveParticipation(db, h, participating, MonikerMap)
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
			InitMonikerMap()

			// Compare with the old Monikermap
			MonikerMutex.RLock()
			for addr, moniker := range MonikerMap {
				if _, exists := oldMap[addr]; !exists {
					msg := fmt.Sprintf("✅ **New Validator detected**: %s (%s)", moniker, addr)
					log.Println(msg)
					internal.SendAllValidatorAlerts(msg, "info", addr, moniker, db)
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
				SELECT addr, moniker, COUNT(*) 
				FROM daily_participations 
				WHERE date = ? AND participated = 0 
				GROUP BY addr, moniker
			`, today).Rows()
			if err != nil {
				log.Printf("❌ Error executing query: %v", err)
				time.Sleep(checkInterval)
				continue
			}

			for rows.Next() {
				var addr, moniker string
				var missed int

				if err := rows.Scan(&addr, &moniker, &missed); err != nil {
					log.Printf("❌ Error scanning row: %v", err)
					continue
				}

				var level, emoji, prefix string
				switch {
				case missed >= 3:
					level = "CRITICAL"
					emoji = "🚨"
					prefix = "**"
				case missed == 1:
					level = "WARNING"
					emoji = "⚠️"
					prefix = ""
				default:
					continue
				}

				msg := fmt.Sprintf(
					"%s %s%s%s\naddr: %s\nmoniker: %s\nmissed %d blocks today",
					emoji, prefix, level, prefix, addr, moniker, missed,
				)
				log.Println(msg)

				internal.SendAllValidatorAlerts(msg, level, addr, moniker, db)
			}

			rows.Close() // On ferme explicitement ici, pas avec defer

			time.Sleep(checkInterval)
		}
	}()
}

func SaveParticipation(db *gorm.DB, blockHeight int64, participating map[string]bool, monikerMap map[string]string) error {
	today := time.Now().Format("2006-01-02")

	tx := db.Begin()
	if tx.Error != nil {
		log.Printf("❌ Error starting transaction: %v", tx.Error)
		return tx.Error
	}

	stmt := `
		INSERT OR REPLACE INTO daily_participations
		(date, block_height, moniker, addr, participated)
		VALUES (?, ?, ?, ?, ?)
	`

	for valAddr, moniker := range monikerMap {
		participated := participating[valAddr] // false si non trouvé

		if err := tx.Exec(stmt, today, blockHeight, moniker, valAddr, participated).Error; err != nil {
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

	InitMonikerMap() // init validator Map
	WatchNewValidators(db, 5*time.Minute)
	CollectParticipation(db, client)         // collect participant
	WatchValidatorAlerts(db, 20*time.Second) // DB-based of alerts
}
