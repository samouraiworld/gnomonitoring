package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"
)

type Users struct {
	USER_ID string `json:"user_id"`
	NAME    string `json:"name"`
	EMAIL   string `json:"email"`
}
type AlertContact struct {
	ID         int
	USER_ID    string
	MONIKER    string
	NAME       string
	MENTIONTAG string
}

type WebhookGovDao struct {
	ID            int
	DESCRIPTION   string
	USER          string
	URL           string
	Type          string
	LastCheckedID int
}
type WebhookValidator struct {
	ID          int
	DESCRIPTION string
	USER        string
	URL         string
	Type        string
}
type HourReport struct {
	DAYLYRH int `json:"daily_report_hour"`
	DAYLYRM int `json:"daily_report_minute"`
}

func InitDB() *sql.DB {
	db, err := sql.Open("sqlite3", "./webhooks.db")
	if err != nil {
		log.Fatalf("DB opening error: %v", err)
	}
	dir, _ := os.Getwd()
	log.Println("Working dir:", dir)

	schema, err := os.ReadFile("./internal/database/schema.sql") // ou "./migrations/schema.sql"
	if err != nil {
		log.Fatalf("Error reading schema.sql: %v", err)
	}

	_, err = db.Exec(string(schema))
	if err != nil {
		log.Fatalf("Table creation error: %v", err)
	}
	_, err = db.Exec("PRAGMA journal_mode = WAL;") // Multi write
	if err != nil {
		log.Fatalf("Failed to enable WAL mode: %v", err)
	}
	InitGovDaoState(db)

	return db
}

// ===================================State GovDao=====================================
func InitGovDaoState(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO govdao_state (id, last_proposal_id) VALUES (1, -1)`)
	return err
}
func GetLastGovDaoProposalID(db *sql.DB) (int, error) {
	var id int
	err := db.QueryRow(`SELECT last_proposal_id FROM govdao_state WHERE id = 1`).Scan(&id)
	return id, err
}
func UpdateLastGovDaoProposalID(db *sql.DB, newID int) error {
	_, err := db.Exec(`UPDATE govdao_state SET last_proposal_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, newID)
	return err
}

// ==================================== GovDao ======================================
func InsertWebhook(user_id string, url string, description, wtype string, lastid int, db *sql.DB) error {
	if wtype != "discord" && wtype != "slack" {
		return fmt.Errorf("Invalid type. Use discord or slack")
	}

	_, err := db.Exec("INSERT OR IGNORE INTO webhooks_govdao (user_id, url,description, type, last_checked_id) VALUES (?, ?,?, ?, ?)", user_id, url, description, wtype, lastid)
	return err
}

func Loadwebhooks(db *sql.DB) ([]WebhookGovDao, error) {
	rows, err := db.Query("SELECT id, description, user_id, url, type, last_checked_id FROM webhooks_govdao")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var webhooks []WebhookGovDao
	for rows.Next() {
		var w WebhookGovDao
		if err := rows.Scan(&w.ID, &w.DESCRIPTION, &w.USER, &w.URL, &w.Type, &w.LastCheckedID); err != nil {
			return nil, err
		}
		webhooks = append(webhooks, w)
	}
	return webhooks, nil
}

func UpdateLastCheckedID(url string, newID int, db *sql.DB) error {
	_, err := db.Exec("UPDATE webhooks_govdao SET last_checked_id = ? WHERE url = ?", newID, url)
	return err
}

func ListWebhooks(db *sql.DB, user_id string) ([]WebhookGovDao, error) {
	rows, err := db.Query("SELECT id, description, user_id, url, type, last_checked_id FROM webhooks_govdao WHERE user_id = ?ORDER BY id ASC", user_id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []WebhookGovDao
	for rows.Next() {
		var wh WebhookGovDao
		err := rows.Scan(&wh.ID, &wh.DESCRIPTION, &wh.USER, &wh.URL, &wh.Type, &wh.LastCheckedID)
		if err != nil {
			return nil, err
		}
		list = append(list, wh)
	}
	return list, nil
}
func DeleteWebhook(id int, user_id string, db *sql.DB) error {
	_, err := db.Exec("DELETE FROM webhooks_govdao WHERE id = ? and user_id = ?", id, user_id)
	return err
}

// ==========================webhooks_validator ===============================================

func InsertMonitoringWebhook(user_id, url, description, typ string, db *sql.DB) error {
	_, err := db.Exec("INSERT INTO webhooks_validator (user_id,description, url, type) VALUES (?, ?, ?,?)", user_id, description, url, typ)
	return err
}

func DeleteMonitoringWebhook(id int, user_id string, db *sql.DB) error {
	_, err := db.Exec("DELETE FROM webhooks_validator WHERE id = ? and user_id = ?", id, user_id)
	return err
}

func ListMonitoringWebhooks(db *sql.DB, user_id string) ([]WebhookValidator, error) {
	rows, err := db.Query("SELECT id, description, user_id, url, type FROM webhooks_validator WHERE user_id= ?", user_id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []WebhookValidator
	for rows.Next() {
		var hook WebhookValidator
		if err := rows.Scan(&hook.ID, &hook.DESCRIPTION, &hook.USER, &hook.URL, &hook.Type); err != nil {
			return nil, err
		}
		result = append(result, hook)
	}
	return result, nil
}

// =============================== gnovalidator y Govdao ======================================
func UpdateMonitoringWebhook(db *sql.DB, id int, user_id, description, newURL, newType, tablename string) error {
	query := fmt.Sprintf(
		"UPDATE %s SET url=?,description=?, type=? WHERE user_id=? AND id = ?",
		tablename,
	)
	_, err := db.Exec(query, newURL, description, newType, user_id, id)
	if err != nil {
		return fmt.Errorf("failed to update webhook with id %d: %w", id, err)
	}
	return nil
}

func GetWebhookByID(db *sql.DB, user_id string, table string) (*WebhookValidator, error) {
	query := fmt.Sprintf("SELECT USER, description,	 URL, Type FROM %s WHERE user_id = ?", table)

	row := db.QueryRow(query, user_id)

	var wh WebhookValidator
	err := row.Scan(&wh.USER, &wh.DESCRIPTION, &wh.URL, &wh.Type)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Pas trouvé
		}
		return nil, fmt.Errorf("failed to get webhook with id %s: %w", user_id, err)
	}

	return &wh, nil
}

