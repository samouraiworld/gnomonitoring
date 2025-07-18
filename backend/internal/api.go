package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

func getUserIDFromRequest(r *http.Request) (string, error) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		return "", fmt.Errorf("missing user_id")
	}
	return userID, nil
}

// ========== GOVDAO ==========

func ListWebhooksHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	webhooks, err := ListWebhooks(db, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(webhooks)
}

func CreateWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	var webhook WebhookGovDao
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = InsertWebhook(webhook.USER, webhook.URL, webhook.Type, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func DeleteWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	idStr := r.URL.Query().Get("id")
	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid id", http.StatusBadRequest)
		return
	}

	err = DeleteWebhook(id, userID, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func UpdateWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var webhook WebhookGovDao
	enableCORS(w)
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = UpdateMonitoringWebhook(db, webhook.ID, webhook.USER, webhook.URL, webhook.Type, "webhooks_govdao")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ========== VALIDATOR ==========

func ListMonitoringWebhooksHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	webhooks, err := ListMonitoringWebhooks(db, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(webhooks)
}

func CreateMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	var webhook WebhookValidator
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = InsertMonitoringWebhook(webhook.USER, webhook.URL, webhook.Type, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func DeleteMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	idStr := r.URL.Query().Get("id")
	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid id", http.StatusBadRequest)
		return
	}

	err = DeleteMonitoringWebhook(id, userID, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func UpdateMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	var webhook WebhookValidator
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = UpdateMonitoringWebhook(db, webhook.ID, webhook.USER, webhook.URL, webhook.Type, "webhooks_validator")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", Config.AllowOrigin)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
}
func StartWebhookAPI(db *sql.DB) {
	// Webhooks GOVDAO
	http.HandleFunc("/webhooksgovdao", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			ListWebhooksHandler(w, r, db)
		case http.MethodPost:
			CreateWebhookHandler(w, r, db)
		case http.MethodPut:
			UpdateWebhookHandler(w, r, db)
		case http.MethodDelete:
			DeleteWebhookHandler(w, r, db)
		case http.MethodOptions:
			enableCORS(w)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	// Webhooks VALIDATOR
	http.HandleFunc("/webhooks/validator", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			ListMonitoringWebhooksHandler(w, r, db)
		case http.MethodPost:
			CreateMonitoringWebhookHandler(w, r, db)
		case http.MethodPut:
			UpdateMonitoringWebhookHandler(w, r, db)
		case http.MethodDelete:
			DeleteMonitoringWebhookHandler(w, r, db)
		case http.MethodOptions:
			enableCORS(w)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

}
