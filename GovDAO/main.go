package main

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3" // ← Import anonyme indispensable

	"github.com/samouraiworld/gnomonitoring/GovDao/internal"
)

var db *sql.DB

func main() {
	internal.LoadConfig()
	db = internal.InitDB()
	internal.StartWebhookAPI(db)
	// lastChecked := internal.Config.LastCheckedID //init valor
	ticker := time.NewTicker(time.Duration(internal.Config.IntervallSecond) * time.Second)
	defer ticker.Stop()

	webhooks, _ := internal.Loadwebhooks(db)
	for _, wh := range webhooks {
		go internal.StartWebhookWatcher(wh, db)
	}
	select {}
}
