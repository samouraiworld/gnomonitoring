package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	clerk "github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/chainmanager"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/govdao"
	"gorm.io/gorm"
)

// adminRoleMiddleware checks that the authenticated Clerk user has { "role": "admin" }
// in their publicMetadata. Must be chained after clerkhttp.RequireHeaderAuthorization().
func adminRoleMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := clerk.SessionClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		u, err := clerkuser.Get(r.Context(), claims.Subject)
		if err != nil {
			log.Printf("[admin] failed to fetch user %s: %v", claims.Subject, err)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var meta map[string]interface{}
		if len(u.PublicMetadata) > 0 {
			if err := json.Unmarshal(u.PublicMetadata, &meta); err != nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}

		role, _ := meta["role"].(string)
		if role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// writeJSON encodes v as JSON and sets the appropriate Content-Type header.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[admin] writeJSON encode error: %v", err)
	}
}

// adminStartChain starts the monitoring goroutines for a chain and registers
// the cancel function with chainmanager. Mirrors startChainMonitoring in main.go.
func adminStartChain(db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	ctx, cancel := context.WithCancel(context.Background())
	chainmanager.Register(chainID, cancel)
	go gnovalidator.StartValidatorMonitoring(ctx, db, chainID, chainCfg)
	go govdao.StartGovDAo(ctx, db, chainID, chainCfg.GraphqlEndpoint, chainCfg.RPCEndpoint, chainCfg.GnowebEndpoint)
}

// registerAdminRoutes attaches all /admin/* handlers to mux.
// In production: Clerk JWT validation + admin role check.
// In dev_mode: no auth required.
func registerAdminRoutes(mux *http.ServeMux, db *gorm.DB) {
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		EnableCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		adminRouter(w, r, db)
	})

	if internal.Config.DevMode {
		mux.Handle("/admin/", router)
	} else {
		protected := clerkhttp.RequireHeaderAuthorization()
		mux.Handle("/admin/", protected(adminRoleMiddleware(router)))
	}
}

