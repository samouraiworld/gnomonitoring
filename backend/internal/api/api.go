package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"

	clerk "github.com/clerk/clerk-sdk-go/v2"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/scheduler"
	"gorm.io/gorm"
)

// GetChainIDFromRequest extracts and validates chainID from query parameter
// Returns default chain if not specified
func GetChainIDFromRequest(r *http.Request) (string, error) {
	chainID := r.URL.Query().Get("chain")
	if chainID == "" {
		if internal.Config.DefaultChain != "" {
			chainID = internal.Config.DefaultChain
		} else {
			return "", fmt.Errorf("no chains enabled")
		}
	}
	if err := internal.Config.ValidateChainID(chainID); err != nil {
		return "", err
	}
	return chainID, nil
}

// function for get userid with clerk
func authUserIDFromContext(r *http.Request) (string, error) {
	// Development mode: allow bypassing auth when explicitly enabled
	if internal.Config.DevMode {
		if uid := r.Header.Get("X-Debug-UserID"); uid != "" {
			return uid, nil
		}
		// If no debug header provided, use a default local user ID
		return "local-dev-user", nil
	}

	claims, ok := clerk.SessionClaimsFromContext(r.Context())
	if !ok {
		return "", fmt.Errorf("unauthorized: missing session claims")
	}

	return claims.Subject, nil
}

// ========== GOVDAO ==========

func ListWebhooksHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Optional chain filter: if ?chain= is provided and valid, filter by it.
	chainID := r.URL.Query().Get("chain")
	if chainID != "" {
		if err := internal.Config.ValidateChainID(chainID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	webhooks, err := database.ListWebhooks(db, userID, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(webhooks) == 0 {
		json.NewEncoder(w).Encode(map[string]string{"message": "no webhook found"})
		return
	}

	json.NewEncoder(w).Encode(webhooks)
}

func CreateWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)

	var webhook database.WebhookGovDAO
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	webhook.UserID = userID

	if webhook.UserID == "" || webhook.URL == "" || webhook.Type == "" || webhook.Description == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// Optional chain scoping: validate chain_id if provided.
	if webhook.ChainID != nil && *webhook.ChainID != "" {
		if err := internal.Config.ValidateChainID(*webhook.ChainID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Check if webhook exist
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
		// webhook exist → return 409
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("Webhook already exists"))
		return
	}

	// Send sample proposal for the chosen chain (or default chain if none specified).
	var chainIDForSample string
	if webhook.ChainID != nil && *webhook.ChainID != "" {
		chainIDForSample = *webhook.ChainID
	} else {
		chainIDForSample = internal.Config.DefaultChain
	}

	govdaolist, err := database.GetLastGovDaoInfoByChain(db, chainIDForSample)
	if err != nil {
		http.Error(w, "error get lastID GovDao: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := internal.SendReportGovdao(chainIDForSample, govdaolist.Id, govdaolist.Title, govdaolist.Url, govdaolist.Tx, webhook.Type, webhook.URL); err != nil {
		log.Printf("❌ SendReportGovdao: %v", err)
	}

	// If not exist insert (with chain_id support).
	err = database.InsertWebhook(webhook.UserID, webhook.URL, webhook.Description, webhook.Type, webhook.ChainID, db)
	if err != nil {
		log.Println("Insert error:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Webhook created successfully"))
}

func DeleteWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	idStr := r.URL.Query().Get("id")
	userID, err := authUserIDFromContext(r)
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
	EnableCORS(w, r)
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	webhook.UserID = userID

	// Validate chain_id if provided.
	if webhook.ChainID != nil && *webhook.ChainID != "" {
		if err := internal.Config.ValidateChainID(*webhook.ChainID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	err = database.UpdateMonitoringWebhook(db, webhook.ID, webhook.UserID, webhook.Description, webhook.URL, webhook.Type, webhook.ChainID, "webhook_gov_daos")
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ========== VALIDATOR ==========

func ListMonitoringWebhooksHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Optional chain filter: if ?chain= is provided and valid, filter by it.
	chainID := r.URL.Query().Get("chain")
	if chainID != "" {
		if err := internal.Config.ValidateChainID(chainID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	webhooks, err := database.ListMonitoringWebhooks(db, userID, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(webhooks) == 0 {
		json.NewEncoder(w).Encode(map[string]string{"message": "no webhook found"})
		return
	}

	json.NewEncoder(w).Encode(webhooks)
}

func CreateMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)

	var webhook database.WebhookValidator
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// here
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	webhook.UserID = userID

	// ✅ Check Required fileds
	if webhook.UserID == "" || webhook.URL == "" || webhook.Type == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// Optional chain scoping: read chain_id from body (already decoded into webhook.ChainID).
	// Validate it if provided.
	var chainID string
	if webhook.ChainID != nil && *webhook.ChainID != "" {
		if err := internal.Config.ValidateChainID(*webhook.ChainID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		chainID = *webhook.ChainID
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
	err = database.InsertMonitoringWebhook(webhook.UserID, webhook.URL, webhook.Description, webhook.Type, chainID, db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Webhook created successfully"))
}

func DeleteMonitoringWebhookHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	idStr := r.URL.Query().Get("id")
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
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
	EnableCORS(w, r)
	var webhook database.WebhookValidator
	err := json.NewDecoder(r.Body).Decode(&webhook)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	webhook.UserID = userID

	err = database.UpdateMonitoringWebhook(db, webhook.ID, webhook.UserID, webhook.Description, webhook.URL, webhook.Type, nil, "webhook_validators")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// =======================USER==========================================func CreateUserhandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
func CreateUserhandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)

	var users database.User
	err := json.NewDecoder(r.Body).Decode(&users)
	if err != nil {
		http.Error(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, "Missing or invalid user_id", http.StatusBadRequest)
		return
	}

	// check if user exist
	var exists bool
	err = db.Model(&database.User{}).
		Select("count(*) > 0").
		Where("user_id = ?", userID).
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
	err = database.InsertUser(userID, users.Email, users.Name, db)
	if err != nil {
		http.Error(w, "Insert error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated) // 201
}

func DeleteUserHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
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
	EnableCORS(w, r)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
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
	EnableCORS(w, r)

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
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
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
	EnableCORS(w, r)
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
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	err = database.UpdateHeureReport(db, payload.Hour, payload.Minute, payload.Timezone, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update report hour: %v", err), http.StatusInternalServerError)
		return
	}
	if err := scheduler.Schedulerinstance.ReloadForUser(userID, db); err != nil {
		log.Printf("❌ ReloadForUser: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}
func GetReportHourHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
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
	EnableCORS(w, r)
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
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	input.UserID = userID
	if input.UserID == "" || input.Moniker == "" || input.NameContact == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// F6: validate mention_tag is a numeric Discord/Slack snowflake (or empty)
	for _, c := range input.MentionTag {
		if c < '0' || c > '9' {
			http.Error(w, "Invalid mention_tag: must be numeric", http.StatusBadRequest)
			return
		}
	}

	// F7: verify the referenced webhook belongs to the calling user
	if input.IDwebhook != 0 {
		var count int64
		db.Model(&database.WebhookValidator{}).
			Where("id = ? AND user_id = ?", input.IDwebhook, userID).
			Count(&count)
		if count == 0 {
			http.Error(w, "Webhook not found", http.StatusBadRequest)
			return
		}
	}

	err = database.InsertAlertContact(db, input.UserID, input.Moniker, input.NameContact, input.MentionTag, input.IDwebhook)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to insert alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func GetAlertContactsHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// for get userid with apiclerk
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	EnableCORS(w, r)
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
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// F6: validate mention_tag is numeric (Discord/Slack snowflake) or empty
	for _, c := range data.MentionTag {
		if c < '0' || c > '9' {
			http.Error(w, "Invalid mention_tag: must be numeric", http.StatusBadRequest)
			return
		}
	}

	err = database.UpdateAlertContact(db, data.ID, userID, data.Moniker, data.NameContact, data.MentionTag, data.IDwebhook)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Alert contact updated"))
}
func DeleteAlertContactHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := authUserIDFromContext(r)
	if err != nil {
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
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

	err = database.DeleteAlertContact(db, id, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete alert contact: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Alert contact deleted"))
}

// ====================== Block Height ============
func Getblockheight(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return

	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lastStored, err := gnovalidator.GetLastStoredHeight(db, chainID)
	if err != nil {
		log.Printf("❌ Failed to get latest block height: %v", err)
		return
	}
	json.NewEncoder(w).Encode(map[string]int64{"last_stored": lastStored})
}

// ======================last incident ==================================
func Getlastincident(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		http.Error(w, "Missing period", http.StatusBadRequest)
		return
	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	incident, err := database.GetAlertLog(db, chainID, period)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(incident)
}

// ============================ Participation Rate ========================
func Getarticipation(w http.ResponseWriter, r *http.Request, db *gorm.DB) {

	EnableCORS(w, r)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		http.Error(w, "Missing period", http.StatusBadRequest)
		return
	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	part, err := database.GetCurrentPeriodParticipationRate(db, chainID, period)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get participation rate: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(part)
}

// =========================== Get uptime metrics =============================
func GetUptime(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return

	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	uptime, err := database.UptimeMetricsaddr(db, chainID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get Uptime metrics: %v", err), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(uptime)

}

// =========================== Get Operation time metrics =============================
func GetOperationtime(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return

	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	uptime, err := database.OperationTimeMetricsaddr(db, chainID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get Operation time  metrics: %v", err), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(uptime)

}

// =========================== Get First Seen metrics =============================
func GetFirstSeen(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	firstSeen, err := database.GetFirstSeen(db, chainID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get First Seen metrics: %v", err), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(firstSeen)
}

// ========================= Get tx_contrib
func GetTxContrib(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		http.Error(w, "Missing period", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return

	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	txcontrib, err := database.TxContrib(db, chainID, period)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get TxContrib metrics: %v", err), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(txcontrib)

}

// =========================== Missing Block
// ========================= Get tx_contrib
func GetMissingBlock(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		http.Error(w, "Missing period", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return

	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	txcontrib, err := database.MissingBlock(db, chainID, period)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get Missing Block metrics: %v", err), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(txcontrib)

}

// =========================Info of  rpc gnoweb use ====================

func GetInfo(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type ChainInfo struct {
		RPCEndpoint     string `json:"rpc_endpoint"`
		GraphqlEndpoint string `json:"graphql"`
		GnowebEndpoint  string `json:"gnoweb"`
	}
	type InfoResponse struct {
		EnabledChains []string             `json:"enabled_chains"`
		Chains        map[string]ChainInfo `json:"chains"`
	}
	info := InfoResponse{
		EnabledChains: internal.EnabledChains,
		Chains:        make(map[string]ChainInfo),
	}
	for _, chainID := range internal.EnabledChains {
		cfg, err := internal.Config.GetChainConfig(chainID)
		if err != nil {
			continue
		}
		info.Chains[chainID] = ChainInfo{
			RPCEndpoint:     cfg.RPCEndpoint,
			GraphqlEndpoint: cfg.GraphqlEndpoint,
			GnowebEndpoint:  cfg.GnowebEndpoint,
		}
	}
	json.NewEncoder(w).Encode(info)
}

// ======================ADDR MONIKER====================================
func GetAddrMonikerHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	addr := r.URL.Query().Get("addr")
	if addr == "" {
		http.Error(w, "Missing addr parameter", http.StatusBadRequest)
		return
	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	moniker, err := database.GetMonikerByAddr(db, chainID, addr)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get moniker: %v", err), http.StatusInternalServerError)
		return
	}
	if moniker == "" {
		http.Error(w, "Address not found", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"addr": addr, "moniker": moniker})
}

// ======================CORS=============================================
func EnableCORS(w http.ResponseWriter, r ...*http.Request) {
	origin := ""
	if len(r) > 0 && r[0] != nil {
		origin = r[0].Header.Get("Origin")
	}

	// Match the request origin against the allowed list.
	// If no match and there's only one allowed origin, fall back to that
	// (backward compatible with single-origin configs).
	allowedOrigin := ""
	if len(internal.Config.AllowedOrigins) == 1 {
		allowedOrigin = internal.Config.AllowedOrigins[0]
	} else {
		for _, ao := range internal.Config.AllowedOrigins {
			if ao == origin {
				allowedOrigin = ao
				break
			}
		}
	}

	if allowedOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
	}
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Forwarded-Proto, X-Forwarded-Host")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
}

// ======================== Start API =====================================
func StartWebhookAPI(db *gorm.DB) {
	clerk.SetKey(internal.Config.ClerkSecretKey)
	mux := http.NewServeMux()

	// ====================== Admin routes ===================================
	registerAdminRoutes(mux, db)

	// Create handler wrapper function
	webhookGovDAOHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	webhookValidatorHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	userHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	alertContactsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	usersHHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			UpdateReportHourHandler(w, r, db)
		case http.MethodGet:
			GetReportHourHandler(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	if internal.Config.DevMode {
		// In development mode, don't use Clerk protection
		mux.Handle("/webhooks/govdao", webhookGovDAOHandler)
		mux.Handle("/webhooks/validator", webhookValidatorHandler)
		mux.Handle("/users", userHandler)
		mux.Handle("/alert-contacts", alertContactsHandler)
		mux.Handle("/usersH", usersHHandler)
	} else {
		// In production mode, use Clerk protection
		protected := clerkhttp.RequireHeaderAuthorization()
		mux.Handle("/webhooks/govdao", protected(webhookGovDAOHandler))
		mux.Handle("/webhooks/validator", protected(webhookValidatorHandler))
		mux.Handle("/users", protected(userHandler))
		mux.Handle("/alert-contacts", protected(alertContactsHandler))
		mux.Handle("/usersH", protected(usersHHandler))
	}

	// ====================== Dashboard =================
	mux.HandleFunc("/block_height", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			Getblockheight(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	mux.HandleFunc("/latest_incidents", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			Getlastincident(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	mux.HandleFunc("/Participation", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			Getarticipation(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	mux.HandleFunc("/uptime", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			GetUptime(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	mux.HandleFunc("/operation_time", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			GetOperationtime(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})

	mux.HandleFunc("/first_seen", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			GetFirstSeen(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})

	mux.HandleFunc("/tx_contrib", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			GetTxContrib(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	mux.HandleFunc("/missing_block", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			GetMissingBlock(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})

	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		case http.MethodGet:
			GetInfo(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		}

	})
	mux.HandleFunc("/addr_moniker", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			GetAddrMonikerHandler(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Starting the HTTP server -
	addr := ":" + internal.Config.BackendPort

	log.Printf("Starting Webhook API server on %s\n", addr)

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  90 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}

}
