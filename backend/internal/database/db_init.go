package database

import (
	"fmt"
	"log"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Govdao struct {
	Id      int    `gorm:"primaryKey;autoIncrement:false;column:id"`
	ChainID string `gorm:"column:chain_id;not null;default:betanet"`
	Url     string `gorm:"column:url;" `
	Title   string `gorm:"column:title;" `
	Tx      string `gorm:"column:tx;" `
	Status  string `gorm:"column:status;" `
}
type Telegram struct {
	ChatID  int64  `gorm:"primaryKey;column:chat_id;" `
	Type    string `gorm:"primaryKey;olumn:type;not null;check:type IN ('govdao','validator')" `
	ChainID string `gorm:"column:chain_id;not null;default:betanet"`
}
type TelegramHourReport struct {
	ChatID            int64  `gorm:"primaryKey;column:chat_id;" `
	ChainID           string `gorm:"primaryKey;column:chain_id;not null;default:betanet"`
	DailyReportHour   int    `gorm:"column:daily_report_hour;default:9"`
	DailyReportMinute int    `gorm:"column:daily_report_minute;default:0"`
	Activate          bool   `gorm:"column:activate;default:true"`
	Timezone          string `gorm:"column:timezone;default:Europe/Paris" `
}

type TelegramValidatorSub struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	ChatID    int64     `gorm:"index:idx_tvs_chain_addr_chatid,unique,priority:3;not null"`
	ChainID   string    `gorm:"column:chain_id;not null;default:betanet;index:idx_tvs_chain_addr_chatid,unique,priority:1"`
	Moniker   string    `gorm:"size:64;index"`
	Addr      string    `gorm:"index:idx_tvs_chain_addr_chatid,unique,priority:2;not null"`
	Activate  bool      `gorm:"default:true;index"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}
type ParticipationRate struct {
	Addr              string  `json:"addr"`
	Moniker           string  `json:"moniker"`
	ParticipationRate float64 `json:"participationRate"`
}

type User struct {
	UserID    string    `gorm:"primaryKey;column:user_id" json:"user_id"`
	Name      string    `gorm:"column:nameuser;not null" json:"name"`
	Email     string    `gorm:"uniqueIndex;not null" json:"email"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}
type HourReport struct {
	UserID            string `gorm:"primaryKey;column:user_id;not null" `
	DailyReportHour   int    `gorm:"column:daily_report_hour;default:9" json:"daily_report_hour"`
	DailyReportMinute int    `gorm:"column:daily_report_minute;default:0" json:"daily_report_minute"`
	Timezone          string `gorm:"column:timezone;default:Europe/Paris" `
}

type AlertContact struct {
	ID          int       `gorm:"primaryKey;autoIncrement;column:id"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UserID      string    `gorm:"column:user_id;not null" `
	Moniker     string    `gorm:"column:moniker;not null" `
	NameContact string    `gorm:"column:namecontact;not null" `
	MentionTag  string    `gorm:"column:mention_tag" `
	IDwebhook   int       `gorm:"column:id_webhook" `
}

type WebhookGovDAO struct {
	ID            int       `gorm:"primaryKey;autoIncrement;column:id" `
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" `
	Description   string    `gorm:"column:description" `
	UserID        string    `gorm:"column:user_id;not null;index:idx_webhooks_govdao_user" `
	URL           string    `gorm:"column:url;not null" `
	Type          string    `gorm:"column:type;not null;check:type IN ('discord','slack')" `
	LastCheckedID int       `gorm:"column:last_checked_id;not null;default:-1" `
	ChainID       *string   `gorm:"column:chain_id;default:null"`
}
type WebhookValidator struct {
	ID          int       `gorm:"primaryKey;autoIncrement;column:id"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime" `
	Description string    `gorm:"column:description" `
	UserID      string    `gorm:"column:user_id;not null;index:idx_webhooks_validator_user" `
	URL         string    `gorm:"column:url;not null" `
	Type        string    `gorm:"column:type;not null;check:type IN ('discord','slack')" `
	ChainID     *string   `gorm:"column:chain_id;default:null"`
}
type DailyParticipation struct {
	Date           time.Time `gorm:"column:date"`
	BlockHeight    int64     `gorm:"column:block_height;uniqueIndex:uniq_chain_addr_height,priority:3"`
	ChainID        string    `gorm:"column:chain_id;not null;default:betanet;uniqueIndex:uniq_chain_addr_height,priority:1"`
	Moniker        string    `gorm:"column:moniker"`
	Addr           string    `gorm:"column:addr;not null;uniqueIndex:uniq_chain_addr_height,priority:2"`
	Participated   bool      `gorm:"column:participated;not null"`
	TxContribution bool      `gorm:"column:tx_contribution;not null"`
}

