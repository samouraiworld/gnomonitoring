package main

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
)

var db *sql.DB

func main() {
	internal.LoadConfig()
	db = internal.InitDB()
	internal.StartWebhookAPI(db)

	go gnovalidator.StartValidatorMonitoring(db)

	ticker := time.NewTicker(time.Duration(internal.Config.IntervallSecond) * time.Second)
	defer ticker.Stop()

	webhooks, _ := internal.Loadwebhooks(db)
	for _, wh := range webhooks {
		go internal.StartWebhookWatcher(wh, db)
	}
	select {}
}
