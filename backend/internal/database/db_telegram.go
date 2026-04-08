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

// InsertChatID upserts the chat record. When chatType is "validator" it also
// ensures a TelegramHourReport row exists for the given chainID so that the
// scheduler can pick it up.
func InsertChatID(db *gorm.DB, chatID int64, chatType string, chainID ...string) (bool, error) {
	cid := ""
	if len(chainID) > 0 {
		cid = chainID[0]
	}
	if cid == "" {
		cid = "betanet"
	}

	chat := Telegram{
		ChatID:  chatID,
		Type:    chatType,
		ChainID: cid,
	}

	if chatType == "validator" {
		if err := createHourReportTelegram(db, chatID, cid); err != nil {
			log.Printf("⚠️ createHourReportTelegram: %v", err)
		}
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
func GetAllChatIDs(db *gorm.DB, typeChatid string) ([]int64, error) {
	var chats []Telegram

	if err := db.Where("type = ?", typeChatid).Find(&chats).Error; err != nil {
		return nil, err
	}

	var ids []int64
	for _, c := range chats {
		ids = append(ids, c.ChatID)
	}
	return ids, nil
}

// GetChatIDsForChain returns all chat IDs of the given type that have at least
// one active validator subscription for chainID. Used to scope chain-level
// alerts (stuck, activity restored, new validator) to interested chats only.
func GetChatIDsForChain(db *gorm.DB, typeChatid, chainID string) ([]int64, error) {
	var ids []int64
	err := db.Raw(`
		SELECT chat_id FROM telegrams WHERE chain_id = ? AND type = ?
	`, chainID, typeChatid).Scan(&ids).Error
	return ids, err
}

// GetAllChatChains returns a map of chat_id -> chain_id for all validator chats.
// Used at startup to hydrate chatChainState from persisted preferences.
func GetAllChatChains(db *gorm.DB) (map[int64]string, error) {
	var rows []Telegram
	if err := db.Where("type = ?", "validator").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("GetAllChatChains: %w", err)
	}
	result := make(map[int64]string, len(rows))
	for _, r := range rows {
		if r.ChainID != "" {
			result[r.ChatID] = r.ChainID
		}
	}
	return result, nil
}

// UpdateChatChain persists the per-chat chain preference to the database.
// Called after /setchain to save the user's selection.
func UpdateChatChain(db *gorm.DB, chatID int64, chainID string) error {
	res := db.Model(&Telegram{}).
		Where("chat_id = ? AND type = ?", chatID, "validator").
		Update("chain_id", chainID)
	return res.Error
}

// GetAllGovdaoChatChains returns a map of chat_id -> chain_id for all govdao chats.
// Used at startup to hydrate govdaoChatChainState from persisted preferences.
func GetAllGovdaoChatChains(db *gorm.DB) (map[int64]string, error) {
	var rows []Telegram
	if err := db.Where("type = ?", "govdao").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("GetAllGovdaoChatChains: %w", err)
	}
	result := make(map[int64]string, len(rows))
	for _, r := range rows {
		if r.ChainID != "" {
			result[r.ChatID] = r.ChainID
		}
	}
	return result, nil
}

// UpdateGovdaoChatChain persists the per-chat chain preference to the database for govdao.
// Called after /setchain to save the user's selection.
func UpdateGovdaoChatChain(db *gorm.DB, chatID int64, chainID string) error {
	res := db.Model(&Telegram{}).
		Where("chat_id = ? AND type = ?", chatID, "govdao").
		Update("chain_id", chainID)
	return res.Error
}

// ============================ Telegram validato =============================================
// ================== Telegram hours report ================================

func UpdateTelegramHeureReport(db *gorm.DB, h, m int, t string, chatid int64, chainID string) error {
	// Validate timezone
	if _, err := time.LoadLocation(t); err != nil {
		log.Printf("Invalid timezone '%s', defaulting to UTC", t)
		t = "UTC"
	}
	return db.
		Model(&TelegramHourReport{}).
		Where("chat_id = ? AND chain_id = ?", chatid, chainID).
		Updates(map[string]interface{}{
			"daily_report_hour":   h,
			"daily_report_minute": m,
			"timezone":            t,
		}).Error
}

