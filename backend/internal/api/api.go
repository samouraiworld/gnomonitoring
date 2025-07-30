package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/scheduler"
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
	EnableCORS(w)
	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	webhooks, err := database.ListWebhooks(db, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(webhooks)
}

func CreateWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)

	var webhook database.WebhookGovDao
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// ✅ Vérifier les champs obligatoires
	if webhook.USER == "" || webhook.URL == "" || webhook.Type == "" || webhook.DESCRIPTION == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// ✅ Vérifier si le webhook existe déjà
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM webhooks_govdao  WHERE user_id = ? AND url = ? AND type = ?)`
	err = db.QueryRow(query, webhook.USER, webhook.URL, webhook.Type).Scan(&exists)
	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if exists {
		// ✅ Webhook déjà présent → retourne 409
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("Webhook already exists"))
		return
	}

	// ✅ Si pas existant, on insère
	err = database.InsertWebhook(webhook.USER, webhook.URL, webhook.DESCRIPTION, webhook.Type, db)
	if err != nil {
		log.Println("Insert error:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Webhook created successfully"))
}

func DeleteWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
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

	err = database.DeleteWebhook(id, userID, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func UpdateWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var webhook database.WebhookGovDao
	EnableCORS(w)
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = database.UpdateMonitoringWebhook(db, webhook.ID, webhook.USER, webhook.DESCRIPTION, webhook.URL, webhook.Type, "webhooks_govdao")
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ========== VALIDATOR ==========

func ListMonitoringWebhooksHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	webhooks, err := database.ListMonitoringWebhooks(db, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(webhooks)
}

func CreateMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)

	var webhook database.WebhookValidator
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// ✅ Vérification des champs obligatoires
	if webhook.USER == "" || webhook.URL == "" || webhook.Type == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// ✅ Vérifier si le webhook existe déjà
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM webhooks_validator WHERE user_id = ? AND url = ? AND type = ?)`
	err = db.QueryRow(query, webhook.USER, webhook.URL, webhook.Type).Scan(&exists)
	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if exists {
		// ✅ Webhook déjà présent → retourne 409
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("Webhook already exists"))
		return
	}

	// ✅ Si pas existant, on insère
	err = database.InsertMonitoringWebhook(webhook.USER, webhook.URL, webhook.DESCRIPTION, webhook.Type, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Webhook created successfully"))
}

func DeleteMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
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

	err = database.DeleteMonitoringWebhook(id, userID, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func UpdateMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
	var webhook database.WebhookValidator
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = database.UpdateMonitoringWebhook(db, webhook.ID, webhook.USER, webhook.DESCRIPTION, webhook.URL, webhook.Type, "webhooks_validator")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// =======================USER==========================================func CreateUserhandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
func CreateUserhandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)

	var users database.Users
	err := json.NewDecoder(r.Body).Decode(&users)
	if err != nil {
		http.Error(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Vérifie si l'utilisateur existe déjà
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM users WHERE user_id = ?)"
	err = db.QueryRow(query, users.USER_ID).Scan(&exists)
	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if exists {
		http.Error(w, "User already exists", http.StatusConflict) // 409
		return
	}

	// Insertion
	err = database.InsertUser(users.USER_ID, users.EMAIL, users.NAME, db)
	if err != nil {
		http.Error(w, "Insert error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated) // 201
}

func DeleteUserHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, "Missing or invalid user_id", http.StatusBadRequest)
		return
	}

	err = database.DeleteUser(userID, db)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete user: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func GetUserHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}

	user, err := database.GetUserById(db, userID)
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
	EnableCORS(w)

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
	err = database.UpdateUser(db, user.Name, user.Email, userID)
	if err != nil {
		http.Error(w, "Failed to update user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// =====================Hour Report ================================
func UpdateReportHourHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Hour     int    `json:"hour"`
		Minute   int    `json:"minute"`
		Timezone string `json:"timezone"`
		UserID   string `json:"user_id"`
	}
	err := json.NewDecoder(r.Body).Decode(&payload)
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	err = database.UpdateHeureReport(db, payload.Hour, payload.Minute, payload.Timezone, payload.UserID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update report hour: %v", err), http.StatusInternalServerError)
		return
	}
	scheduler.Schedulerinstance.ReloadForUser(payload.UserID, db)
	w.WriteHeader(http.StatusOK)
}
func GetReportHourHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}
	hr, err := database.GetHourReport(db, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get report hour: %v", err), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(hr)
}

// ======================Alert Contact ================================

func InsertAlertContactHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
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

	err := database.InsertAlertContact(db, input.UserID, input.Moniker, input.NameContact, input.MentionTag)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to insert alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func GetAlertContactsHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}

	contacts, err := database.ListAlertContacts(db, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list alert contacts: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(contacts)
}
func UpdateAlertContactHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
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

	err := database.UpdateAlertContact(db, data.ID, data.Moniker, data.NameContact, data.MentionTag)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Alert contact updated"))
}
func DeleteAlertContactHandler(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	EnableCORS(w)
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

	err = database.DeleteAlertContact(db, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Alert contact deleted"))
}

// ======================CORS=============================================
func EnableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", internal.Config.AllowOrigin)
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
			EnableCORS(w)
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
			EnableCORS(w)
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
			EnableCORS(w)
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
		case http.MethodGet:
			GetReportHourHandler(w, r, db)
		case http.MethodOptions:
			EnableCORS(w)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})

	// Démarrage du serveur HTTP - **C’EST ICI QUE TU COMMENCES À ÉCOUTER LE PORT**
	addr := ":" + internal.Config.BackendPort
	// Optionnel : log pour debug
	log.Printf("Starting Webhook API server on %s\n", addr)

	err := http.ListenAndServe(addr, nil)
	if err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}

}