// adminRouter dispatches admin requests to the right handler based on path + method.
func adminRouter(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	path = strings.TrimSuffix(path, "/")

	switch {
	// 2.1 — Live status
	case path == "/status" && r.Method == http.MethodGet:
		handleGetStatus(w, r, db)

	// 2.2 — Chain management
	case path == "/chains" && r.Method == http.MethodGet:
		handleGetChains(w, r, db)
	case path == "/chains" && r.Method == http.MethodPost:
		handlePostChain(w, r, db)
	case strings.HasPrefix(path, "/chains/") && strings.HasSuffix(path, "/reinit") && r.Method == http.MethodPost:
		chainID := strings.TrimSuffix(strings.TrimPrefix(path, "/chains/"), "/reinit")
		handleReinitChain(w, r, db, chainID)
	case strings.HasPrefix(path, "/chains/") && r.Method == http.MethodPut:
		chainID := strings.TrimPrefix(path, "/chains/")
		handlePutChain(w, r, db, chainID)
	case strings.HasPrefix(path, "/chains/") && r.Method == http.MethodDelete:
		chainID := strings.TrimPrefix(path, "/chains/")
		handleDeleteChain(w, r, db, chainID)

	// config reload
	case path == "/config/reload" && r.Method == http.MethodPost:
		handleConfigReload(w, r)

	// 2.3 — Alert configuration
	case path == "/config/thresholds" && r.Method == http.MethodGet:
		handleGetThresholds(w, r, db)
	case path == "/config/thresholds" && r.Method == http.MethodPut:
		handlePutThresholds(w, r, db)

	// 2.4 — Alert management
	case path == "/alerts" && r.Method == http.MethodGet:
		handleGetAlerts(w, r, db)
	case path == "/alerts" && r.Method == http.MethodDelete:
		handleDeleteAlerts(w, r, db)

	// 2.5 — Moniker management
	case path == "/monikers" && r.Method == http.MethodGet:
		handleGetMonikers(w, r, db)
	case path == "/monikers" && r.Method == http.MethodPost:
		handlePostMoniker(w, r, db)
	case strings.HasPrefix(path, "/monikers/") && r.Method == http.MethodPut:
		chain, addr, ok := splitTwo(strings.TrimPrefix(path, "/monikers/"))
		if !ok {
			http.Error(w, "bad path: expected /admin/monikers/{chain}/{addr}", http.StatusBadRequest)
			return
		}
		handlePutMoniker(w, r, db, chain, addr)
	case strings.HasPrefix(path, "/monikers/") && r.Method == http.MethodDelete:
		chain, addr, ok := splitTwo(strings.TrimPrefix(path, "/monikers/"))
		if !ok {
			http.Error(w, "bad path: expected /admin/monikers/{chain}/{addr}", http.StatusBadRequest)
			return
		}
		handleDeleteMoniker(w, r, db, chain, addr)

	// 2.6 — User management
	case path == "/users" && r.Method == http.MethodGet:
		handleGetUsers(w, r, db)
	case strings.HasPrefix(path, "/users/") && r.Method == http.MethodDelete:
		userID := strings.TrimPrefix(path, "/users/")
		handleDeleteUser(w, r, db, userID)

	// 2.7 — Webhook management
	case path == "/webhooks" && r.Method == http.MethodGet:
		handleGetWebhooks(w, r, db)
	case strings.HasPrefix(path, "/webhooks/govdao/") && strings.HasSuffix(path, "/reset") && r.Method == http.MethodPut:
		idStr := strings.TrimSuffix(strings.TrimPrefix(path, "/webhooks/govdao/"), "/reset")
		handleResetGovDAOWebhook(w, r, db, idStr)
	case strings.HasPrefix(path, "/webhooks/") && r.Method == http.MethodDelete:
		kind, idStr, ok := splitTwo(strings.TrimPrefix(path, "/webhooks/"))
		if !ok {
			http.Error(w, "bad path: expected /admin/webhooks/{type}/{id}", http.StatusBadRequest)
			return
		}
		handleDeleteWebhook(w, r, db, kind, idStr)

	// 2.8 — Telegram management
	case path == "/telegram/chats" && r.Method == http.MethodGet:
		handleGetTelegramChats(w, r, db)
	case strings.HasPrefix(path, "/telegram/chats/") && r.Method == http.MethodDelete:
		idStr := strings.TrimPrefix(path, "/telegram/chats/")
		handleDeleteTelegramChat(w, r, db, idStr)
	case path == "/telegram/subs" && r.Method == http.MethodGet:
		handleGetTelegramSubs(w, r, db)
	case strings.HasPrefix(path, "/telegram/subs/") && r.Method == http.MethodPut:
		idStr := strings.TrimPrefix(path, "/telegram/subs/")
		handlePutTelegramSub(w, r, db, idStr)
	case path == "/telegram/schedules" && r.Method == http.MethodGet:
		handleGetTelegramSchedules(w, r, db)
	case strings.HasPrefix(path, "/telegram/schedules/") && r.Method == http.MethodPut:
		chatIDStr, chainID, ok := splitTwo(strings.TrimPrefix(path, "/telegram/schedules/"))
		if !ok {
			http.Error(w, "bad path: expected /admin/telegram/schedules/{chat_id}/{chain}", http.StatusBadRequest)
			return
		}
		handlePutTelegramSchedule(w, r, db, chatIDStr, chainID)

	// 2.9 — Web user report schedules
	case path == "/schedules" && r.Method == http.MethodGet:
		handleGetSchedules(w, r, db)
	case strings.HasPrefix(path, "/schedules/") && r.Method == http.MethodPut:
		userID := strings.TrimPrefix(path, "/schedules/")
		handlePutSchedule(w, r, db, userID)

	// 2.10 — GovDAO proposals
	case path == "/govdao/proposals" && r.Method == http.MethodGet:
		handleGetGovDAOProposals(w, r, db)

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// splitTwo splits a path segment "a/b" into ("a", "b", true).
// Returns ("", "", false) if the segment does not contain exactly one "/".
func splitTwo(segment string) (string, string, bool) {
	idx := strings.Index(segment, "/")
	if idx < 0 {
		return "", "", false
	}
	return segment[:idx], segment[idx+1:], true
}

// ── 2.1 Live status ──────────────────────────────────────────────────────────

func handleGetStatus(w http.ResponseWriter, _ *http.Request, db *gorm.DB) {
	type chainStatus struct {
		ChainID        string `json:"chain_id"`
		Height         int64  `json:"height"`
		ActiveAlerts   int    `json:"active_alerts"`
		CriticalAlerts int    `json:"critical_alerts"`
		IsActive       bool   `json:"goroutine_active"`
	}

	chains := make([]chainStatus, 0)
	for chainID := range internal.Config.Chains {
		height, _ := database.GetCurrentChainHeight(db, chainID)
		warn, _ := database.GetActiveAlertCount(db, chainID, "WARNING")
		crit, _ := database.GetActiveAlertCount(db, chainID, "CRITICAL")
		chains = append(chains, chainStatus{
			ChainID:        chainID,
			Height:         height,
			ActiveAlerts:   warn,
			CriticalAlerts: crit,
			IsActive:       chainmanager.IsActive(chainID),
		})
	}

	var userCount int64
	db.Model(&database.User{}).Count(&userCount)
	var webhookCount int64
	db.Model(&database.WebhookValidator{}).Count(&webhookCount)
	var govdaoWebhookCount int64
	db.Model(&database.WebhookGovDAO{}).Count(&govdaoWebhookCount)
	var chatCount int64
	db.Model(&database.Telegram{}).Count(&chatCount)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chains":               chains,
		"total_users":          userCount,
		"total_webhooks":       webhookCount + govdaoWebhookCount,
		"total_telegram_chats": chatCount,
	})
}