type DailyParticipationAgrega struct {
	ChainID             string `gorm:"column:chain_id;not null;primaryKey"`
	Addr                string `gorm:"column:addr;not null;primaryKey"`
	BlockDate           string `gorm:"column:block_date;not null;primaryKey"` // DATE string YYYY-MM-DD
	Moniker             string `gorm:"column:moniker"`
	ParticipatedCount   int    `gorm:"column:participated_count;not null"`
	MissedCount         int    `gorm:"column:missed_count;not null"`
	TxContributionCount int    `gorm:"column:tx_contribution_count;not null"`
	TotalBlocks         int    `gorm:"column:total_blocks;not null"`
	FirstBlockHeight    int64  `gorm:"column:first_block_height;not null"`
	LastBlockHeight     int64  `gorm:"column:last_block_height;not null"`
}

type AlertLog struct {
	ID          uint      `gorm:"primaryKey;autoIncrement;column:id"`
	ChainID     string    `gorm:"column:chain_id;not null;default:betanet;index:idx_al_chain_addr,priority:1"`
	Addr        string    `gorm:"column:addr;not null;index:idx_al_chain_addr,priority:2"`
	Moniker     string    `gorm:"column:moniker;not null" `
	Level       string    `gorm:"column:level;not null" `
	StartHeight int64     `gorm:"column:start_height;not null" `
	EndHeight   int64     `gorm:"column:end_height;not null" `
	Skipped     bool      `gorm:"column:skipped;not null" `
	Msg         string    `gorm:"column:msg" `
	SentAt      time.Time `gorm:"column:sent_at;autoCreateTime" `
}

type AddrMoniker struct {
	ID      uint   `gorm:"primaryKey;autoIncrement;column:id"`
	ChainID string `gorm:"column:chain_id;not null;default:betanet;uniqueIndex:uniq_chain_addr,priority:1"`
	Addr    string `gorm:"column:addr;not null;uniqueIndex:uniq_chain_addr,priority:2"`
	Moniker string `gorm:"column:moniker;not null" `
}
type AlertSummary struct {
	Moniker     string    `json:"moniker"`
	Addr        string    `json:"addr"`
	Level       string    `json:"level"`
	StartHeight int64     `json:"startHeight"`
	EndHeight   int64     `json:"endHeight"`
	Msg         string    `json:"msg"`
	SentAt      time.Time `json:"sentAt"`
}
type UptimeMetrics struct {
	Moniker string  `json:"moniker"`
	Addr    string  `json:"addr"`
	Uptime  float64 `json:"uptime"`
}
type OperationTimeMetrics struct {
	Moniker      string  `json:"moniker"`
	Addr         string  `json:"addr"`
	LastDownDate string  `json:"lastDownDate"`
	LastUpDate   string  `json:"lastUpDate"`
	DaysDiff     float64 `json:"operationTime"`
}
type TxContribMetrics struct {
	Moniker   string  ` json:"moniker"`
	Addr      string  `json:"addr"`
	TxContrib float64 `json:"txContrib"`
}
type MissingBlockMetrics struct {
	Moniker      string ` json:"moniker"`
	Addr         string `json:"addr"`
	MissingBlock int    `json:"missingBlock"`
}
type FirstSeenMetrics struct {
	Addr      string `json:"addr"`
	Moniker   string `json:"moniker"`
	FirstSeen string `json:"firstSeen"`
}

