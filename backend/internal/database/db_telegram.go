package database

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
