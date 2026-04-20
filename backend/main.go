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

// convertValidatorRates converts gnovalidator.ValidatorRate map values to
// their telegram mirror equivalents.
func convertValidatorRates(src map[string]gnovalidator.ValidatorRate) map[string]telegram.ValidatorRate {
	if src == nil {
		return nil
	}
	dst := make(map[string]telegram.ValidatorRate, len(src))
	for k, v := range src {
		dst[k] = telegram.ValidatorRate{Rate: v.Rate, Moniker: v.Moniker}
	}
	return dst
}


// startChainMonitoring launches monitoring goroutines for one chain and registers
// their shared cancel function in the chainmanager registry.
func startChainMonitoring(db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	log.Printf("[main] starting monitoring for chain %s", chainID)
	ctx, cancel := context.WithCancel(context.Background())
	chainmanager.Register(chainID, cancel)
	go gnovalidator.StartValidatorMonitoring(ctx, db, chainID, chainCfg)
	go govdao.StartGovDAo(ctx, db, chainID, chainCfg)
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

	// Wire Telegram send function to break gnovalidator → telegram import cycle.
	gnovalidator.SendTelegramMessage = telegram.SendMessageTelegram

	// Wire chain-health fetch and format functions to break the gnovalidator →
	// internal → telegram circular import. The two convert* helpers copy the
	// mirrored ValidatorRate maps between the two packages' local types.
	telegram.SetChainHealthFetcher(
		func(chainID string) telegram.ChainHealthSnapshot {
			snap := gnovalidator.FetchChainHealthSnapshot(db, chainID)
			tgSnap := telegram.ChainHealthSnapshot{
				LatestBlockHeight: snap.LatestBlockHeight,
				LatestBlockTime:   snap.LatestBlockTime,
				ConsensusRound:    snap.ConsensusRound,
				RPCReachable:      snap.RPCReachable,
				IsStuck:           snap.IsStuck,
				IsDisabled:        snap.IsDisabled,
				ValidatorRates:    convertValidatorRates(snap.ValidatorRates),
				MinBlock:          snap.MinBlock,
				MaxBlock:          snap.MaxBlock,
				MissedLast24h:     snap.MissedLast24h,
				PeerCount:         snap.PeerCount,
				MempoolTxCount:    snap.MempoolTxCount,
				MempoolTotalBytes: snap.MempoolTotalBytes,
			}
			if snap.PrecommitBitmap != nil {
				tgSnap.PrecommitBitmap = snap.PrecommitBitmap
			}
			for _, vi := range snap.ValidatorSet {
				tgSnap.ValidatorSet = append(tgSnap.ValidatorSet, telegram.ValidatorInfo{
					Address:     vi.Address,
					VotingPower: vi.VotingPower,
					KeepRunning: vi.KeepRunning,
					ServerType:  vi.ServerType,
				})
			}
			for _, vc := range snap.ValsetChanges {
				tgSnap.ValsetChanges = append(tgSnap.ValsetChanges, telegram.ValsetChange{
					BlockNum: vc.BlockNum,
					Address:  vc.Address,
					NewPower: vc.NewPower,
				})
			}
			return tgSnap
		},
		func(chainID string, snap telegram.ChainHealthSnapshot) string {
			return gnovalidator.FormatDisabledReport(chainID, gnovalidator.ChainHealthSnapshot{
				LatestBlockHeight: snap.LatestBlockHeight,
				LatestBlockTime:   snap.LatestBlockTime,
				IsDisabled:        snap.IsDisabled,
			})
		},
		func(chainID string, snap telegram.ChainHealthSnapshot) string {
			gnSnap := gnovalidator.ChainHealthSnapshot{
				LatestBlockHeight: snap.LatestBlockHeight,
				LatestBlockTime:   snap.LatestBlockTime,
				ConsensusRound:    snap.ConsensusRound,
				IsStuck:           snap.IsStuck,
				MissedLast24h:     snap.MissedLast24h,
				PeerCount:         snap.PeerCount,
				MempoolTxCount:    snap.MempoolTxCount,
				MempoolTotalBytes: snap.MempoolTotalBytes,
				PrecommitBitmap:   snap.PrecommitBitmap,
			}
			for _, vi := range snap.ValidatorSet {
				gnSnap.ValidatorSet = append(gnSnap.ValidatorSet, gnovalidator.ValidatorInfo{
					Address:     vi.Address,
					VotingPower: vi.VotingPower,
					KeepRunning: vi.KeepRunning,
					ServerType:  vi.ServerType,
				})
			}
			for _, vc := range snap.ValsetChanges {
				gnSnap.ValsetChanges = append(gnSnap.ValsetChanges, gnovalidator.ValsetChange{
					BlockNum: vc.BlockNum,
					Address:  vc.Address,
					NewPower: vc.NewPower,
				})
			}
			return gnovalidator.FormatStuckReport(chainID, gnSnap)
		},
	)
	telegram.MissedBlocksFormatter = gnovalidator.FormatMissedBlocksLast24hHTML

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

	telegram.SchedulerInstance = scheduler.Schedulerinstance
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
