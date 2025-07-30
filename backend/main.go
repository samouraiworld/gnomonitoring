package main

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/api"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/scheduler"
)

var db *sql.DB

func main() {
	internal.LoadConfig()
	db = database.InitDB() // Init db

	go gnovalidator.StartValidatorMonitoring(db)
	go scheduler.InitScheduler(db) // for dailyreport

	webhooks, _ := database.Loadwebhooks(db)
	for _, wh := range webhooks {
		go internal.StartWebhookWatcher(wh, db) // for govdao
	}

	gnovalidator.Init() // registre les métriques
	gnovalidator.StartMetricsUpdater(db)
	go gnovalidator.StartPrometheusServer(internal.Config.MetricsPort)
	api.StartWebhookAPI(db)

	select {}
}
