package gnovalidator

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
)

// VARIABLE DECLARATION
var MonikerMutex sync.RWMutex
var lastRPCErrorAlert time.Time //anti spam for error RPC

type BlockParticipation struct {
	Height     int64
	Validators map[string]bool
}

var MonikerMap = make(map[string]string)

func CollectParticipation(db *sql.DB, client gnoclient.Client) {
	// Start the real-time tracking loop
	go func() {
		lastStored, err := GetLastStoredHeight(db)
		if err != nil {
			log.Printf("⚠️ Database empty get last block: %v", err)
			lastStored, err = client.LatestBlockHeight() // if db is empty begin with lastblock
		}

		currentHeight := lastStored + 1

		for {
			// Get value of the last block
			latest, err := client.LatestBlockHeight()

			if err != nil {
				log.Printf("Recovery error: height: %v", err)

				if time.Since(lastRPCErrorAlert) > 10*time.Minute {
					msg := fmt.Sprintf("⚠️ Error when querying latest block height: %v", err)
					msg += fmt.Sprintf("\nLast known block height: %d", currentHeight)

					lastRPCErrorAlert = time.Now()
				}
				//feat / add resolve Recovery Block height
				time.Sleep(10 * time.Second)
				continue
			}

			lastRPCErrorAlert = time.Time{}

			if latest <= currentHeight {
				time.Sleep(3 * time.Second)
				continue
			}

			log.Println("last block ", latest)
			// Load new blocks (if more than one at a time)

			for h := currentHeight + 1; h <= latest; h++ {

				block, err := client.Block(h)
				if err != nil || block.Block.LastCommit == nil {
					log.Printf("Erreur bloc %d: %v", h, err)
					continue
				}

				participating := make(map[string]bool)
				for _, precommit := range block.Block.LastCommit.Precommits {
					if precommit != nil {
						participating[precommit.ValidatorAddress.String()] = true
					}
				}
				// Inserts the participation for each validator into the database

				err = SaveParticipation(db, h, participating, MonikerMap)
				if err != nil {
					log.Printf("❌ Failed to save participation at height %d: %v", h, err)
				}

			}

			currentHeight = latest
		}
	}()

}
func WatchNewValidators(db *sql.DB, refreshInterval time.Duration) {
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

func WatchValidatorAlerts(db *sql.DB, checkInterval time.Duration) {
	go func() {
		for {
			today := time.Now().Format("2006-01-02")
			rows, err := db.Query(`
				SELECT addr, moniker, COUNT(*) 
				FROM daily_participation 
				WHERE date = ? AND participated = 0 
				GROUP BY addr, moniker
			`, today)
			if err != nil {
				log.Printf("❌ Error querying missed participation: %v", err)
				time.Sleep(checkInterval)
				continue
			}
			defer rows.Close()

			for rows.Next() {
				var addr, moniker string
				var missed int
				if err := rows.Scan(&addr, &moniker, &missed); err != nil {
					log.Printf("❌ Error scanning participation row: %v", err)
					continue
				}

				var level, emoji, prefix string
				if missed >= 3 {
					level = "CRITICAL"
					emoji = "🚨"
					prefix = "**"

				} else if missed == 1 {
					level = "WARNING"
					emoji = "⚠️"
					prefix = ""

				} else {
					continue
				}

				msg := fmt.Sprintf("%s %s %s %s \n addr:%s \n moniker: %s \n missed %d blocks today", emoji, prefix, level, prefix, addr, moniker, missed)
				log.Println(msg)
				internal.SendAllValidatorAlerts(msg, level, addr, moniker, db)
			}

			time.Sleep(checkInterval)
		}
	}()
}

// SaveParticipation records the participation of validators for a given block

func SaveParticipation(db *sql.DB, blockHeight int64, participating map[string]bool, monikerMap map[string]string) error {
	today := time.Now().Format("2006-01-02")

	for valAddr, moniker := range monikerMap {
		_, participated := participating[valAddr]
		stmt := `INSERT OR REPLACE INTO daily_participation(date, block_height, moniker, addr, participated) VALUES (?, ?, ?, ?, ?)`
		_, err := db.Exec(stmt, today, blockHeight, moniker, valAddr, participated)
		if err != nil {
			log.Printf("❌ Error saving participation for %s: %v", valAddr, err)
			return err // on retourne dès la première erreur rencontrée
		}
		log.Printf("✅ Saved participation for %s (%s) at height %d: %v", valAddr, moniker, blockHeight, participated)
	}
	return nil
}

func StartValidatorMonitoring(db *sql.DB) {
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