// ── 2.2 Chain management ─────────────────────────────────────────────────────

func handleGetChains(w http.ResponseWriter, _ *http.Request, db *gorm.DB) {
	type chainInfo struct {
		ID          string `json:"id"`
		RPCEndpoint string `json:"rpc_endpoint"`
		GraphQL     string `json:"graphql"`
		GnoWeb      string `json:"gnoweb"`
		Enabled     bool   `json:"enabled"`
		GoroutineOK bool   `json:"goroutine_active"`
		Height      int64  `json:"height"`
	}

	result := make([]chainInfo, 0, len(internal.Config.Chains))
	for id, cfg := range internal.Config.Chains {
		height, _ := database.GetCurrentChainHeight(db, id)
		result = append(result, chainInfo{
			ID:          id,
			RPCEndpoint: cfg.RPCEndpoint,
			GraphQL:     cfg.GraphqlEndpoint,
			GnoWeb:      cfg.GnowebEndpoint,
			Enabled:     cfg.Enabled,
			GoroutineOK: chainmanager.IsActive(id),
			Height:      height,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func handlePostChain(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	var body struct {
		ID          string `json:"id"`
		RPCEndpoint string `json:"rpc_endpoint"`
		GraphQL     string `json:"graphql"`
		GnoWeb      string `json:"gnoweb"`
		Enabled     bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.ID == "" || body.RPCEndpoint == "" {
		http.Error(w, "id and rpc_endpoint are required", http.StatusBadRequest)
		return
	}

	cfg := &internal.ChainConfig{
		RPCEndpoint:     body.RPCEndpoint,
		GraphqlEndpoint: body.GraphQL,
		GnowebEndpoint:  body.GnoWeb,
		Enabled:         body.Enabled,
	}
	if err := internal.AddChain(body.ID, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := internal.WriteConfig(); err != nil {
		log.Printf("[admin] POST /chains: WriteConfig: %v", err)
		http.Error(w, "chain added but failed to persist config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if body.Enabled {
		adminStartChain(db, body.ID, cfg)
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created", "id": body.ID})
}

func handlePutChain(w http.ResponseWriter, r *http.Request, db *gorm.DB, chainID string) {
	var body struct {
		RPCEndpoint *string `json:"rpc_endpoint"`
		GraphQL     *string `json:"graphql"`
		GnoWeb      *string `json:"gnoweb"`
		Enabled     *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	chainCfg, err := internal.Config.GetChainConfig(chainID)
	if err != nil {
		http.Error(w, "chain not found", http.StatusNotFound)
		return
	}

	if body.RPCEndpoint != nil {
		chainCfg.RPCEndpoint = *body.RPCEndpoint
	}
	if body.GraphQL != nil {
		chainCfg.GraphqlEndpoint = *body.GraphQL
	}
	if body.GnoWeb != nil {
		chainCfg.GnowebEndpoint = *body.GnoWeb
	}
	if body.Enabled != nil {
		if err := internal.SetChainEnabled(chainID, *body.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if *body.Enabled && !chainmanager.IsActive(chainID) {
			adminStartChain(db, chainID, chainCfg)
		} else if !*body.Enabled {
			chainmanager.Cancel(chainID)
		}
	}

	if err := internal.WriteConfig(); err != nil {
		log.Printf("[admin] PUT /chains/%s: WriteConfig: %v", chainID, err)
		http.Error(w, "updated but failed to persist config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "id": chainID})
}

func handleDeleteChain(w http.ResponseWriter, r *http.Request, db *gorm.DB, chainID string) {
	if _, err := internal.Config.GetChainConfig(chainID); err != nil {
		http.Error(w, "chain not found", http.StatusNotFound)
		return
	}
	chainmanager.Cancel(chainID)
	internal.RemoveChain(chainID)
	if err := internal.WriteConfig(); err != nil {
		log.Printf("[admin] DELETE /chains/%s: WriteConfig: %v", chainID, err)
	}
	if err := database.PurgeChainAllData(db, chainID); err != nil {
		http.Error(w, "chain removed but failed to purge DB: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": chainID})
}

func handleReinitChain(w http.ResponseWriter, r *http.Request, db *gorm.DB, chainID string) {
	if _, err := internal.Config.GetChainConfig(chainID); err != nil {
		http.Error(w, "chain not found", http.StatusNotFound)
		return
	}
	if err := database.PurgeChainParticipations(db, chainID); err != nil {
		http.Error(w, "failed to reinit chain: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reinitialized", "id": chainID})
}

func handleConfigReload(w http.ResponseWriter, _ *http.Request) {
	if err := internal.ReloadConfig(); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// ── 2.3 Alert configuration ──────────────────────────────────────────────────

func handleGetThresholds(w http.ResponseWriter, _ *http.Request, db *gorm.DB) {
	configs, err := database.GetAllAdminConfigs(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result := make(map[string]string, len(configs))
	for _, c := range configs {
		result[c.Key] = c.Value
	}
	writeJSON(w, http.StatusOK, result)
}

func handlePutThresholds(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	var pairs map[string]string
	if err := json.NewDecoder(r.Body).Decode(&pairs); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(pairs) == 0 {
		http.Error(w, "no keys provided", http.StatusBadRequest)
		return
	}
	if err := database.SetAdminConfigBatch(db, pairs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	gnovalidator.RefreshThresholds(db)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ── 2.4 Alert management ─────────────────────────────────────────────────────

func handleGetAlerts(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	chainID := r.URL.Query().Get("chain")
	level := r.URL.Query().Get("level")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	limitStr := r.URL.Query().Get("limit")
	limit := 200
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	logs, err := database.GetAllAlertLogs(db, chainID, level, from, to, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func handleDeleteAlerts(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	chainID := r.URL.Query().Get("chain")
	if chainID == "" {
		http.Error(w, "?chain= is required", http.StatusBadRequest)
		return
	}
	if err := database.PurgeAlertLogsByChain(db, chainID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "purged", "chain": chainID})
}

// ── 2.5 Moniker management ───────────────────────────────────────────────────

func handleGetMonikers(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	chainID := r.URL.Query().Get("chain")
	monikers, err := database.GetAllMonikersList(db, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, monikers)
}

func handlePostMoniker(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	var body struct {
		ChainID string `json:"chain_id"`
		Addr    string `json:"addr"`
		Moniker string `json:"moniker"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.ChainID == "" || body.Addr == "" || body.Moniker == "" {
		http.Error(w, "chain_id, addr and moniker are required", http.StatusBadRequest)
		return
	}
	if err := database.UpsertAddrMoniker(db, body.ChainID, body.Addr, body.Moniker); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

func handlePutMoniker(w http.ResponseWriter, r *http.Request, db *gorm.DB, chainID, addr string) {
	var body struct {
		Moniker string `json:"moniker"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Moniker == "" {
		http.Error(w, "moniker is required", http.StatusBadRequest)
		return
	}
	if err := database.UpsertAddrMoniker(db, chainID, addr, body.Moniker); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func handleDeleteMoniker(w http.ResponseWriter, r *http.Request, db *gorm.DB, chainID, addr string) {
	if err := database.DeleteAddrMonikerAdmin(db, chainID, addr); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── 2.6 User management ──────────────────────────────────────────────────────

func handleGetUsers(w http.ResponseWriter, _ *http.Request, db *gorm.DB) {
	users, err := database.GetAllUsers(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func handleDeleteUser(w http.ResponseWriter, r *http.Request, db *gorm.DB, userID string) {
	if userID == "" {
		http.Error(w, "user ID is required", http.StatusBadRequest)
		return
	}
	if err := database.DeleteUser(userID, db); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "user_id": userID})
}

// ── 2.7 Webhook management ───────────────────────────────────────────────────

func handleGetWebhooks(w http.ResponseWriter, _ *http.Request, db *gorm.DB) {
	webhooks, err := database.GetAllWebhooksAdmin(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, webhooks)
}

func handleDeleteWebhook(w http.ResponseWriter, r *http.Request, db *gorm.DB, kind, idStr string) {
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid webhook id", http.StatusBadRequest)
		return
	}
	if err := database.DeleteWebhookAdmin(db, kind, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func handleResetGovDAOWebhook(w http.ResponseWriter, r *http.Request, db *gorm.DB, idStr string) {
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid webhook id", http.StatusBadRequest)
		return
	}
	if err := database.ResetGovDAOLastCheckedID(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

// ── 2.8 Telegram management ──────────────────────────────────────────────────

func handleGetTelegramChats(w http.ResponseWriter, _ *http.Request, db *gorm.DB) {
	chats, err := database.GetAllTelegramChatsAdmin(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, chats)
}

func handleDeleteTelegramChat(w http.ResponseWriter, r *http.Request, db *gorm.DB, idStr string) {
	chatID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid chat id", http.StatusBadRequest)
		return
	}
	if err := database.DeleteTelegramChatCascade(db, chatID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func handleGetTelegramSubs(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	chainID := r.URL.Query().Get("chain")
	chatIDStr := r.URL.Query().Get("chat_id")
	var chatID int64
	if chatIDStr != "" {
		if n, err := strconv.ParseInt(chatIDStr, 10, 64); err == nil {
			chatID = n
		}
	}
	subs, err := database.GetAllTelegramSubsAdmin(db, chainID, chatID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, subs)
}

func handlePutTelegramSub(w http.ResponseWriter, r *http.Request, db *gorm.DB, idStr string) {
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid sub id", http.StatusBadRequest)
		return
	}
	var body struct {
		Activate bool `json:"activate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := database.ToggleTelegramSubAdmin(db, uint(id), body.Activate); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func handleGetTelegramSchedules(w http.ResponseWriter, _ *http.Request, db *gorm.DB) {
	schedules, err := database.GetAllTelegramSchedulesAdmin(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, schedules)
}

func handlePutTelegramSchedule(w http.ResponseWriter, r *http.Request, db *gorm.DB, chatIDStr, chainID string) {
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid chat_id", http.StatusBadRequest)
		return
	}
	var body struct {
		Hour     int    `json:"hour"`
		Minute   int    `json:"minute"`
		Timezone string `json:"timezone"`
		Activate bool   `json:"activate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Timezone == "" {
		body.Timezone = "Europe/Paris"
	}
	if _, err := time.LoadLocation(body.Timezone); err != nil {
		http.Error(w, "invalid timezone: "+body.Timezone, http.StatusBadRequest)
		return
	}
	if err := database.UpdateTelegramScheduleAdmin(db, chatID, chainID, body.Hour, body.Minute, body.Timezone, body.Activate); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ── 2.9 Web user report schedules ────────────────────────────────────────────

func handleGetSchedules(w http.ResponseWriter, _ *http.Request, db *gorm.DB) {
	reports, err := database.GetAllHourReportsAdmin(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, reports)
}

func handlePutSchedule(w http.ResponseWriter, r *http.Request, db *gorm.DB, userID string) {
	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	var body struct {
		Hour     int    `json:"hour"`
		Minute   int    `json:"minute"`
		Timezone string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Timezone == "" {
		body.Timezone = "Europe/Paris"
	}
	if _, err := time.LoadLocation(body.Timezone); err != nil {
		http.Error(w, "invalid timezone: "+body.Timezone, http.StatusBadRequest)
		return
	}
	if err := database.UpdateHourReportAdmin(db, userID, body.Hour, body.Minute, body.Timezone); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ── 2.10 GovDAO proposals ─────────────────────────────────────────────────────

func handleGetGovDAOProposals(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	chainID := r.URL.Query().Get("chain")
	if chainID == "" {
		chainID = internal.Config.DefaultChain
	}
	proposals, err := database.GetStatusofGovdao(db, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, proposals)
}
