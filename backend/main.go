package main

import (
	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/api"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/scheduler"
)

// var db *sql.DB

func main() {
	internal.LoadConfig()

	db, err := database.InitDB() // Init db
	println(db)
	println(err)

	go gnovalidator.StartValidatorMonitoring(db)
	go scheduler.InitScheduler(db) // for dailyreport

	// go govdao.StartGovDaoManager(db)

	gnovalidator.Init() // registre les métriques
	gnovalidator.StartMetricsUpdater(db)
	go gnovalidator.StartPrometheusServer(internal.Config.MetricsPort)
	api.StartWebhookAPI(db) //API

	select {}
}