//============================== USERS ===================================================

func InsertUser(user_id, email, name string, db *sql.DB) error {
	_, err := db.Exec("INSERT INTO users (user_id, email, nameuser) VALUES (?, ?, ?)", user_id, email, name)
	createHourReport(db, user_id)
	return err
}
func DeleteUser(user_id string, db *sql.DB) error {
	_, err := db.Exec("DELETE FROM users WHERE user_id = ?", user_id)

	_, err = db.Exec("DELETE FROM webhooks_govdao WHERE user_id = ?", user_id)

	_, err = db.Exec("DELETE FROM webhooks_validator WHERE user_id = ?", user_id)

	_, err = db.Exec("DELETE FROM alert_contacts WHERE user_id = ?", user_id)

	_, err = db.Exec("DELETE FROM hour_report WHERE user_id = ?", user_id)

	return err
}

func UpdateUser(db *sql.DB, name, email, user_id string) error {

	_, err := db.Exec("UPDATE users SET nameuser=?, email = ? WHERE user_id=?", name, email, user_id)
	if err != nil {
		return fmt.Errorf("failed to update user with user_id %s: %w", user_id, err)
	}
	return nil
}
func GetUserById(db *sql.DB, userID string) (*Users, error) {
	row := db.QueryRow("SELECT user_id,nameuser, email, daily_report_hour, daily_report_minute FROM users WHERE user_id = ?", userID)

	var usr Users
	var hour, minute sql.NullInt64
	err := row.Scan(&usr.USER_ID, &usr.NAME, &usr.EMAIL, &hour, &minute)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get user with id %s: %w", userID, err)
	}

	return &usr, nil
}

// ============================== Report Hour =============================================
func UpdateHeureReport(db *sql.DB, H, M int, T, userID string) error {
	// Valider la timezone
	loc, err := time.LoadLocation(T)
	if err != nil {
		log.Printf("invalid timezone for user %s: %s, defaulting to UTC", userID, T)
		loc = time.UTC
		T = "UTC"
	}
	println(loc)
	// Mise à jour dans la base
	_, err = db.Exec(`
		UPDATE hour_report 
		SET daily_report_hour = ?, daily_report_minute = ?, timezone = ? 
		WHERE user_id = ?`, H, M, T, userID)

	if err != nil {
		return fmt.Errorf("failed to update hour and timezone for user %s: %w", userID, err)
	}

	return nil
}
func GetHourReport(db *sql.DB, userID string) (*HourReport, error) {
	row := db.QueryRow("SELECT daily_report_hour, daily_report_minute  FROM hour_report WHERE user_id = ?", userID)

	var hr HourReport
	err := row.Scan(&hr.DAYLYRH, &hr.DAYLYRM)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get hour report for user %s: %w", userID, err)
	}
	return &hr, nil
}
func createHourReport(db *sql.DB, user_id string) error {
	_, err := db.Exec("INSERT INTO hour_report (user_id) VALUES (?)", user_id)
	return err
}

// ============================== Alert_contact =============================================
func InsertAlertContact(db *sql.DB, user_id, moniker, namecontact, mention_tag string) error {
	stmt, err := db.Prepare(`
		INSERT INTO alert_contacts (user_id, moniker, namecontact, mention_tag)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(user_id, moniker, namecontact, mention_tag)
	if err != nil {
		return fmt.Errorf("failed to execute insert: %w", err)
	}

	return nil
}

func ListAlertContacts(db *sql.DB, userID string) ([]AlertContact, error) {
	rows, err := db.Query(`
		SELECT id, user_id, moniker, namecontact, mention_tag
		FROM alert_contacts WHERE user_id = ? ORDER BY id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []AlertContact
	for rows.Next() {
		var c AlertContact
		if err := rows.Scan(&c.ID, &c.USER_ID, &c.MONIKER, &c.NAME, &c.MENTIONTAG); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, nil
}
func UpdateAlertContact(db *sql.DB, id int, moniker, namecontact, mentionTag string) error {
	_, err := db.Exec(`
		UPDATE alert_contacts SET moniker = ?, namecontact = ?, mention_tag = ?
		WHERE id = ?`, moniker, namecontact, mentionTag, id)
	return err
}
func DeleteAlertContact(db *sql.DB, id int) error {
	_, err := db.Exec("DELETE FROM alert_contacts WHERE id = ?", id)
	return err
}

// ==================================== Purge ==========================================
func PruneOldParticipationData(db *sql.DB, keepDays int) error {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	stmt := `DELETE FROM daily_participation WHERE date < ?`

	res, err := db.Exec(stmt, cutoff)
	if err != nil {
		return fmt.Errorf("failed to prune old data: %w", err)
	}
	count, _ := res.RowsAffected()
	log.Printf("🧹 Pruned %d old rows (before %s)", count, cutoff)
	return nil
}
