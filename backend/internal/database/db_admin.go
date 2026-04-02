package database

import (
	"fmt"
	"strconv"

	"gorm.io/gorm"
)

// ── admin_config helpers ─────────────────────────────────────────────────────

// GetAdminConfig returns the string value for a given admin_config key.
func GetAdminConfig(db *gorm.DB, key string) (string, error) {
	var cfg AdminConfig
	if err := db.Where("key = ?", key).First(&cfg).Error; err != nil {
		return "", fmt.Errorf("GetAdminConfig %q: %w", key, err)
	}
	return cfg.Value, nil
}

// GetAdminConfigInt returns the integer value for a given admin_config key.
// Returns fallback if the key is missing or not a valid integer.
func GetAdminConfigInt(db *gorm.DB, key string, fallback int) int {
	val, err := GetAdminConfig(db, key)
	if err != nil {
		return fallback
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return n
}

// SetAdminConfig upserts a key/value pair in admin_config.
func SetAdminConfig(db *gorm.DB, key, value string) error {
	return db.Exec(
		"INSERT INTO admin_configs (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	).Error
}

// GetAllAdminConfigs returns all admin_config rows.
func GetAllAdminConfigs(db *gorm.DB) ([]AdminConfig, error) {
	var configs []AdminConfig
	if err := db.Order("key").Find(&configs).Error; err != nil {
		return nil, err
	}
	return configs, nil
}

// SetAdminConfigBatch upserts multiple key/value pairs at once.
func SetAdminConfigBatch(db *gorm.DB, pairs map[string]string) error {
	for key, value := range pairs {
		if err := SetAdminConfig(db, key, value); err != nil {
			return fmt.Errorf("SetAdminConfigBatch %q: %w", key, err)
		}
	}
	return nil
}

// ── users ────────────────────────────────────────────────────────────────────

// GetAllUsers returns all users ordered by created_at desc.
func GetAllUsers(db *gorm.DB) ([]User, error) {
	var users []User
	if err := db.Order("created_at desc").Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// ── webhooks ─────────────────────────────────────────────────────────────────

// WebhookAdmin is a unified view of both webhook table types.
type WebhookAdmin struct {
	ID          int     `json:"id"`
	UserID      string  `json:"user_id"`
	URL         string  `json:"url"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
	ChainID     *string `json:"chain_id"`
	Kind        string  `json:"kind"` // "govdao" or "validator"
}

// GetAllWebhooksAdmin returns all webhooks from both tables.
func GetAllWebhooksAdmin(db *gorm.DB) ([]WebhookAdmin, error) {
	var result []WebhookAdmin

	var govdao []WebhookGovDAO
	if err := db.Find(&govdao).Error; err != nil {
		return nil, err
	}
	for _, w := range govdao {
		result = append(result, WebhookAdmin{
			ID: w.ID, UserID: w.UserID, URL: w.URL, Type: w.Type,
			Description: w.Description, ChainID: w.ChainID, Kind: "govdao",
		})
	}

	var validators []WebhookValidator
	if err := db.Find(&validators).Error; err != nil {
		return nil, err
	}
	for _, w := range validators {
		result = append(result, WebhookAdmin{
			ID: w.ID, UserID: w.UserID, URL: w.URL, Type: w.Type,
			Description: w.Description, ChainID: w.ChainID, Kind: "validator",
		})
	}

	return result, nil
}

// DeleteWebhookAdmin deletes a webhook by kind and id without user scope check.
func DeleteWebhookAdmin(db *gorm.DB, kind string, id int) error {
	switch kind {
	case "govdao":
		return db.Delete(&WebhookGovDAO{}, id).Error
	case "validator":
		return db.Delete(&WebhookValidator{}, id).Error
	default:
		return fmt.Errorf("unknown webhook kind: %q", kind)
	}
}

// ResetGovDAOLastCheckedID resets last_checked_id to -1 for a GovDAO webhook.
func ResetGovDAOLastCheckedID(db *gorm.DB, id int) error {
	return db.Model(&WebhookGovDAO{}).Where("id = ?", id).Update("last_checked_id", -1).Error
}

// ── alert_logs ───────────────────────────────────────────────────────────────

// GetAllAlertLogs returns alert_logs with optional filters. Pass empty string to skip a filter.
func GetAllAlertLogs(db *gorm.DB, chainID, level, from, to string, limit int) ([]AlertLog, error) {
	q := db.Model(&AlertLog{}).Order("sent_at desc")
	if chainID != "" {
		q = q.Where("chain_id = ?", chainID)
	}
	if level != "" {
		q = q.Where("level = ?", level)
	}
	if from != "" {
		q = q.Where("sent_at >= ?", from)
	}
	if to != "" {
		q = q.Where("sent_at <= ?", to)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var logs []AlertLog
	if err := q.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// PurgeAlertLogsByChain deletes all alert_logs for a chain.
func PurgeAlertLogsByChain(db *gorm.DB, chainID string) error {
	return db.Where("chain_id = ?", chainID).Delete(&AlertLog{}).Error
}

// ── addr_monikers ─────────────────────────────────────────────────────────────

// GetAllMonikersList returns all addr_monikers rows, optionally filtered by chain.
func GetAllMonikersList(db *gorm.DB, chainID string) ([]AddrMoniker, error) {
	q := db.Model(&AddrMoniker{})
	if chainID != "" {
		q = q.Where("chain_id = ?", chainID)
	}
	var monikers []AddrMoniker
	if err := q.Order("chain_id, addr").Find(&monikers).Error; err != nil {
		return nil, err
	}
	return monikers, nil
}

// DeleteAddrMonikerAdmin deletes a moniker override by chain+addr.
func DeleteAddrMonikerAdmin(db *gorm.DB, chainID, addr string) error {
	return db.Where("chain_id = ? AND addr = ?", chainID, addr).Delete(&AddrMoniker{}).Error
}

// ── telegram ──────────────────────────────────────────────────────────────────

// GetAllTelegramChatsAdmin returns all telegram chat registrations.
func GetAllTelegramChatsAdmin(db *gorm.DB) ([]Telegram, error) {
	var chats []Telegram
	if err := db.Order("chat_id").Find(&chats).Error; err != nil {
		return nil, err
	}
	return chats, nil
}

// DeleteTelegramChatCascade deletes a telegram chat and all its associated data.
func DeleteTelegramChatCascade(db *gorm.DB, chatID int64) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	if err := tx.Where("chat_id = ?", chatID).Delete(&Telegram{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Where("chat_id = ?", chatID).Delete(&TelegramHourReport{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Where("chat_id = ?", chatID).Delete(&TelegramValidatorSub{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// GetAllTelegramSubsAdmin returns telegram_validator_subs with optional filters.
// Pass empty chainID or 0 chatID to skip that filter.
func GetAllTelegramSubsAdmin(db *gorm.DB, chainID string, chatID int64) ([]TelegramValidatorSub, error) {
	q := db.Model(&TelegramValidatorSub{})
	if chainID != "" {
		q = q.Where("chain_id = ?", chainID)
	}
	if chatID != 0 {
		q = q.Where("chat_id = ?", chatID)
	}
	var subs []TelegramValidatorSub
	if err := q.Order("chain_id, chat_id").Find(&subs).Error; err != nil {
		return nil, err
	}
	return subs, nil
}

// ToggleTelegramSubAdmin enables or disables a telegram_validator_subs row by its primary key.
func ToggleTelegramSubAdmin(db *gorm.DB, id uint, activate bool) error {
	return db.Model(&TelegramValidatorSub{}).Where("id = ?", id).Update("activate", activate).Error
}

// GetAllTelegramSchedulesAdmin returns all telegram_hour_reports.
func GetAllTelegramSchedulesAdmin(db *gorm.DB) ([]TelegramHourReport, error) {
	var reports []TelegramHourReport
	if err := db.Order("chat_id, chain_id").Find(&reports).Error; err != nil {
		return nil, err
	}
	return reports, nil
}

// UpdateTelegramScheduleAdmin updates a telegram_hour_reports row.
func UpdateTelegramScheduleAdmin(db *gorm.DB, chatID int64, chainID string, hour, minute int, timezone string, activate bool) error {
	return db.Model(&TelegramHourReport{}).
		Where("chat_id = ? AND chain_id = ?", chatID, chainID).
		Updates(map[string]interface{}{
			"daily_report_hour":   hour,
			"daily_report_minute": minute,
			"timezone":            timezone,
			"activate":            activate,
		}).Error
}

// ── hour_reports (web users) ──────────────────────────────────────────────────

// HourReportAdmin is a panel-only projection of HourReport with snake_case JSON keys.
type HourReportAdmin struct {
	UserID            string `json:"user_id"`
	DailyReportHour   int    `json:"daily_report_hour"`
	DailyReportMinute int    `json:"daily_report_minute"`
	Timezone          string `json:"timezone"`
}

// GetAllHourReportsAdmin returns all web user hour_reports as panel-safe DTOs.
func GetAllHourReportsAdmin(db *gorm.DB) ([]HourReportAdmin, error) {
	var reports []HourReport
	if err := db.Order("user_id").Find(&reports).Error; err != nil {
		return nil, err
	}
	result := make([]HourReportAdmin, len(reports))
	for i, r := range reports {
		result[i] = HourReportAdmin{
			UserID:            r.UserID,
			DailyReportHour:   r.DailyReportHour,
			DailyReportMinute: r.DailyReportMinute,
			Timezone:          r.Timezone,
		}
	}
	return result, nil
}

// UpdateHourReportAdmin updates a web user's hour_report.
func UpdateHourReportAdmin(db *gorm.DB, userID string, hour, minute int, timezone string) error {
	return db.Model(&HourReport{}).
		Where("user_id = ?", userID).
		Updates(map[string]interface{}{
			"daily_report_hour":   hour,
			"daily_report_minute": minute,
			"timezone":            timezone,
		}).Error
}

// ── chain data purge ──────────────────────────────────────────────────────────

// PurgeChainAllData deletes all chain data: participations, aggregates, alerts,
// monikers, and telegram subscriptions for the given chain.
func PurgeChainAllData(db *gorm.DB, chainID string) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	for _, model := range []interface{}{
		&DailyParticipation{},
		&DailyParticipationAgrega{},
		&AlertLog{},
		&AddrMoniker{},
		&Telegram{},
		&TelegramHourReport{},
		&TelegramValidatorSub{},
	} {
		if err := tx.Where("chain_id = ?", chainID).Delete(model).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("PurgeChainAllData %T: %w", model, err)
		}
	}
	return tx.Commit().Error
}

// PurgeChainParticipations deletes participations, aggregates, and alert_logs for a chain
// but keeps monikers, config, and telegram subscriptions.
func PurgeChainParticipations(db *gorm.DB, chainID string) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	for _, model := range []interface{}{
		&DailyParticipation{},
		&DailyParticipationAgrega{},
		&AlertLog{},
	} {
		if err := tx.Where("chain_id = ?", chainID).Delete(model).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("PurgeChainParticipations %T: %w", model, err)
		}
	}
	return tx.Commit().Error
}
