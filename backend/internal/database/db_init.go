package database

import (
	"fmt"
	"log"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type Govdao struct {
	Id     int    `gorm:"primaryKey;autoIncrement:false;column:id"`
	Url    string `gorm:"column:url;" `
	Title  string `gorm:"column:title;" `
	Tx     string `gorm:"column:tx;" `
	Status string `gorm:"column:status;" `
}
type Telegram struct {
	ChatID int64  `gorm:"primaryKey;column:chat_id;" `
	Type   string `gorm:"primaryKey;olumn:type;not null;check:type IN ('govdao','validator')" `
}
type TelegramHourReport struct {
	ChatID            int64  `gorm:"primaryKey;column:chat_id;" `
	DailyReportHour   int    `gorm:"column:daily_report_hour;default:9"`
	DailyReportMinute int    `gorm:"column:daily_report_minute;default:0"`
	Activate          bool   `gorm:"column:activate;default:true"`
	Timezone          string `gorm:"column:timezone;default:Europe/Paris" `
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
}
type WebhookValidator struct {
	ID          int       `gorm:"primaryKey;autoIncrement;column:id"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime" `
	Description string    `gorm:"column:description" `
	UserID      string    `gorm:"column:user_id;not null;index:idx_webhooks_validator_user" `
	URL         string    `gorm:"column:url;not null" `
	Type        string    `gorm:"column:type;not null;check:type IN ('discord','slack')" `
}
type DailyParticipation struct {
	Date           time.Time `gorm:"column:date"`
	BlockHeight    int64     `gorm:"column:block_height;uniqueIndex:uniq_addr_height,priority:2"`
	Moniker        string    `gorm:"column:moniker"`
	Addr           string    `gorm:"column:addr;not null;uniqueIndex:uniq_addr_height,priority:1"`
	Participated   bool      `gorm:"column:participated;not null"`
	TxContribution bool      `gorm:"column:tx_contribution;not null"`
}

type AlertLog struct {
	Addr        string    `gorm:"column:addr;primaryKey" `
	Moniker     string    `gorm:"column:moniker;not null" `
	Level       string    `gorm:"column:level;primaryKey" `
	StartHeight int64     `gorm:"column:start_height;primaryKey;not null" `
	EndHeight   int64     `gorm:"column:end_height;primaryKey;not null" `
	Skipped     bool      `gorm:"column:skipped;not null" `
	Msg         string    `gorm:"column:msg" `
	SentAt      time.Time `gorm:"column:sent_at;autoCreateTime" `
}

type AddrMoniker struct {
	Addr    string `gorm:"column:addr;primaryKey" `
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

// CReate index
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
	_, err = sqlDB.Exec("PRAGMA journal_mode = WAL;")
	if err != nil {
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	_, _ = sqlDB.Exec("PRAGMA synchronous = NORMAL;")
	_, _ = sqlDB.Exec("PRAGMA temp_store = MEMORY;")

	err = db.AutoMigrate(
		&User{}, &AlertContact{}, &WebhookValidator{},
		&WebhookGovDAO{}, &HourReport{},
		&DailyParticipation{}, &AlertLog{}, &AddrMoniker{}, &Govdao{}, &Telegram{}, &TelegramHourReport{},
	)
	if err != nil {
		return nil, err
	}

	CreateMissingBlocksView(db)

	return db, nil
}
