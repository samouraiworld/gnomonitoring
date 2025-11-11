package database

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ValidatorStatus struct {
	Moniker string
	Addr    string
	Status  string
}

// ============================ Telegram =============================================

func InsertChatID(db *gorm.DB, chatID int64, chatType string) (bool, error) {
	chat := Telegram{
		ChatID: chatID,
		Type:   chatType,
	}

	if chatType == "validator" {
		createHourReportTelegram(db, chatID)

	}

	tx := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "chat_id"}, {Name: "type"}},
		DoNothing: true,
	}).Create(&chat)

	if tx.Error != nil {
		return false, tx.Error
	}
	inserted := tx.RowsAffected > 0
	return inserted, nil

}

// If error during send delete chatid
func DeleteChatByID(db *gorm.DB, chatID int64) error {
	return db.Where("chat_id = ?", chatID).Delete(&Telegram{}).Error
}
func GetAllChatIDs(db *gorm.DB, TypeChatid string) ([]int64, error) {
	var chats []Telegram

	if err := db.Where("type = ?", TypeChatid).Find(&chats).Error; err != nil {
		return nil, err
	}

	var ids []int64
	for _, c := range chats {
		ids = append(ids, c.ChatID)
	}
	return ids, nil
}

// ============================ Telegram validato =============================================
// ================== Telegram hours report ================================

func UpdateTelegramHeureReport(db *gorm.DB, H, M int, T string, chatid int64) error {
	// Validate timezone
	if _, err := time.LoadLocation(T); err != nil {
		log.Printf("Invalid timezone '%s', defaulting to UTC", T)
		T = "UTC"
	}
	return db.
		Model(&TelegramHourReport{}).
		Where("chat_id = ?", chatid).
		Updates(map[string]interface{}{
			"daily_report_hour":   H,
			"daily_report_minute": M,
			"timezone":            T,
		}).Error
}

func ActivateTelegramReport(db *gorm.DB, IsActivate bool, chatid int64) error {
	// Validate timezone

	return db.
		Model(&TelegramHourReport{}).
		Where("chat_id = ?", chatid).
		Updates(map[string]interface{}{
			"activate": IsActivate,
		}).Error
}

func GetTelegramReportStatus(db *gorm.DB, chatID int64) (bool, error) {
	var activate bool
	err := db.Model(&TelegramHourReport{}).
		Select("activate").
		Where("chat_id = ?", chatID).
		Scan(&activate).Error

	if err != nil {
		return false, fmt.Errorf("failed to get status for chat_id=%d: %w", chatID, err)
	}
	return activate, nil
}

func GetHourTelegramReport(db *gorm.DB, chatid int64) (*TelegramHourReport, error) {
	var hr TelegramHourReport
	err := db.Model(&TelegramHourReport{}).
		Where("chat_id = ?", chatid).
		First(&hr).Error
	if err != nil {
		return nil, err
	}
	return &hr, nil
}
func createHourReportTelegram(db *gorm.DB, chatid int64) error {
	return db.Create(&TelegramHourReport{ChatID: chatid}).Error
}

// ============================ Telegram subscsubscriptions===============================

func InsertTelegramValidatorSub(db *gorm.DB, chatID int64, moniker, addr string) error {
	sub := TelegramValidatorSub{
		ChatID:   chatID,
		Moniker:  moniker,
		Addr:     addr,
		Activate: true,
	}

	// Vérifie si un abonnement existe déjà
	var existing TelegramValidatorSub
	err := db.
		Where("chat_id = ? AND addr = ?", chatID, addr).
		First(&existing).Error

	if err == nil {
		// Si déjà présent, on le réactive
		if !existing.Activate {
			return db.Model(&existing).Update("activate", true).Error
		}
		return nil // déjà actif → rien à faire
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Sinon on l'insère
		return db.Create(&sub).Error
	}

	return err
}
func GetTelegramValidatorSub(db *gorm.DB, chatID int64, onlyActive bool) ([]TelegramValidatorSub, error) {
	var subs []TelegramValidatorSub
	query := db.Where("chat_id = ?", chatID)
	if onlyActive {
		query = query.Where("activate = ?", true)
	}
	err := query.Order("created_at DESC").Find(&subs).Error
	return subs, err
}
func GetValidatorStatusList(db *gorm.DB, chatID int64) ([]ValidatorStatus, error) {

	var results []ValidatorStatus

	query := `
		WITH v AS (
			SELECT DISTINCT moniker, addr
			FROM daily_participations
		)
		SELECT
			v.moniker,
			v.addr,
			CASE
				WHEN s.activate = 1 THEN 'on'
				ELSE 'off'
			END AS status
		FROM v
		LEFT JOIN telegram_validator_subs s
			ON s.addr = v.addr
			AND s.chat_id = ?
		ORDER BY status DESC;
	`

	err := db.Raw(query, chatID).Scan(&results).Error
	if err != nil {
		return nil, err
	}

	return results, nil
}

func DeleteTelegramValidatorSub(db *gorm.DB, chatID int64, addr string) error {
	return db.
		Where("chat_id = ? AND addr = ?", chatID, addr).
		Delete(&TelegramValidatorSub{}).Error
}
func UpdateTelegramValidatorSubStatus(db *gorm.DB, chatID int64, addr, moniker, action string) error {
	var activate bool

	switch strings.ToLower(action) {
	case "subscribe":
		activate = true
	case "unsubscribe":
		activate = false
	default:
		return fmt.Errorf("invalid action: %s (expected 'subscribe' or 'unsubscribe')", action)
	}

	// find if exist record
	var sub TelegramValidatorSub
	err := db.Where("chat_id = ? AND addr = ?", chatID, addr).First(&sub).Error

	// if not existe insert record
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if activate {
			log.Printf("ℹ️ No existing record found — creating new active subscription for %s", addr)
			return InsertTelegramValidatorSub(db, chatID, moniker, addr)
		}
		log.Printf("ℹ️ No existing record found — nothing to unsubscribe for %s", addr)
		return nil
	}

	if err != nil {
		return fmt.Errorf("database lookup failed: %w", err)
	}

	//update status
	if sub.Activate == activate {
		log.Printf("⚙️ Subscription for %s already set to %t", addr, activate)
		return nil
	}

	res := db.Model(&sub).Update("activate", activate)
	if res.Error != nil {
		return fmt.Errorf("failed to update validator subscription: %w", res.Error)
	}

	log.Printf("✅ Subscription for %s set to %t", addr, activate)
	return nil
}

// ============================ Telegram govdao =============================================
// status of govdao handlers
func GetStatusofGovdao(db *gorm.DB) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT
			id,
			url,
			title,
			tx,
			status
		FROM
			govdaos
		ORDER BY
			id DESC;`

	err := db.Raw(query).Scan(&results).Error
	log.Println(results)

	return results, err
}

func GetLastExecute(db *gorm.DB) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT
			id,
			url,
			title,
			tx,
			status
		FROM
			govdaos
			where status = "ACCEPTED"
		ORDER BY
			id DESC;`

	err := db.Raw(query).Scan(&results).Error
	log.Println(results)

	return results, err
}
func GetLastPorposal(db *gorm.DB) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT
			id,
			url,
			title,
			tx,
			status
		FROM
			govdaos
		
		ORDER BY
			id DESC
		LIMIT 1;`

	err := db.Raw(query).Scan(&results).Error
	log.Println(results)

	return results, err
}
