package database

import (
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ===================================State GovDao=====================================
func InsertGovdao(db *gorm.DB, id int, chainID, url, title, tx, status string) error {
	govdao := Govdao{
		Id:      id,
		ChainID: chainID,
		Url:     url,
		Title:   title,
		Tx:      tx,
		Status:  status,
	}
	return db.Create(&govdao).Error

}

func GetLastGovDaoInfo(db *gorm.DB) (Govdao, error) {
	var govdao Govdao

	err := db.
		Order("id DESC").
		Limit(1).
		Find(&govdao).Error

	return govdao, err
}

// // ==================================== GovDao ======================================
func InsertWebhook(user_id string, url string, description, wtype string, db *gorm.DB) error {
	govdao := WebhookGovDAO{
		UserID:      user_id,
		URL:         url,
		Description: description,
		Type:        wtype,
	}

	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&govdao).Error

}

func LoadWebhooks(db *gorm.DB) ([]WebhookGovDAO, error) {
	var webhooks []WebhookGovDAO
	err := db.Find(&webhooks).Error
	return webhooks, err
}
func UpdateLastCheckedID(url string, newID int, db *gorm.DB) error {
	return db.Model(&WebhookGovDAO{}).
		Where("url = ?", url).
		Update("last_checked_id", newID).
		Error
}

func ListWebhooks(db *gorm.DB, userID string) ([]WebhookGovDAO, error) {
	var list []WebhookGovDAO
	err := db.
		Select("id, description, user_id, url, type, last_checked_id").
		Where("user_id = ?", userID).
		Order("id ASC").
		Find(&list).Error

	if err != nil {
		return nil, err
	}
	return list, nil
}

func DeleteWebhook(id int, userID string, db *gorm.DB) error {
	err := db.
		Where("id = ? AND user_id = ?", id, userID).
		Delete(&WebhookGovDAO{}).
		Error

	return err
}

// // ==========================webhooks_validator ===============================================

func InsertMonitoringWebhook(userID, url, description, typ, chainID string, db *gorm.DB) error {
	wh := WebhookValidator{
		UserID:      userID,
		URL:         url,
		Description: description,
		Type:        typ,
	}
	if chainID != "" {
		wh.ChainID = &chainID
	}

	if err := createHourReport(db, userID); err != nil {
		log.Printf("⚠️ createHourReport: %v", err)
	}

	return db.Create(&wh).Error
}

func DeleteMonitoringWebhook(id int, userID string, db *gorm.DB) error {
	return db.
		Where("id = ? AND user_id = ?", id, userID).
		Delete(&WebhookValidator{}).
		Error
}

func ListMonitoringWebhooks(db *gorm.DB, userID string, chainID ...string) ([]WebhookValidator, error) {
	var result []WebhookValidator
	q := db.Select("id, description, user_id, url, type, chain_id").
		Where("user_id = ?", userID)
	if len(chainID) > 0 && chainID[0] != "" {
		q = q.Where("chain_id = ? OR chain_id IS NULL", chainID[0])
	}
	err := q.Order("id ASC").Find(&result).Error
	return result, err
}

// =============================== gnovalidator y Govdao ======================================
func UpdateMonitoringWebhook(db *gorm.DB, id int, userID, description, newURL, newType, tablename string) error {
	var stmt string
	switch tablename {
	case "webhook_gov_daos":
		stmt = "UPDATE webhook_gov_daos SET url = ?, description = ?, type = ? WHERE user_id = ? AND id = ?"
	case "webhook_validators":
		stmt = "UPDATE webhook_validators SET url = ?, description = ?, type = ? WHERE user_id = ? AND id = ?"
	default:
		return fmt.Errorf("unknown table: %q", tablename)
	}
	return db.Exec(stmt, newURL, description, newType, userID, id).Error
}

func GetWebhookByID(db *gorm.DB, userID, table string) (*WebhookValidator, error) {
	var wh WebhookValidator
	var query string
	switch table {
	case "webhook_gov_daos":
		query = "SELECT user_id, description, url, type FROM webhook_gov_daos WHERE user_id = ?"
	case "webhook_validators":
		query = "SELECT user_id, description, url, type FROM webhook_validators WHERE user_id = ?"
	default:
		return nil, fmt.Errorf("unknown table: %q", table)
	}
	err := db.Raw(query, userID).Scan(&wh).Error
	if err != nil {
		return nil, err
	}
	return &wh, nil
}