func ActivateTelegramReport(db *gorm.DB, isActivate bool, chatid int64, chainID string) error {
	return db.
		Model(&TelegramHourReport{}).
		Where("chat_id = ? AND chain_id = ?", chatid, chainID).
		Updates(map[string]interface{}{
			"activate": isActivate,
		}).Error
}

func GetTelegramReportStatus(db *gorm.DB, chatID int64, chainID string) (bool, error) {
	var activate bool
	err := db.Model(&TelegramHourReport{}).
		Select("activate").
		Where("chat_id = ? AND chain_id = ?", chatID, chainID).
		Scan(&activate).Error

	if err != nil {
		return false, fmt.Errorf("failed to get status for chat_id=%d chain_id=%s: %w", chatID, chainID, err)
	}
	return activate, nil
}

func GetHourTelegramReport(db *gorm.DB, chatid int64, chainID string) (*TelegramHourReport, error) {
	var hr TelegramHourReport
	err := db.Model(&TelegramHourReport{}).
		Where("chat_id = ? AND chain_id = ?", chatid, chainID).
		First(&hr).Error
	if err != nil {
		return nil, err
	}
	return &hr, nil
}

// GetAllHourTelegramReports returns all active TelegramHourReport rows for the
// given chat. It returns one row per (chat_id, chain_id) pair.
func GetAllHourTelegramReports(db *gorm.DB, chatid int64) ([]TelegramHourReport, error) {
	var hrs []TelegramHourReport
	err := db.Model(&TelegramHourReport{}).
		Where("chat_id = ?", chatid).
		Find(&hrs).Error
	return hrs, err
}

func createHourReportTelegram(db *gorm.DB, chatid int64, chainID string) error {
	return db.Clauses().FirstOrCreate(&TelegramHourReport{
		ChatID:  chatid,
		ChainID: chainID,
	}).Error
}

// ============================ Telegram subscsubscriptions===============================

func InsertTelegramValidatorSub(db *gorm.DB, chatID int64, chainID, moniker, addr string) error {
	sub := TelegramValidatorSub{
		ChatID:   chatID,
		ChainID:  chainID,
		Moniker:  moniker,
		Addr:     addr,
		Activate: true,
	}

	// Check if a subscription already exists for this (chat_id, chain_id, addr) triplet.
	var existing TelegramValidatorSub
	err := db.
		Where("chat_id = ? AND chain_id = ? AND addr = ?", chatID, chainID, addr).
		First(&existing).Error

	if err == nil {
		// Already present: reactivate if needed.
		if !existing.Activate {
			return db.Model(&existing).Update("activate", true).Error
		}
		return nil // already active — nothing to do
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Create(&sub).Error
	}

	return err
}

func GetTelegramValidatorSub(db *gorm.DB, chatID int64, chainID string, onlyActive bool) ([]TelegramValidatorSub, error) {
	var subs []TelegramValidatorSub
	query := db.Where("chat_id = ? AND chain_id = ?", chatID, chainID)
	if onlyActive {
		query = query.Where("activate = ?", true)
	}
	err := query.Order("created_at DESC").Find(&subs).Error
	return subs, err
}