// ApplyMultiChainMigrations adds chain_id columns to existing tables when upgrading
// from a single-chain schema. It is idempotent: it checks for column existence first.
func ApplyMultiChainMigrations(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("ApplyMultiChainMigrations: get sql.DB: %w", err)
	}

	// Check whether chain_id already exists in daily_participations.
	var count int
	row := sqlDB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('daily_participations') WHERE name='chain_id'`)
	if err := row.Scan(&count); err != nil {
		return fmt.Errorf("ApplyMultiChainMigrations: pragma check: %w", err)
	}

	// If chain_id already present, migrations have already been applied.
	if count > 0 {
		return nil
	}

	type alteration struct {
		table string
		stmt  string
	}
	alterations := []alteration{
		{"daily_participations", "ALTER TABLE daily_participations ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet'"},
		{"alert_logs", "ALTER TABLE alert_logs ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet'"},
		{"addr_monikers", "ALTER TABLE addr_monikers ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet'"},
		{"govdao", "ALTER TABLE govdao ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet'"},
		{"webhooks_validators", "ALTER TABLE webhooks_validators ADD COLUMN chain_id TEXT DEFAULT NULL"},
		{"webhooks_gov_d_a_os", "ALTER TABLE webhooks_gov_d_a_os ADD COLUMN chain_id TEXT DEFAULT NULL"},
		{"telegram_validator_subs", "ALTER TABLE telegram_validator_subs ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet'"},
	}

	for _, a := range alterations {
		if _, err := sqlDB.Exec(a.stmt); err != nil {
			return fmt.Errorf("ApplyMultiChainMigrations: alter %s: %w", a.table, err)
		}
	}

	// telegram_hour_reports cannot gain a new PRIMARY KEY column via ALTER TABLE in
	// SQLite, so we recreate the table and preserve existing rows.
	recreateStmts := []string{
		`CREATE TABLE IF NOT EXISTS telegram_hour_reports_new (
			chat_id              INTEGER NOT NULL,
			chain_id             TEXT    NOT NULL DEFAULT 'betanet',
			daily_report_hour    INTEGER NOT NULL DEFAULT 9,
			daily_report_minute  INTEGER NOT NULL DEFAULT 0,
			activate             INTEGER NOT NULL DEFAULT 1,
			timezone             TEXT    NOT NULL DEFAULT 'Europe/Paris',
			PRIMARY KEY (chat_id, chain_id)
		)`,
		`INSERT INTO telegram_hour_reports_new (chat_id, chain_id, daily_report_hour, daily_report_minute, activate, timezone)
			SELECT chat_id, 'betanet', daily_report_hour, daily_report_minute, activate, timezone
			FROM telegram_hour_reports`,
		`DROP TABLE telegram_hour_reports`,
		`ALTER TABLE telegram_hour_reports_new RENAME TO telegram_hour_reports`,
	}

	for _, stmt := range recreateStmts {
		if _, err := sqlDB.Exec(stmt); err != nil {
			return fmt.Errorf("ApplyMultiChainMigrations: recreate telegram_hour_reports: %w", err)
		}
	}

	return nil
}

// ApplyTelegramChainIDMigration adds chain_id column to the telegrams table.
// It is idempotent: it checks for column existence first.
func ApplyTelegramChainIDMigration(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("ApplyTelegramChainIDMigration: get sql.DB: %w", err)
	}
	var count int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('telegrams') WHERE name='chain_id'`,
	).Scan(&count); err != nil {
		return fmt.Errorf("ApplyTelegramChainIDMigration: pragma check: %w", err)
	}
	if count > 0 {
		return nil
	}
	if _, err := sqlDB.Exec(
		`ALTER TABLE telegrams ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet'`,
	); err != nil {
		return fmt.Errorf("ApplyTelegramChainIDMigration: alter: %w", err)
	}
	return nil
}

// CreateOrReplaceIndexes drops legacy single-chain indexes and creates new
// compound (chain_id, …) indexes suited for multi-chain queries.
func CreateOrReplaceIndexes(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("CreateOrReplaceIndexes: get sql.DB: %w", err)
	}

	drops := []string{
		"DROP INDEX IF EXISTS idx_dp_addr",
		"DROP INDEX IF EXISTS idx_dp_block_height",
		"DROP INDEX IF EXISTS idx_dp_date",
		"DROP INDEX IF EXISTS idx_dp_addr_participated",
		"DROP INDEX IF EXISTS idx_dp_addr_date",
		"DROP INDEX IF EXISTS idx_tvs_addr_chatid",
	}
	for _, stmt := range drops {
		if _, err := sqlDB.Exec(stmt); err != nil {
			return fmt.Errorf("CreateOrReplaceIndexes: drop: %w", err)
		}
	}

	creates := []string{
		"CREATE INDEX IF NOT EXISTS idx_dp_chain_block_height ON daily_participations(chain_id, block_height)",
		"CREATE INDEX IF NOT EXISTS idx_dp_chain_addr ON daily_participations(chain_id, addr)",
		"CREATE INDEX IF NOT EXISTS idx_dp_chain_date ON daily_participations(chain_id, date)",
		"CREATE INDEX IF NOT EXISTS idx_dp_chain_addr_participated ON daily_participations(chain_id, addr, participated)",
		"CREATE INDEX IF NOT EXISTS idx_al_chain_addr ON alert_logs(chain_id, addr)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_tvs_chain_addr_chatid ON telegram_validator_subs(chain_id, addr, chat_id)",
		// Covering index for consecutive-missed and uptime queries (block_height range + addr grouping)
		"CREATE INDEX IF NOT EXISTS idx_dp_chain_addr_blockheight ON daily_participations(chain_id, addr, block_height)",
		// Index for date-range queries with addr grouping (TxContrib, MissingBlock, ParticipationRate)
		"CREATE INDEX IF NOT EXISTS idx_dp_chain_date_addr ON daily_participations(chain_id, date, addr)",
		// Index for alert_logs sent_at ordering (used by GetActiveAlertCount CTE)
		"CREATE INDEX IF NOT EXISTS idx_al_chain_addr_sentat ON alert_logs(chain_id, addr, sent_at)",
	}
	for _, stmt := range creates {
		if _, err := sqlDB.Exec(stmt); err != nil {
			return fmt.Errorf("CreateOrReplaceIndexes: create: %w", err)
		}
	}

	return nil
}

