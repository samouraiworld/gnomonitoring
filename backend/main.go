package main

import (
	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/api"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/scheduler"
)

// var db *sql.DB

func main() {
	internal.LoadConfig()

	db, err := database.InitDB() // Init db
	println(db)
	println(err)
	// database.InsertAddrMoniker(db, "g1sgr3gnr7uy5hvpqfvyvh0ld09waj86xja844lg", "Samourai")
	// database.InsertAddrMoniker(db, "g1mxguhd5zacar64txhfm0v7hhtph5wur5hx86vs", "devX Val1")
	// database.InsertAddrMoniker(db, "g1t9ctfa468hn6czff8kazw08crazehcxaqa2uaa", "devX Val2")
	// database.InsertAddrMoniker(db, "g15zkeyz2gwrjluqj6eremllrh6nx7mt4tlz8f32", "Onbloc 1")
	// database.InsertAddrMoniker(db, "g1v7wl7qlakzku5mrafmgntfuvd7xjrluhuhwewp", "Onbloc 2")
	// database.InsertAddrMoniker(db, "g14anvsqpkgz6mlvrnjpc4x6mc9ncz7dh3vlmruk", "Berty")

	// go gnovalidator.StartValidatorMonitoring(db) // gnovalidator realtime
	go scheduler.InitScheduler(db) // for dailyreport

	// go govdao.StartGovDaoManager(db)

	// gnovalidator.Init()                  // init metrics prometheus
	// gnovalidator.StartMetricsUpdater(db) // update metrics prometheus / 5 min
	// go gnovalidator.StartPrometheusServer(internal.Config.MetricsPort)

	// go func() {
	// 	for {
	// 		internal.SendResolveAlerts(db)
	// 		time.Sleep(1 * time.Minute) // check if alert  resolve
	// 	}
	// }()
	api.StartWebhookAPI(db) //API
	select {}
}