// ============================== USERS ===================================================

func InsertUser(userID, email, name string, db *gorm.DB) error {
	u := User{
		UserID: userID,
		Email:  email,
		Name:   name,
	}
	err := db.Create(&u).Error
	if err != nil {
		return err
	}
	return createHourReport(db, userID)
}
func DeleteUser(userID string, db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		tables := []any{
			&WebhookGovDAO{}, &WebhookValidator{},
			&AlertContact{}, &HourReport{},
			&User{},
		}
		for _, model := range tables {
			if err := tx.Where("user_id = ?", userID).Delete(model).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func UpdateUser(db *gorm.DB, name, email, userID string) error {
	return db.
		Model(&User{}).
		Where("user_id = ?", userID).
		Updates(map[string]interface{}{
			"nameuser": name,
			"email":    email,
		}).Error
}
func GetUserById(db *gorm.DB, userID string) (*User, error) {
	var usr User
	err := db.
		Select("user_id, nameuser, email").
		Where("user_id = ?", userID).
		First(&usr).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &usr, nil
}

// ============================== Report Hour =============================================
func UpdateHeureReport(db *gorm.DB, h, m int, t, userID string) error {
	// Validate timezone
	if _, err := time.LoadLocation(t); err != nil {
		log.Printf("Invalid timezone '%s', defaulting to UTC", t)
		t = "UTC"
	}
	return db.
		Model(&HourReport{}).
		Where("user_id = ?", userID).
		Updates(map[string]interface{}{
			"daily_report_hour":   h,
			"daily_report_minute": m,
			"timezone":            t,
		}).Error
}
func GetHourReport(db *gorm.DB, userID string) (*HourReport, error) {
	var hr HourReport
	err := db.Model(&HourReport{}).
		Where("user_id = ?", userID).
		First(&hr).Error
	if err != nil {
		return nil, err
	}
	return &hr, nil
}
func createHourReport(db *gorm.DB, userID string) error {
	return db.Create(&HourReport{UserID: userID}).Error
}

// ============================== Alert_contact =============================================
func InsertAlertContact(db *gorm.DB, userID, moniker, namecontact, mentionTag string, idwebhook int) error {
	contact := AlertContact{
		UserID:      userID,
		Moniker:     moniker,
		NameContact: namecontact,
		MentionTag:  mentionTag,
		IDwebhook:   idwebhook,
	}
	return db.Create(&contact).Error
}

func ListAlertContacts(db *gorm.DB, userID string) ([]AlertContact, error) {
	var contacts []AlertContact
	err := db.
		Where("user_id = ?", userID).
		Order("id ASC").
		Find(&contacts).Error
	return contacts, err
}

func UpdateAlertContact(db *gorm.DB, id int, userID, moniker, namecontact, mentionTag string, idwebhook int) error {

	return db.
		Model(&AlertContact{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]interface{}{
			"moniker":     moniker,
			"namecontact": namecontact,
			"mention_tag": mentionTag,
			"id_webhook":  idwebhook,
		}).Error
}
func DeleteAlertContact(db *gorm.DB, id int, userID string) error {
	return db.Where("id = ? AND user_id = ?", id, userID).Delete(&AlertContact{}).Error
}

// UpsertAddrMoniker inserts or updates the moniker for a given validator address.
func UpsertAddrMoniker(db *gorm.DB, chainID, addr, moniker string) error {
	return db.Exec(`
		INSERT INTO addr_monikers (chain_id, addr, moniker)
		VALUES (?, ?, ?)
		ON CONFLICT(chain_id, addr) DO UPDATE SET moniker = excluded.moniker
	`, chainID, addr, moniker).Error
}

// GetMonikerByAddr returns the moniker for a given validator address.
// Returns an empty string (no error) if the address is not found.
func GetMonikerByAddr(db *gorm.DB, chainID, addr string) (string, error) {
	var result struct{ Moniker string }
	err := db.Table("addr_monikers").
		Select("moniker").
		Where("chain_id = ? AND addr = ?", chainID, addr).
		First(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return result.Moniker, err
}

// ==================================== Purge ==========================================
func PruneOldParticipationData(db *gorm.DB, keepDays int) error {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	res := db.
		Where("date < ?", cutoff).
		Delete(&DailyParticipation{})

	if res.Error != nil {
		return res.Error
	}

	log.Printf("🧹 Pruned %d old rows (before %s)", res.RowsAffected, cutoff)
	return nil
}
