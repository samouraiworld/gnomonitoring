package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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
		log.Println(err)
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
		log.Println(err)
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

// =======================USER==========================================
func CreateUserhandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	var users Users
	err := json.NewDecoder(r.Body).Decode(&users)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = InsertUser(users.USER_ID, users.EMAIL, users.NAME, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func DeleteUserHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, "Missing or invalid user_id", http.StatusBadRequest)
		return
	}

	err = DeleteUser(userID, db)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete user: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func UpdateReportHourHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Hour   int    `json:"hour"`
		Minute int    `json:"minute"`
		UserID string `json:"user_id"`
	}
	err := json.NewDecoder(r.Body).Decode(&payload)
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	err = UpateHeureReport(db, payload.Hour, payload.Minute, payload.UserID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update report hour: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func GetUserHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}

	user, err := GetUserById(db, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get user: %v", err), http.StatusInternalServerError)
		return
	}
	if user == nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(user)
}
func UpdateUserHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)

	// Vérifie que la méthode est bien PUT
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Décodage du JSON envoyé dans le body
	var user struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	err := json.NewDecoder(r.Body).Decode(&user)
	if err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Récupère le user_id (via auth ou query param selon ton système)
	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	// Mise à jour en base
	err = UpdateUser(db, user.Name, user.Email, userID)
	if err != nil {
		http.Error(w, "Failed to update user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ======================Alert Contact ================================

func InsertAlertContactHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		UserID      string `json:"user_id"`
		Moniker     string `json:"moniker"`
		NameContact string `json:"namecontact"`
		MentionTag  string `json:"mention_tag"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if input.UserID == "" || input.Moniker == "" || input.NameContact == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	err := InsertAlertContact(db, input.UserID, input.Moniker, input.NameContact, input.MentionTag)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to insert alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func GetAlertContactsHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}

	contacts, err := ListAlertContacts(db, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list alert contacts: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(contacts)
}
func UpdateAlertContactHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var data struct {
		ID          int    `json:"id"`
		Moniker     string `json:"moniker"`
		NameContact string `json:"namecontact"`
		MentionTag  string `json:"mention_tag"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	err := UpdateAlertContact(db, data.ID, data.Moniker, data.NameContact, data.MentionTag)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Alert contact updated"))
}
func DeleteAlertContactHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	enableCORS(w)
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		http.Error(w, "Missing id", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid id", http.StatusBadRequest)
		return
	}

	err = DeleteAlertContact(db, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Alert contact deleted"))
}

// ======================CORS=============================================
func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", Config.AllowOrigin)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
}

// ======================== Start API =====================================
func StartWebhookAPI(db *sql.DB) {
	// Webhooks GOVDAO
	http.HandleFunc("/webhooks/govdao", func(w http.ResponseWriter, r *http.Request) {
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
	// USER
	http.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			GetUserHandler(w, r, db)
		case http.MethodDelete:
			DeleteUserHandler(w, r, db)
		case http.MethodPut:
			UpdateUserHandler(w, r, db)
		case http.MethodPost:
			CreateUserhandler(w, r, db)
		case http.MethodOptions:
			enableCORS(w)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	// ===================Alert Contact

	http.HandleFunc("/alert-contacts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			InsertAlertContactHandler(w, r, db)
		case http.MethodGet:
			GetAlertContactsHandler(w, r, db)
		case http.MethodPut:
			UpdateAlertContactHandler(w, r, db)
		case http.MethodDelete:
			DeleteAlertContactHandler(w, r, db)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	// ==================Update hour of report =======================
	http.HandleFunc("/usersH", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodPut:
			UpdateReportHourHandler(w, r, db)
		case http.MethodOptions:
			enableCORS(w)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	// Démarrage du serveur HTTP - **C’EST ICI QUE TU COMMENCES À ÉCOUTER LE PORT**
	addr := ":" + Config.BackendPort
	// Optionnel : log pour debug
	log.Printf("Starting Webhook API server on %s\n", addr)

	err := http.ListenAndServe(addr, nil)
	if err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}

}