// CreateAggregaIndexes creates covering indexes on daily_participation_agrega
// to speed up date-range and per-validator queries. Idempotent.
func CreateAggregaIndexes(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("CreateAggregaIndexes: get sql.DB: %w", err)
	}

	creates := []string{
		"CREATE INDEX IF NOT EXISTS idx_dpa_chain_date      ON daily_participation_agregas(chain_id, block_date)",
		"CREATE INDEX IF NOT EXISTS idx_dpa_chain_addr_date ON daily_participation_agregas(chain_id, addr, block_date)",
	}
	for _, stmt := range creates {
		if _, err := sqlDB.Exec(stmt); err != nil {
			return fmt.Errorf("CreateAggregaIndexes: create: %w", err)
		}
	}

	return nil
}

// InitDB opens the SQLite database, enables performance pragmas, runs
// AutoMigrate, applies multi-chain schema migrations, rebuilds indexes and
// creates the missing-blocks view.
func InitDB(dbPath string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		log.Fatalf("DB opening error: %v", err)
	}

	// Activate WAL mode
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite allows only one writer at a time. Limiting the pool to a single
	// connection serialises all access and prevents "database is locked" errors
	// that arise when multiple goroutines (realtime loop, aggregator, Prometheus
	// updater, etc.) hold separate connections and race on the write lock.
	sqlDB.SetMaxOpenConns(1)

	_, err = sqlDB.Exec("PRAGMA journal_mode = WAL;")
	if err != nil {
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	_, _ = sqlDB.Exec("PRAGMA synchronous = NORMAL;")
	_, _ = sqlDB.Exec("PRAGMA temp_store = MEMORY;")
	_, _ = sqlDB.Exec("PRAGMA cache_size = -64000;")   // 64 MB page cache
	_, _ = sqlDB.Exec("PRAGMA mmap_size = 268435456;") // 256 MB memory-mapped I/O
	_, _ = sqlDB.Exec("PRAGMA busy_timeout = 5000;")   // 5s retry on SQLITE_BUSY

	err = db.AutoMigrate(
		&User{}, &AlertContact{}, &WebhookValidator{},
		&WebhookGovDAO{}, &HourReport{},
		&DailyParticipation{}, &DailyParticipationAgrega{}, &AlertLog{}, &AddrMoniker{}, &Govdao{}, &Telegram{}, &TelegramHourReport{}, &TelegramValidatorSub{},
	)
	if err != nil {
		return nil, err
	}

	if err := ApplyMultiChainMigrations(db); err != nil {
		return nil, fmt.Errorf("ApplyMultiChainMigrations: %w", err)
	}

	if err := ApplyTelegramChainIDMigration(db); err != nil {
		return nil, fmt.Errorf("ApplyTelegramChainIDMigration: %w", err)
	}

	if err := CreateOrReplaceIndexes(db); err != nil {
		return nil, fmt.Errorf("CreateOrReplaceIndexes: %w", err)
	}

	if err := CreateAggregaIndexes(db); err != nil {
		return nil, fmt.Errorf("CreateAggregaIndexes: %w", err)
	}

	if err := CreateMissingBlocksView(db); err != nil {
		return nil, fmt.Errorf("CreateMissingBlocksView: %w", err)
	}

	return db, nil
}
