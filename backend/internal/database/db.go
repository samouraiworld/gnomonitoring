package database

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// =================================== VIEW MISSING BLOCK ============================
func CreateMissingBlocksView(db *gorm.DB) error {
	createViewSQL := `
CREATE VIEW IF NOT EXISTS daily_missing_series AS
WITH ranked AS (
    SELECT
        addr,
        moniker,
        date,
        block_height,
        participated,
        -- Vérifie si le bloc précédent était manqué
        CASE 
            WHEN participated = 0 AND LAG(participated) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) = 1
            THEN 1
            WHEN participated = 0 AND LAG(participated) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) IS NULL
            THEN 1
            ELSE 0
        END AS new_seq
    FROM daily_participations
	WHERE date >= datetime('now', '-24 hours')
),
grouped AS (
    SELECT
        *,
        SUM(new_seq) OVER (PARTITION BY addr, moniker, DATE(date) ORDER BY block_height) AS seq_id
    FROM ranked
)
SELECT
    addr,
    moniker,
    DATE(date) AS date,
    TIME(date) AS time_block,
    MIN(block_height) OVER (PARTITION BY addr, moniker, DATE(date), seq_id) AS start_height,
    block_height AS end_height,
    SUM(1) OVER (
        PARTITION BY addr, moniker, DATE(date), seq_id
        ORDER BY block_height
        ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
    ) AS missed
FROM grouped
WHERE participated = 0
ORDER BY addr, moniker, date, seq_id, block_height;

	`

	if err := db.Exec(createViewSQL).Error; err != nil {
		return fmt.Errorf("failed to create view: %w", err)
	}

	log.Println("✅ View `daily_missing_series` created")
	return nil
}

// ===================================State GovDao=====================================
func InsertGovdao(db *gorm.DB, id int, url, title, tx, status string) error {
	govdao := Govdao{
		Id:     id,
		Url:    url,
		Title:  title,
		Tx:     tx,
		Status: status,
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

func InsertMonitoringWebhook(userID, url, description, typ string, db *gorm.DB) error {
	wh := WebhookValidator{
		UserID:      userID,
		URL:         url,
		Description: description,
		Type:        typ,
	}

	createHourReport(db, userID)

	return db.Create(&wh).Error
}

func DeleteMonitoringWebhook(id int, userID string, db *gorm.DB) error {
	return db.
		Where("id = ? AND user_id = ?", id, userID).
		Delete(&WebhookValidator{}).
		Error
}

func ListMonitoringWebhooks(db *gorm.DB, userID string) ([]WebhookValidator, error) {
	var result []WebhookValidator
	err := db.
		Select("id, description, user_id, url, type").
		Where("user_id = ?", userID).
		Order("id ASC").
		Find(&result).Error
	return result, err
}

// =============================== gnovalidator y Govdao ======================================
func UpdateMonitoringWebhook(db *gorm.DB, id int, userID, description, newURL, newType, tablename string) error {
	stmt := fmt.Sprintf("UPDATE %s SET url = ?, description = ?, type = ? WHERE user_id = ? AND id = ?", tablename)
	return db.Exec(stmt, newURL, description, newType, userID, id).Error
}

func GetWebhookByID(db *gorm.DB, userID, table string) (*WebhookValidator, error) {
	var wh WebhookValidator
	query := fmt.Sprintf("SELECT user_id, description, url, type FROM %s WHERE user_id = ?", table)
	err := db.Raw(query, userID).Scan(&wh).Error
	if err != nil {
		return nil, err
	}
	return &wh, nil
}

//============================== USERS ===================================================

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
	if err != nil {
		return nil, err
	}
	return &usr, nil
}

// ============================== Report Hour =============================================
func UpdateHeureReport(db *gorm.DB, H, M int, T, userID string) error {
	// Validate timezone
	if _, err := time.LoadLocation(T); err != nil {
		log.Printf("Invalid timezone '%s', defaulting to UTC", T)
		T = "UTC"
	}
	return db.
		Model(&HourReport{}).
		Where("user_id = ?", userID).
		Updates(map[string]interface{}{
			"daily_report_hour":   H,
			"daily_report_minute": M,
			"timezone":            T,
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
func DeleteAlertContact(db *gorm.DB, id int) error {
	return db.Delete(&AlertContact{}, id).Error
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
