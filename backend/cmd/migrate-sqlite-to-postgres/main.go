package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const copyBatchSize = 10_000
const insertBatchSize = 1_000

func main() {
	sqlitePath := flag.String("sqlite", "./db/webhooks.db", "path to source SQLite file")
	pgDSN := flag.String("postgres", "", "target Postgres DSN (required)")
	flag.Parse()

	if *pgDSN == "" {
		log.Fatal("missing -postgres flag, e.g. -postgres \"host=localhost port=5432 user=gnomonitoring password=... dbname=gnomonitoring sslmode=disable\"")
	}

	gormCfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Error)}

	log.Printf("opening SQLite source: %s", *sqlitePath)
	src, err := gorm.Open(sqlite.Open(*sqlitePath), gormCfg)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	srcSQL, err := src.DB()
	if err != nil {
		log.Fatalf("get sqlite sql.DB: %v", err)
	}
	srcSQL.SetMaxOpenConns(1) // SQLite single-writer

	log.Println("opening Postgres target...")
	dst, err := gorm.Open(postgres.Open(*pgDSN), gormCfg)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}

	// Step 1 — schema
	step("creating schema via AutoMigrate", func() error {
		return dst.AutoMigrate(
			&database.User{},
			&database.AlertContact{},
			&database.WebhookValidator{},
			&database.WebhookGovDAO{},
			&database.HourReport{},
			&database.DailyParticipation{},
			&database.DailyParticipationAgrega{},
			&database.AlertLog{},
			&database.AddrMoniker{},
			&database.Govdao{},
			&database.Telegram{},
			&database.TelegramHourReport{},
			&database.TelegramValidatorSub{},
			&database.AdminConfig{},
		)
	})
	step("ApplyMultiChainMigrations", func() error { return database.ApplyMultiChainMigrations(dst) })
	step("ApplyTelegramChainIDMigration", func() error { return database.ApplyTelegramChainIDMigration(dst) })

	// Step 2 — large table via COPY protocol (much faster than batched INSERTs)
	copyDailyParticipations(*pgDSN, src)

	// Step 3 — small tables via GORM batch insert + sequence reset
	copySmallTable[database.User](src, dst, "")
	copySmallTable[database.AlertContact](src, dst, "alert_contacts")
	copySmallTable[database.WebhookValidator](src, dst, "webhook_validators")
	copySmallTable[database.WebhookGovDAO](src, dst, "webhook_gov_daos")
	copySmallTable[database.HourReport](src, dst, "")
	copySmallTable[database.DailyParticipationAgrega](src, dst, "")
	copySmallTable[database.AlertLog](src, dst, "alert_logs")
	copySmallTable[database.AddrMoniker](src, dst, "addr_monikers")
	copySmallTable[database.Govdao](src, dst, "")
	copySmallTable[database.Telegram](src, dst, "")
	copySmallTable[database.TelegramHourReport](src, dst, "")
	copySmallTable[database.TelegramValidatorSub](src, dst, "telegram_validator_subs")
	copySmallTable[database.AdminConfig](src, dst, "")

	// Step 4 — indexes, view, seed (run after bulk load for speed)
	step("CreateOrReplaceIndexes", func() error { return database.CreateOrReplaceIndexes(dst) })
	step("CreateAggregaIndexes", func() error { return database.CreateAggregaIndexes(dst) })
	step("CreateMissingBlocksView", func() error { return database.CreateMissingBlocksView(dst) })
	step("SeedAdminConfig", func() error { return database.SeedAdminConfig(dst) })
	step("PopulateFirstActiveBlocks", func() error { return database.PopulateFirstActiveBlocks(dst) })
	step("CleanupSpuriousParticipations", func() error { return database.CleanupSpuriousParticipations(dst) })

	// Step 5 — verification
	printRowCounts(src, dst)

	log.Println("migration complete ✓")
}

// step runs fn, logging its name and elapsed time. Fatals on error.
func step(name string, fn func() error) {
	log.Printf("[%s] starting...", name)
	start := time.Now()
	if err := fn(); err != nil {
		log.Fatalf("[%s] failed: %v", name, err)
	}
	log.Printf("[%s] done in %s", name, time.Since(start).Round(time.Millisecond))
}

// resetSequence advances the Postgres serial sequence to MAX(id) so the next
// app-level INSERT (without explicit id) doesn't collide with migrated rows.
// table is a hardcoded constant from the call sites in main(), never user input.
func resetSequence(dst *gorm.DB, table string) error {
	return dst.Exec(fmt.Sprintf(
		`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM %s), 1))`,
		table, table,
	)).Error
}

