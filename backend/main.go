package main

import (
	"context"
	"log"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/api"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/govdao"
	"github.com/samouraiworld/gnomonitoring/backend/internal/scheduler"
	"github.com/samouraiworld/gnomonitoring/backend/internal/telegram"
)

func main() {
	internal.LoadConfig()
	//========================Init Flags ==================== //
	internal.InitFlags()

	//======================== Init DB ==================== //

	db, err := database.InitDB("./db/webhooks.db")
	if err != nil {
		log.Fatalf("❌ Failed to initialize database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("❌ Failed to get underlying SQL DB: %v", err)
	}

	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("❌ Database is not reachable: %v", err)
	}

	log.Println("✅ Database connection established successfully")

	// ==================== Parse and save pareticipation and tx contribution =============== //

	go gnovalidator.StartValidatorMonitoring(db) // gnovalidator realtime

	// ==================== Scheduler for hour report =============================== //

	if !*internal.DisableReport {
		go scheduler.InitScheduler(db)
	} else {
		log.Println("⚠️ Daily report scheduler disabled by flag")
	}

	// ====================== Gov Dao Proposal ====================================== //

	go govdao.StartGovDAo(db)
	go govdao.StartProposalWatcher(db)

	//======================= Telegram bot validator
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handlers := telegram.BuildTelegramHandlers(internal.Config.TokenTelegramValidator, db)

	go func() {
		if err := telegram.StartCommandLoop(ctx, internal.Config.TokenTelegramValidator, handlers, "validator", db); err != nil {
			log.Fatalf("command loop error bot validator : %v", err)
		}
	}()

	// ======================= Telegram govdao bot ======================================
	ctxgovdao, cancelgovdao := context.WithCancel(context.Background())
	defer cancelgovdao()

	handlersgovdao := telegram.BuildTelegramGovdaoHandlers(internal.Config.TokenTelegramGovdao, db)

	go func() {
		if err := telegram.StartCommandLoop(ctxgovdao, internal.Config.TokenTelegramGovdao, handlersgovdao, "govdao", db); err != nil {
			log.Fatalf("command loop error bot govdao: %v", err)
		}
	}()

	// ====================== Metrics for prometheus =============================== //

	gnovalidator.Init()                  // init metrics prometheus
	gnovalidator.StartMetricsUpdater(db) // update metrics prometheus / 5 min
	go gnovalidator.StartPrometheusServer(internal.Config.MetricsPort)

	// ====================== Run API ============================================== //

	api.StartWebhookAPI(db) //API
	select {}
}