func GetValidatorStatusList(db *gorm.DB, chatID int64, chainID string) ([]ValidatorStatus, error) {

	var results []ValidatorStatus

	// UNION raw (last 7 days) + agrega (all history) to keep validators visible
	// even after raw data is pruned beyond the retention window.
	query := `
		WITH v AS (
			SELECT DISTINCT addr, COALESCE(
				(SELECT moniker FROM addr_monikers am WHERE am.chain_id = ? AND am.addr = all_addrs.addr),
				addr
			) AS moniker
			FROM (
				SELECT dp.addr FROM daily_participations dp WHERE dp.chain_id = ?
				UNION
				SELECT dpa.addr FROM daily_participation_agregas dpa WHERE dpa.chain_id = ?
			) all_addrs
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
			AND s.chain_id = ?
			AND s.chat_id = ?
		ORDER BY status DESC;
	`

	err := db.Raw(query, chainID, chainID, chainID, chainID, chatID).Scan(&results).Error
	if err != nil {
		return nil, err
	}

	return results, nil
}

func GetAllValidators(db *gorm.DB, chainID string) ([]AddrMoniker, error) {

	var results []AddrMoniker

	// UNION raw + agrega so validators pruned from raw remain visible.
	query := `
		SELECT DISTINCT all_addrs.addr, COALESCE(am.moniker, all_addrs.addr) AS moniker
		FROM (
			SELECT dp.addr FROM daily_participations dp WHERE dp.chain_id = ?
			UNION
			SELECT dpa.addr FROM daily_participation_agregas dpa WHERE dpa.chain_id = ?
		) all_addrs
		LEFT JOIN addr_monikers am ON am.chain_id = ? AND am.addr = all_addrs.addr;`

	err := db.Raw(query, chainID, chainID, chainID).Scan(&results).Error
	if err != nil {
		return nil, err
	}

	return results, nil
}

func ResolveAddrs(db *gorm.DB, chainID string, addrs []string) ([]AddrMoniker, error) {
	var results []AddrMoniker

	err := db.Raw(`
		SELECT DISTINCT am.addr, COALESCE(am.moniker, am.addr) AS moniker
		FROM addr_monikers am
		WHERE am.chain_id = ? AND am.addr IN ?
	`, chainID, addrs).Scan(&results).Error
	if err != nil {
		return nil, err
	}

	return results, nil
}

func DeleteTelegramValidatorSub(db *gorm.DB, chatID int64, chainID, addr string) error {
	return db.
		Where("chat_id = ? AND chain_id = ? AND addr = ?", chatID, chainID, addr).
		Delete(&TelegramValidatorSub{}).Error
}

func UpdateTelegramValidatorSubStatus(db *gorm.DB, chatID int64, chainID, addr, moniker, action string) error {
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
	err := db.Where("chat_id = ? AND chain_id = ? AND addr = ?", chatID, chainID, addr).First(&sub).Error

	// if not exist insert record
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if activate {
			log.Printf("ℹ️ No existing record found — creating new active subscription for %s", addr)
			return InsertTelegramValidatorSub(db, chatID, chainID, moniker, addr)
		}
		log.Printf("ℹ️ No existing record found — nothing to unsubscribe for %s", addr)
		return nil
	}

	if err != nil {
		return fmt.Errorf("database lookup failed: %w", err)
	}

	// update status
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
func GetStatusofGovdao(db *gorm.DB, chainID string) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT
			id,
			url,
			title,
			tx,
			status,
			chain_id
		FROM
			govdaos
		WHERE chain_id = ?
		ORDER BY
			id DESC;`

	err := db.Raw(query, chainID).Scan(&results).Error
	log.Println(results)

	return results, err
}

func GetLastExecute(db *gorm.DB, chainID string) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT
			id,
			url,
			title,
			tx,
			status,
			chain_id
		FROM
			govdaos
		WHERE chain_id = ? AND status = "ACCEPTED"
		ORDER BY
			id DESC;`

	err := db.Raw(query, chainID).Scan(&results).Error
	log.Println(results)

	return results, err
}
func GetLastPorposal(db *gorm.DB, chainID string) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT
			id,
			url,
			title,
			tx,
			status,
			chain_id
		FROM
			govdaos
		WHERE chain_id = ?
		ORDER BY
			id DESC
		LIMIT 1;`

	err := db.Raw(query, chainID).Scan(&results).Error
	log.Println(results)

	return results, err
}