// copySmallTable reads all rows of T from src and bulk-inserts them into dst.
// If seqTable is non-empty, the Postgres sequence for that table's id column is
// reset to MAX(id) after the insert.
func copySmallTable[T any](src, dst *gorm.DB, seqTable string) {
	var rows []T
	var zero T
	typeName := fmt.Sprintf("%T", zero)

	if err := src.Find(&rows).Error; err != nil {
		log.Fatalf("read %s from sqlite: %v", typeName, err)
	}
	if len(rows) == 0 {
		log.Printf("[%s] 0 rows — skipped", typeName)
		return
	}
	start := time.Now()
	if err := dst.CreateInBatches(&rows, insertBatchSize).Error; err != nil {
		log.Fatalf("write %s to postgres: %v", typeName, err)
	}
	log.Printf("[%s] %d rows copied in %s", typeName, len(rows), time.Since(start).Round(time.Millisecond))

	if seqTable != "" {
		if err := resetSequence(dst, seqTable); err != nil {
			log.Fatalf("resetSequence %s: %v", seqTable, err)
		}
		log.Printf("[%s] sequence reset on %s", typeName, seqTable)
	}
}

// sqliteDP maps the SQLite daily_participations schema.
// SQLite has no explicit `id` column — rows are identified by the implicit rowid.
// The Postgres target has `id BIGSERIAL` added in Phase 2 Pattern D; we omit it
// from the COPY column list and let Postgres assign it automatically.
type sqliteDP struct {
	RowID          int64     `gorm:"column:rowid"`
	Date           time.Time `gorm:"column:date"`
	BlockHeight    int64     `gorm:"column:block_height"`
	ChainID        string    `gorm:"column:chain_id"`
	Moniker        string    `gorm:"column:moniker"`
	Addr           string    `gorm:"column:addr"`
	Participated   bool      `gorm:"column:participated"`
	TxContribution bool      `gorm:"column:tx_contribution"`
}

func (sqliteDP) TableName() string { return "daily_participations" }

// copyDailyParticipations streams all rows from the SQLite daily_participations
// table into Postgres using the pgx COPY protocol. This is ~100× faster than
// batched INSERTs for millions of rows.
//
// Pagination uses the implicit SQLite rowid rather than OFFSET, which avoids a
// full-table scan on every page. The `id BIGSERIAL` Postgres column is excluded
// from the COPY column list so Postgres auto-assigns it; no sequence reset is
// needed afterwards.
func copyDailyParticipations(pgDSN string, src *gorm.DB) {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("pgxpool connect: %v", err)
	}
	defer pool.Close()

	cols := []string{"date", "block_height", "chain_id", "moniker", "addr", "participated", "tx_contribution"}

	log.Println("[daily_participations] starting COPY...")
	start := time.Now()
	var lastRowID int64
	var total int64

	for {
		var rows []sqliteDP
		err := src.Raw(
			`SELECT rowid, date, block_height, chain_id, moniker, addr, participated, tx_contribution
			 FROM daily_participations
			 WHERE rowid > ?
			 ORDER BY rowid ASC
			 LIMIT ?`,
			lastRowID, copyBatchSize,
		).Scan(&rows).Error
		if err != nil {
			log.Fatalf("[daily_participations] read from sqlite: %v", err)
		}
		if len(rows) == 0 {
			break
		}

		source := pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.Date, r.BlockHeight, r.ChainID, r.Moniker, r.Addr, r.Participated, r.TxContribution}, nil
		})
		n, err := pool.CopyFrom(ctx, pgx.Identifier{"daily_participations"}, cols, source)
		if err != nil {
			log.Fatalf("[daily_participations] COPY to postgres: %v", err)
		}

		total += n
		lastRowID = rows[len(rows)-1].RowID

		if total%500_000 == 0 {
			log.Printf("[daily_participations] %d rows copied (%.0f rows/s)...",
				total, float64(total)/time.Since(start).Seconds())
		}
	}

	log.Printf("[daily_participations] %d rows copied in %s (%.0f rows/s)",
		total, time.Since(start).Round(time.Second), float64(total)/time.Since(start).Seconds())
}

// printRowCounts compares row counts between SQLite source and Postgres target
// for the five tables that hold significant data.
func printRowCounts(src, dst *gorm.DB) {
	tables := []string{
		"daily_participations",
		"daily_participation_agregas",
		"alert_logs",
		"addr_monikers",
		"telegram_validator_subs",
	}

	log.Println("--- row count verification ---")
	log.Printf("%-35s %15s %15s %8s", "table", "sqlite", "postgres", "match")

	allOK := true
	for _, t := range tables {
		var srcCount, dstCount int64
		src.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s", t)).Scan(&srcCount)
		dst.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s", t)).Scan(&dstCount)
		match := "✓"
		if srcCount != dstCount {
			match = "✗ MISMATCH"
			allOK = false
		}
		log.Printf("%-35s %15d %15d %8s", t, srcCount, dstCount, match)
	}

	if !allOK {
		log.Fatal("row count mismatch — do NOT proceed with cutover")
	}
	log.Println("all row counts match ✓")
}
