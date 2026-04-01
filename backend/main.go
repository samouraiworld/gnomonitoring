package main

import (
	"context"
	"log"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/api"
	"github.com/samouraiworld/gnomonitoring/backend/internal/chainmanager"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/govdao"
	"github.com/samouraiworld/gnomonitoring/backend/internal/scheduler"
	"github.com/samouraiworld/gnomonitoring/backend/internal/telegram"
	"gorm.io/gorm"
)

// startChainMonitoring launches monitoring goroutines for one chain and registers
// their shared cancel function in the chainmanager registry.
func startChainMonitoring(db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	log.Printf("[main] starting monitoring for chain %s", chainID)
	ctx, cancel := context.WithCancel(context.Background())
	chainmanager.Register(chainID, cancel)
	go gnovalidator.StartValidatorMonitoring(ctx, db, chainID, chainCfg)
	go govdao.StartGovDAo(ctx, db, chainID, chainCfg.GraphqlEndpoint, chainCfg.RPCEndpoint, chainCfg.GnowebEndpoint)
}

func main() {
	internal.LoadConfig()
	// ========================Init Flags ==================== //
	internal.InitFlags()

	// ======================== Init DB ==================== //

	db, err := database.InitDB("./db/webhooks.db")
	if err != nil {
		log.Fatalf("[main] failed to initialize database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("[main] failed to get underlying SQL DB: %v", err)
	}

	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("[main] database not reachable: %v", err)
	}

	log.Printf("[main] database ready")

	// ==================== Load admin thresholds from DB ============ //
	gnovalidator.LoadThresholds(db)

	// ==================== Per-Chain Monitoring Loops =============== //
	log.Printf("[main] enabled chains (%d): %v", len(internal.EnabledChains), internal.EnabledChains)

	for _, chainID := range internal.EnabledChains {
		chainCfg, err := internal.Config.GetChainConfig(chainID)
		if err != nil {
			log.Printf("[main] skipping chain %s: %v", chainID, err)
			continue
		}
		go startChainMonitoring(db, chainID, chainCfg)
	}

	// ==================== Scheduler for hour report =============================== //

	if !*internal.DisableReport {
		go scheduler.InitScheduler(db)
	} else {
		log.Printf("[main] daily report scheduler disabled")
	}

	// ====================== Gov Dao Proposal ====================================== //

	go govdao.StartProposalWatcher(db)

	// ======================= Telegram bot validator ========================= //
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handlers := telegram.BuildTelegramHandlers(internal.Config.TokenTelegramValidator, db, internal.Config.DefaultChain, internal.EnabledChains)
	callbackHandler := telegram.BuildTelegramCallbackHandler(internal.Config.TokenTelegramValidator, db, internal.Config.DefaultChain)

	go func() {
		if err := telegram.StartCommandLoop(ctx, internal.Config.TokenTelegramValidator, handlers, callbackHandler, "validator", db, internal.Config.DefaultChain); err != nil {
			log.Fatalf("[main] validator bot command loop failed: %v", err)
		}
	}()

	// ======================= Telegram govdao bot ====================================== //
	ctxgovdao, cancelgovdao := context.WithCancel(context.Background())
	defer cancelgovdao()

	handlersgovdao := telegram.BuildTelegramGovdaoHandlers(
		internal.Config.TokenTelegramGovdao,
		db,
		internal.Config.DefaultChain,
		internal.EnabledChains,
	)

	go func() {
		if err := telegram.StartCommandLoop(ctxgovdao, internal.Config.TokenTelegramGovdao, handlersgovdao, nil, "govdao", db, internal.Config.DefaultChain); err != nil {
			log.Fatalf("[main] govdao bot command loop failed: %v", err)
		}
	}()

	// ====================== Metrics for prometheus =============================== //

	gnovalidator.Init()                  // init metrics prometheus
	gnovalidator.StartMetricsUpdater(db) // update metrics prometheus / 5 min
	go gnovalidator.StartPrometheusServer(internal.Config.MetricsPort)

	// ====================== Aggregator (daily_participation_agregas) ============= //
	gnovalidator.StartAggregator(db) // aggregate past days + prune raw rows every hour

	// ====================== Run API ============================================== //

	api.StartWebhookAPI(db) // API
	select {}
}
