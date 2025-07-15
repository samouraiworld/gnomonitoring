package internal

import (
	"database/sql"
	"fmt"
	"log"
)

func InitDB() *sql.DB {
	db, err := sql.Open("sqlite3", "./webhooks.db")
	if err != nil {
		log.Fatalf("DB opening error: %v", err)
	}

	createTable := `
	CREATE TABLE IF NOT EXISTS webhooks_GovDAO (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user TEXT NOT NULL,
		url TEXT NOT NULL,
		type TEXT NOT NULL, -- "discord" ou "slack"
		last_checked_id INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS webhooks_validator (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user TEXT NOT NULL,
    url TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('discord', 'slack'))
);
CREATE TABLE IF NOT EXISTS daily_participation (
    date TEXT NOT NULL,
    block_height INTEGER NOT NULL,
    moniker TEXT NOT NULL,
	addr TEXT NOT NULL,
    participated BOOLEAN NOT NULL,
    PRIMARY KEY (date, block_height, moniker)
);
CREATE INDEX IF NOT EXISTS idx_participation_date ON daily_participation(date);
	`
	_, err = db.Exec(createTable)
	if err != nil {
		log.Fatalf("Table creation error: %v", err)
	}

	return db
}

type WebhookGovDao struct {
	ID            int
	USER          string
	URL           string
	Type          string
	LastCheckedID int
}
type WebhookValidator struct {
	ID            int
	USER          string
	URL           string
	Type          string
	LastCheckedID int
}

func InsertWebhook(user string, url string, wtype string, db *sql.DB) error {
	if wtype != "discord" && wtype != "slack" {
		return fmt.Errorf("Invalid type. Use discord or slack")
	}

	_, err := db.Exec("INSERT OR IGNORE INTO webhooks_GovDAO (user, url, type, last_checked_id) VALUES (?, ?, ?, 0)", user, url, wtype)
	return err
}

func Loadwebhooks(db *sql.DB) ([]WebhookGovDao, error) {
	rows, err := db.Query("SELECT id, user, url, type, last_checked_id FROM webhooks_GovDAO")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var webhooks []WebhookGovDao
	for rows.Next() {
		var w WebhookGovDao
		if err := rows.Scan(&w.ID, &w.USER, &w.URL, &w.Type, &w.LastCheckedID); err != nil {
			return nil, err
		}
		webhooks = append(webhooks, w)
	}
	return webhooks, nil
}

func UpdateLastCheckedID(url string, newID int, db *sql.DB) error {
	_, err := db.Exec("UPDATE webhooks_GovDAO SET last_checked_id = ? WHERE url = ?", newID, url)
	return err
}

func ListWebhooks(db *sql.DB) ([]WebhookGovDao, error) {
	rows, err := db.Query("SELECT id,user, url, type, last_checked_id FROM webhooks_GovDAO ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []WebhookGovDao
	for rows.Next() {
		var wh WebhookGovDao
		err := rows.Scan(&wh.ID, &wh.USER, &wh.URL, &wh.Type, &wh.LastCheckedID)
		if err != nil {
			return nil, err
		}
		list = append(list, wh)
	}
	return list, nil
}
func DeleteWebhook(id int, db *sql.DB) error {
	_, err := db.Exec("DELETE FROM webhooks_GovDAO WHERE id = ?", id)
	return err
}

// fonction webhooks_validator

func InsertMonitoringWebhook(user, url, typ string, db *sql.DB) error {
	_, err := db.Exec("INSERT INTO webhooks_validator (user, url, type) VALUES ($1, $2, $3)", user, url, typ)
	return err
}

func DeleteMonitoringWebhook(id int, db *sql.DB) error {
	_, err := db.Exec("DELETE FROM webhooks_validator WHERE id = $1", id)
	return err
}

func ListMonitoringWebhooks(db *sql.DB) ([]WebhookValidator, error) {
	rows, err := db.Query("SELECT id, user, url, type FROM webhooks_validator")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []WebhookValidator
	for rows.Next() {
		var hook WebhookValidator
		if err := rows.Scan(&hook.ID, &hook.USER, &hook.URL, &hook.Type); err != nil {
			return nil, err
		}
		result = append(result, hook)
	}
	return result, nil
}
func UpdateMonitoringWebhook(db *sql.DB, id int64, newUser, newURL, newType, tablename string) error {
	query := fmt.Sprintf(
		"UPDATE %s SET user=?, url=?, type=? WHERE id=?",
		tablename, // attention : doit être contrôlé
	)
	_, err := db.Exec(query, newUser, newURL, newType, id)
	if err != nil {
		return fmt.Errorf("failed to update webhook with id %d: %w", id, err)
	}
	return nil
}

func GetWebhookByID(db *sql.DB, id int, table string) (*WebhookValidator, error) {
	query := fmt.Sprintf("SELECT ID, USER, URL, Type FROM %s WHERE ID = ?", table)

	row := db.QueryRow(query, id)

	var wh WebhookValidator
	err := row.Scan(&wh.ID, &wh.USER, &wh.URL, &wh.Type)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Pas trouvé
		}
		return nil, fmt.Errorf("failed to get webhook with id %d: %w", id, err)
	}

	return &wh, nil
}
