package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/scheduler"
	"gorm.io/gorm"
)

func getUserIDFromRequest(r *http.Request) (string, error) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		return "", fmt.Errorf("missing user_id")
	}
	return userID, nil
}

// ========== GOVDAO ==========

func ListWebhooksHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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

func CreateWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w)

	var webhook database.WebhookGovDAO
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if webhook.UserID == "" || webhook.URL == "" || webhook.Type == "" || webhook.Description == "" {

		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	//Check if webhhok exist
	var exists bool
	err = db.Model(&database.WebhookGovDAO{}).
		Select("count(*) > 0").
		Where("user_id = ? AND url = ? AND type = ?", webhook.UserID, webhook.URL, webhook.Type).
		Find(&exists).Error

	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if exists {
		// webhook exist → retunr 409
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("Webhook already exists"))
		return
	}
	// for send ultimate Govdao
	lastid, err := database.GetLastGovDaoProposalID(db)
	if err != nil {
		http.Error(w, "erro get lastID GovDao: "+err.Error(), http.StatusInternalServerError)
		return
	}
	lastid = lastid - 1

	// If not exist insert
	err = database.InsertWebhook(webhook.UserID, webhook.URL, webhook.Description, webhook.Type, db)
	if err != nil {
		log.Println("Insert error:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Webhook created successfully"))
}

func DeleteWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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

func UpdateWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	var webhook database.WebhookGovDAO
	EnableCORS(w)
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = database.UpdateMonitoringWebhook(db, webhook.ID, webhook.UserID, webhook.Description, webhook.URL, webhook.Type, "webhook_gov_daos")
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ========== VALIDATOR ==========

func ListMonitoringWebhooksHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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

func CreateMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w)

	var webhook database.WebhookValidator
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// ✅ Check Required fileds
	if webhook.UserID == "" || webhook.URL == "" || webhook.Type == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// ✅ Check if webhook exist
	var exists bool
	err = db.Model(&database.WebhookValidator{}).
		Select("count(*) > 0").
		Where("user_id = ? AND url = ? AND type = ?", webhook.UserID, webhook.URL, webhook.Type).
		Find(&exists).Error

	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if exists {
		// ✅ Webhook  present → retourne 409
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("Webhook already exists"))
		return
	}

	// ✅ If not exist insert
	err = database.InsertMonitoringWebhook(webhook.UserID, webhook.URL, webhook.Description, webhook.Type, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Webhook created successfully"))
}

func DeleteMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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

func UpdateMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w)
	var webhook database.WebhookValidator
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = database.UpdateMonitoringWebhook(db, webhook.ID, webhook.UserID, webhook.Description, webhook.URL, webhook.Type, "webhook_validators")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// =======================USER==========================================func CreateUserhandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
func CreateUserhandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w)

	var users database.User
	err := json.NewDecoder(r.Body).Decode(&users)
	if err != nil {
		http.Error(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// check if user exist
	var exists bool
	err = db.Model(&database.User{}).
		Select("count(*) > 0").
		Where("user_id = ?", users.UserID).
		Find(&exists).Error

	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if exists {
		http.Error(w, "User already exists", http.StatusConflict) // 409
		return
	}

	// Insertion
	err = database.InsertUser(users.UserID, users.Email, users.Name, db)
	if err != nil {
		http.Error(w, "Insert error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated) // 201
}

func DeleteUserHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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

func GetUserHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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
func UpdateUserHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w)

	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var user struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	err := json.NewDecoder(r.Body).Decode(&user)
	if err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	userID, err := getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	err = database.UpdateUser(db, user.Name, user.Email, userID)
	if err != nil {
		http.Error(w, "Failed to update user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// =====================Hour Report ================================
func UpdateReportHourHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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
func GetReportHourHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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

func InsertAlertContactHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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
		IDwebhook   int    `json:"id_webhook"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if input.UserID == "" || input.Moniker == "" || input.NameContact == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	err := database.InsertAlertContact(db, input.UserID, input.Moniker, input.NameContact, input.MentionTag, input.IDwebhook)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to insert alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func GetAlertContactsHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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
func UpdateAlertContactHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {

	EnableCORS(w)
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var data struct {
		ID          int    `json:"id"`
		UserID      string `json:"user_id"`
		Moniker     string `json:"moniker"`
		NameContact string `json:"namecontact"`
		MentionTag  string `json:"mention_tag"`
		IDwebhook   int    `json:"id_webhook"`
	}
	bodyBytes, _ := io.ReadAll(r.Body)
	log.Println("Raw body:", string(bodyBytes))
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	err := database.UpdateAlertContact(db, data.ID, data.UserID, data.Moniker, data.NameContact, data.MentionTag, data.IDwebhook)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Alert contact updated"))
}
func DeleteAlertContactHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
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

// ====================== Block Height ============
func Getblockheight(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return

	}
	lastStored, err := gnovalidator.GetLastStoredHeight(db)
	if lastStored == 0 {
		log.Printf("❌ Failed to get latest block height: %v", err)
		return
	}
	json.NewEncoder(w).Encode(lastStored)
}

// ======================last incident ==================================
func Getlastincident(w http.ResponseWriter, r *http.Request, db *gorm.DB) {

	incident, err := database.GetAlertLog(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(incident)
}

// ======================CORS=============================================
func EnableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", internal.Config.AllowOrigin)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
}

// ======================== Start API =====================================
func StartWebhookAPI(db *gorm.DB) {
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
	// ====================== Dashboard =================
	http.HandleFunc("/block_height", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			Getblockheight(w, r, db)
		case http.MethodOptions:
			EnableCORS(w)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	http.HandleFunc("/lastest_incidents", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			Getlastincident(w, r, db)
		case http.MethodOptions:
			EnableCORS(w)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	// Starting the HTTP server -
	addr := ":" + internal.Config.BackendPort

	log.Printf("Starting Webhook API server on %s\n", addr)

	err := http.ListenAndServe(addr, nil)
	if err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}

}
