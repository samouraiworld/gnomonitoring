package main

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
)

var db *sql.DB

func main() {
	internal.LoadConfig()
	db = internal.InitDB()

	go gnovalidator.StartValidatorMonitoring(db)
	go gnovalidator.StartDailyReport(db)

	webhooks, _ := internal.Loadwebhooks(db)
	for _, wh := range webhooks {
		go internal.StartWebhookWatcher(wh, db)
	}

	gnovalidator.Init() // registre les métriques
	gnovalidator.StartMetricsUpdater(db)
	go gnovalidator.StartPrometheusServer(internal.Config.MetricsPort)
	internal.StartWebhookAPI(db)

	select {}
}
