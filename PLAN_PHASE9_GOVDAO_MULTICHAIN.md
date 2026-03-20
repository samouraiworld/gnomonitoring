# PLAN D'IMPLÉMENTATION - Phase 9: GovDAO Multi-Chain Support

**Status:** READY FOR IMPLEMENTATION
**Date:** 2026-03-19
**Scope:** GovDAO bot multi-chain with per-chat chain preferences + chain-aware webhooks

---

## 📋 TABLE DES MATIÈRES

1. [Résumé des Modifications](#résumé)
2. [Phase 1: Database Functions](#phase-1-database-functions)
3. [Phase 2: Telegram State Management](#phase-2-telegram-state-management)
4. [Phase 3: Startup Hydration](#phase-3-startup-hydration)
5. [Phase 4: GovDAO Command Handlers](#phase-4-govdao-command-handlers)
6. [Phase 5: main.go Integration](#phase-5-maingo-integration)
7. [Phase 6: Bug Fix](#phase-6-bug-fix)
8. [Phase 7: API - GovDAO Webhooks](#phase-7-api---govdao-webhooks)
9. [Phase 8: Database - Webhook Functions](#phase-8-database---webhook-functions)
10. [Tests](#tests)
11. [Documentation](#documentation)

---

## Résumé

### Files à Modifier (9 fichiers)

```
backend/internal/database/db_telegram.go        (+2 fonctions)
backend/internal/database/db.go                 (+1 fonction, modifications)
backend/internal/telegram/validator.go          (+4 fonctions pour govdao)
backend/internal/telegram/telegram.go           (+1 bloc hydration)
backend/internal/telegram/govdao.go             (+2 handlers, modifications)
backend/internal/api/api.go                     (+modifications x3 handlers)
backend/internal/govdao/govdao.go               (+1 correction bug)
backend/main.go                                 (+modifications x2 appels)
feat/feat-multi-chain.md                        (+mise à jour doc)
```

### Résumé des Changements

- ✅ 9 fichiers modifiés
- ✅ 3 nouvelles fonctions DB
- ✅ 4 nouvelles fonctions Telegram state
- ✅ 2 nouveaux handlers Telegram (/chain, /setchain)
- ✅ 3 handlers API modifiés
- ✅ 1 bug corrigé
- ✅ Webhooks Discord/Slack chain-aware pour GovDAO
- ✅ Tests à ajouter

---

## PHASE 1: Database Functions

### Fichier: `internal/database/db_telegram.go`

**Ajouter 2 nouvelles fonctions après `UpdateChatChain` (ligne ~100):**

```go
// GetAllGovdaoChatChains retourne un map de chat_id -> chain_id pour tous les chats govdao
// Utilisé au démarrage pour hydrater les préférences de chaîne govdao
func GetAllGovdaoChatChains(db *gorm.DB) (map[int64]string, error) {
	var rows []Telegram
	if err := db.Where("type = ?", "govdao").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("GetAllGovdaoChatChains: %w", err)
	}
	result := make(map[int64]string, len(rows))
	for _, r := range rows {
		if r.ChainID != "" {
			result[r.ChatID] = r.ChainID
		}
	}
	return result, nil
}

// UpdateGovdaoChatChain persiste la préférence de chaîne pour un chat govdao
// Appelé après /setchain pour sauvegarder la sélection de l'utilisateur
func UpdateGovdaoChatChain(db *gorm.DB, chatID int64, chainID string) error {
	res := db.Model(&Telegram{}).
		Where("chat_id = ? AND type = ?", chatID, "govdao").
		Update("chain_id", chainID)
	return res.Error
}
```

**Modifier 3 fonctions de query govdao - ajouter paramètre `chainID string` et `WHERE chain_id = ?`:**

```go
// GetStatusofGovdao
// AVANT: func GetStatusofGovdao(db *gorm.DB) ([]Govdao, error) {
// APRÈS:
func GetStatusofGovdao(db *gorm.DB, chainID string) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT id, url, title, tx, status, chain_id
		FROM govdaos
		WHERE chain_id = ?
		ORDER BY id DESC;`

	err := db.Raw(query, chainID).Scan(&results).Error
	return results, err
}

// GetLastExecute
// AVANT: func GetLastExecute(db *gorm.DB) ([]Govdao, error) {
// APRÈS:
func GetLastExecute(db *gorm.DB, chainID string) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT id, url, title, tx, status, chain_id
		FROM govdaos
		WHERE chain_id = ? AND status = 'ACCEPTED'
		ORDER BY id DESC;`

	err := db.Raw(query, chainID).Scan(&results).Error
	return results, err
}

// GetLastPorposal
// AVANT: func GetLastPorposal(db *gorm.DB) ([]Govdao, error) {
// APRÈS:
func GetLastPorposal(db *gorm.DB, chainID string) ([]Govdao, error) {
	var results []Govdao
	query := `
		SELECT id, url, title, tx, status, chain_id
		FROM govdaos
		WHERE chain_id = ?
		ORDER BY id DESC
		LIMIT 1;`

	err := db.Raw(query, chainID).Scan(&results).Error
	return results, err
}
```

---

## PHASE 2: Telegram State Management

### Fichier: `internal/telegram/validator.go`

**Ajouter après la déclaration de `chatChainState` (après ligne ~40):**

```go
// govdao-specific chain state (separate from validator bot)
// Utilisé pour stocker les préférences de chaîne par chat pour le bot govdao
var (
	govdaoChatChainState = make(map[int64]string)
	govdaoChatChainMu    sync.RWMutex
)

func getGovdaoActiveChain(chatID int64, defaultChainID string) string {
	govdaoChatChainMu.RLock()
	id, ok := govdaoChatChainState[chatID]
	govdaoChatChainMu.RUnlock()
	if ok && id != "" {
		return id
	}
	return defaultChainID
}

func setGovdaoActiveChain(chatID int64, chainID string) {
	govdaoChatChainMu.Lock()
	defer govdaoChatChainMu.Unlock()
	if chainID == "" {
		delete(govdaoChatChainState, chatID)
	} else {
		govdaoChatChainState[chatID] = chainID
	}
}
```

---

## PHASE 3: Startup Hydration

### Fichier: `internal/telegram/telegram.go`

**Modifier `StartCommandLoop` - ajouter hydration govdao après le bloc validator (après ligne ~337):**

Trouver ce bloc:
```go
if typeChatid == "validator" {
    prefs, err := database.GetAllChatChains(db)
    // ...
}
```

Ajouter après:
```go
// Hydrate govdaoChatChainState from DB for govdao bots on startup.
if typeChatid == "govdao" {
	prefs, err := database.GetAllGovdaoChatChains(db)
	if err != nil {
		log.Printf("⚠️ StartCommandLoop: failed to hydrate govdao chatChainState: %v", err)
	} else {
		for chatID, cid := range prefs {
			setGovdaoActiveChain(chatID, cid)
		}
		log.Printf("ℹ️ Hydrated chain preferences for %d govdao chats", len(prefs))
	}
}
```

---

## PHASE 4: GovDAO Command Handlers

### Fichier: `internal/telegram/govdao.go`

**Modifier signature de `BuildTelegramGovdaoHandlers` (ligne ~60):**

AVANT:
```go
func BuildTelegramGovdaoHandlers(token string, db *gorm.DB) map[string]func(int64, string) {
```

APRÈS:
```go
func BuildTelegramGovdaoHandlers(token string, db *gorm.DB, defaultChainID string, enabledChains []string) map[string]func(int64, string) {
```

**Ajouter 2 nouveaux handlers dans la map (après les handlers existants):**

```go
"/chain": func(chatID int64, _ string) {
	current := getGovdaoActiveChain(chatID, defaultChainID)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Current chain: <code>%s</code>\n\nAvailable chains:\n", html.EscapeString(current)))
	for _, id := range enabledChains {
		b.WriteString(fmt.Sprintf("• <code>%s</code>\n", html.EscapeString(id)))
	}
	b.WriteString("\nUse <code>/setchain chain=&lt;id&gt;</code> to switch.")
	_ = SendMessageTelegram(token, chatID, b.String())
},

"/setchain": func(chatID int64, args string) {
	params := parseParams(args)
	requested := strings.TrimSpace(params["chain"])
	if requested == "" {
		_ = SendMessageTelegram(token, chatID, "Usage: <code>/setchain chain=&lt;chain_id&gt;</code>")
		return
	}
	if !validateChainID(requested, enabledChains) {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Unknown chain <code>%s</code>. Available:\n", html.EscapeString(requested)))
		for _, id := range enabledChains {
			b.WriteString(fmt.Sprintf("• <code>%s</code>\n", html.EscapeString(id)))
		}
		_ = SendMessageTelegram(token, chatID, b.String())
		return
	}
	setGovdaoActiveChain(chatID, requested)
	if err := database.UpdateGovdaoChatChain(db, chatID, requested); err != nil {
		log.Printf("⚠️ UpdateGovdaoChatChain chat_id=%d: %v", chatID, err)
	}
	_ = SendMessageTelegram(token, chatID, fmt.Sprintf("Chain set to <code>%s</code>.", html.EscapeString(requested)))
},
```

**Modifier les handlers existants pour utiliser `getGovdaoActiveChain` et passer chainID aux DB calls:**

AVANT (exemple pour `/status`):
```go
"/status": func(chatID int64, args string) {
	msg, _ := formatStatusProposal(db, 10)
	_ = SendMessageTelegram(token, chatID, msg)
},
```

APRÈS:
```go
"/status": func(chatID int64, args string) {
	chainID := getGovdaoActiveChain(chatID, defaultChainID)
	msg, _ := formatStatusProposal(db, chainID, 10)
	_ = SendMessageTelegram(token, chatID, msg)
},
```

Appliquer le même pattern pour `/executedproposals` et `/lastproposal`.

**Modifier les fonctions de formatage (ajouter paramètre `chainID string`):**

```go
func formatStatusProposal(db *gorm.DB, chainID string, limit int) (msg string, err error) {
	status, err := database.GetStatusofGovdao(db, chainID)
	// ... rest du code
}

func formatExecutedProposal(db *gorm.DB, chainID string, limit int) (msg string, err error) {
	executed, err := database.GetLastExecute(db, chainID)
	// ... rest du code
}

func formatLastProposal(db *gorm.DB, chainID string) (msg string, err error) {
	last, err := database.GetLastPorposal(db, chainID)
	// ... rest du code
}
```

---

## PHASE 5: main.go Integration

### Fichier: `main.go`

**Modifier l'appel à `BuildTelegramGovdaoHandlers` (~ligne 91):**

AVANT:
```go
handlersgovdao := telegram.BuildTelegramGovdaoHandlers(internal.Config.TokenTelegramGovdao, db)
```

APRÈS:
```go
handlersgovdao := telegram.BuildTelegramGovdaoHandlers(
	internal.Config.TokenTelegramGovdao,
	db,
	internal.Config.DefaultChain,
	internal.EnabledChains,
)
```

**Modifier l'appel à `StartCommandLoop` pour govdao (~ligne 94):**

AVANT:
```go
telegram.StartCommandLoop(ctxgovdao, internal.Config.TokenTelegramGovdao, handlersgovdao, nil, "govdao", db)
```

APRÈS:
```go
telegram.StartCommandLoop(ctxgovdao, internal.Config.TokenTelegramGovdao, handlersgovdao, nil, "govdao", db, internal.Config.DefaultChain)
```

---

## PHASE 6: Bug Fix

### Fichier: `internal/govdao/govdao.go`

**Corriger le mauvais token Telegram à la ligne ~521:**

AVANT:
```go
telegram.MsgTelegram(msgT, internal.Config.TokenTelegramValidator, "govdao", db)
```

APRÈS:
```go
telegram.MsgTelegram(msgT, internal.Config.TokenTelegramGovdao, "govdao", db)
```

---

## PHASE 7: API - GovDAO Webhooks

### Fichier: `internal/api/api.go`

**1. Modifier `ListWebhooksHandler` (ligne ~59):**

```go
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
```

**2. Modifier `CreateWebhookHandler` (ligne ~81):**

```go
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

	// Optional chain scoping: validate if provided
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
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("Webhook already exists"))
		return
	}

	// Send sample proposal (most recent for chosen chain or default)
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
	if err := internal.SendReportGovdao(govdaolist.Id, govdaolist.Title, govdaolist.Url, govdaolist.Tx, webhook.Type, webhook.URL); err != nil {
		log.Printf("❌ SendReportGovdao: %v", err)
	}

	// If not exist insert (with chain_id support)
	err = database.InsertWebhook(webhook.UserID, webhook.URL, webhook.Description, webhook.Type, webhook.ChainID, db)
	if err != nil {
		log.Println("Insert error:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Webhook created successfully"))
}
```

**3. Modifier `UpdateWebhookHandler` (ligne ~167):**

```go
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

	// Validate chain_id if provided
	if webhook.ChainID != nil && *webhook.ChainID != "" {
		if err := internal.Config.ValidateChainID(*webhook.ChainID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	err = database.UpdateMonitoringWebhook(db, webhook.ID, webhook.UserID, webhook.Description, webhook.URL, webhook.Type, webhook.ChainID, "webhook_gov_d_a_os")
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
```

---

## PHASE 8: Database - Webhook Functions

### Fichier: `internal/database/db.go` (ou `db_init.go`)

**1. Ajouter nouvelle fonction pour query govdao par chaîne:**

```go
// GetLastGovDaoInfoByChain retourne le dernier govdao proposal pour une chaîne spécifique
func GetLastGovDaoInfoByChain(db *gorm.DB, chainID string) (*Govdao, error) {
	var result Govdao
	query := `
		SELECT id, url, title, tx, status, chain_id
		FROM govdaos
		WHERE chain_id = ?
		ORDER BY id DESC
		LIMIT 1
	`
	err := db.Raw(query, chainID).Scan(&result).Error
	if err != nil {
		return nil, fmt.Errorf("GetLastGovDaoInfoByChain: %w", err)
	}
	return &result, nil
}
```

**2. Modifier `ListWebhooks` signature:**

AVANT:
```go
func ListWebhooks(db *gorm.DB, userID string) ([]WebhookGovDAO, error) {
```

APRÈS:
```go
func ListWebhooks(db *gorm.DB, userID string, chainID string) ([]WebhookGovDAO, error) {
	query := db.Where("user_id = ?", userID)

	// Optional chain filtering: if chainID provided, filter by it or NULL
	if chainID != "" {
		query = query.Where("chain_id = ? OR chain_id IS NULL", chainID)
	}

	var webhooks []WebhookGovDAO
	if err := query.Find(&webhooks).Error; err != nil {
		return nil, err
	}
	return webhooks, nil
}
```

**3. Modifier `InsertWebhook` signature:**

AVANT:
```go
func InsertWebhook(userID, url, description, type_ string, db *gorm.DB) error {
```

APRÈS:
```go
func InsertWebhook(userID, url, description, type_ string, chainID *string, db *gorm.DB) error {
	webhook := WebhookGovDAO{
		UserID:      userID,
		URL:         url,
		Description: description,
		Type:        type_,
		ChainID:     chainID,
	}
	return db.Create(&webhook).Error
}
```

**4. Modifier `UpdateMonitoringWebhook` signature:**

AVANT:
```go
func UpdateMonitoringWebhook(db *gorm.DB, id int, userID, description, url, type_, table string) error {
```

APRÈS:
```go
func UpdateMonitoringWebhook(db *gorm.DB, id int, userID, description, url, type_ string, chainID *string, table string) error {
	updates := map[string]interface{}{
		"description": description,
		"url":         url,
		"type":        type_,
		"chain_id":    chainID,
	}
	return db.Model(&WebhookGovDAO{}).Where("id = ? AND user_id = ?", id, userID).Updates(updates).Error
}
```

---

## Tests

### À Ajouter

**Fichier: `internal/database/db_telegram_test.go`**

Ajouter 3 tests:

```go
// TestGetAllGovdaoChatChains_ReturnsGovdaoRows
func TestGetAllGovdaoChatChains_ReturnsGovdaoRows(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert a govdao chat
	_, err := database.InsertChatID(db, 2001, "govdao", "chainA")
	require.NoError(t, err)

	// Insert another govdao chat with different chain
	_, err = database.InsertChatID(db, 2002, "govdao", "chainB")
	require.NoError(t, err)

	// Insert a validator chat (should not be returned)
	_, err = database.InsertChatID(db, 2003, "validator", "betanet")
	require.NoError(t, err)

	// Get all govdao chat chains
	chains, err := database.GetAllGovdaoChatChains(db)
	require.NoError(t, err)

	// Should have only the two govdao chats
	assert.Equal(t, 2, len(chains), "should return exactly 2 govdao chats")
	assert.Equal(t, "chainA", chains[2001])
	assert.Equal(t, "chainB", chains[2002])
	_, exists := chains[2003]
	assert.False(t, exists, "validator chat should not be included")
}

// TestUpdateGovdaoChatChain_PersistsChain
func TestUpdateGovdaoChatChain_PersistsChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	const chatID int64 = 2004

	// Insert a govdao chat with default chain
	_, err := database.InsertChatID(db, chatID, "govdao", "chainA")
	require.NoError(t, err)

	// Update the chain to chainB
	err = database.UpdateGovdaoChatChain(db, chatID, "chainB")
	require.NoError(t, err)

	// Verify the update persisted via GetAllGovdaoChatChains
	chains, err := database.GetAllGovdaoChatChains(db)
	require.NoError(t, err)
	assert.Equal(t, "chainB", chains[chatID])
}

// TestUpdateGovdaoChatChain_NonExistentChatIsNoOp
func TestUpdateGovdaoChatChain_NonExistentChatIsNoOp(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Try to update a govdao chat that was never inserted
	err := database.UpdateGovdaoChatChain(db, 9998, "chainA")
	// Should not error — just a no-op
	require.NoError(t, err)

	// Verify no row was created
	chains, err := database.GetAllGovdaoChatChains(db)
	require.NoError(t, err)
	assert.Equal(t, 0, len(chains))
}
```

**Fichier: `internal/telegram/govdao_test.go` (NEW)**

Créer file avec tests govdao:

```go
package telegram

import (
	"testing"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetGovdaoChainState() {
	govdaoChatChainMu.Lock()
	defer govdaoChatChainMu.Unlock()
	govdaoChatChainState = make(map[int64]string)
}

// TestGetGovdaoActiveChain_DefaultsToDefault
func TestGetGovdaoActiveChain_DefaultsToDefault(t *testing.T) {
	resetGovdaoChainState()

	const chatID int64 = 7001
	const defaultChain = "betanet"

	got := getGovdaoActiveChain(chatID, defaultChain)
	assert.Equal(t, defaultChain, got, "unknown chatID should return the default chain")
}

// TestSetGovdaoActiveChain_ValidChain
func TestSetGovdaoActiveChain_ValidChain(t *testing.T) {
	resetGovdaoChainState()

	const chatID int64 = 7002
	const defaultChain = "betanet"
	const newChain = "testnet"

	setGovdaoActiveChain(chatID, newChain)
	got := getGovdaoActiveChain(chatID, defaultChain)
	assert.Equal(t, newChain, got, "getGovdaoActiveChain should reflect the value set by setGovdaoActiveChain")
}

// TestHandleSetChainCommand_PersistsToDB_Govdao
func TestHandleSetChainCommand_PersistsToDB_Govdao(t *testing.T) {
	resetGovdaoChainState()

	db := testoutils.NewTestDB(t)

	enabledChains := []string{"chainA", "chainB"}
	const defaultChain = "chainA"
	const chatID int64 = 7003

	// First, insert the govdao chat with the default chain.
	_, err := database.InsertChatID(db, chatID, "govdao", defaultChain)
	require.NoError(t, err)

	handlers := BuildTelegramGovdaoHandlers("", db, defaultChain, enabledChains)
	setChainHandler := handlers["/setchain"]

	// Call /setchain to switch to chainB.
	assert.NotPanics(t, func() { setChainHandler(chatID, "chain=chainB") })

	// Verify the update was persisted to the database.
	chains, err := database.GetAllGovdaoChatChains(db)
	require.NoError(t, err)
	assert.Equal(t, "chainB", chains[chatID], "chain should be persisted to database after /setchain")
}
```

---

## Documentation

### Fichier: `feat/feat-multi-chain.md`

**Sections à mettre à jour:**

1. ✅ **Phase Status Header (ligne 3):** "PHASES 1-9 COMPLETED"
2. ✅ **Current Status (ligne 49-50):** Ajouter Phase 9
3. ✅ **Add Phase 9 Section:** Nouvelle section complète après Phase 8
4. ✅ **Known Limitations (ligne 2144):** Enlever limitation GovDAO, mettre à jour
5. ✅ **Section 9.4: GovDAO Bot Multi-Chain:** Nouvelle subsection
6. ✅ **main.go Examples (ligne 391):** Mettre à jour avec StartGovDAo par chaîne
7. ✅ **Webhooks Documentation:** Ajouter section sur webhooks govdao chain-aware

**Phase 9 Description à ajouter:**

```markdown
### Phase 9: GovDAO Multi-Chain Support (COMPLETED - 2026-03-19)

**Objective:** Per-chain GovDAO monitoring with per-chat chain preferences and chain-aware webhooks

**Deliverables:**
- ✅ Task 9.1: Per-chat GovDAO chain state management with persistent storage
- ✅ Task 9.2: /chain and /setchain commands for GovDAO bot
- ✅ Task 9.3: GovDAO query functions chain-filtered
- ✅ Task 9.4: Chain-aware Discord/Slack webhooks for GovDAO
- ✅ Task 9.5: API endpoints support chain parameter for GovDAO webhooks
- ✅ Task 9.6: Bug fix: use correct Telegram token for GovDAO alerts
- ✅ Task 9.7: Created tests for GovDAO multi-chain isolation

**Files Modified (9 files):**
- `internal/database/db_telegram.go` - GetAllGovdaoChatChains, UpdateGovdaoChatChain
- `internal/database/db.go` - GetLastGovDaoInfoByChain, modified webhook functions
- `internal/telegram/validator.go` - GovDAO chain state helpers
- `internal/telegram/telegram.go` - GovDAO hydration at startup
- `internal/telegram/govdao.go` - /chain, /setchain handlers, chain-filtered queries
- `internal/api/api.go` - Chain-aware webhooks for GovDAO
- `internal/govdao/govdao.go` - Bug fix: correct token
- `main.go` - Pass DefaultChain and EnabledChains to govdao handlers
- `feat/feat-multi-chain.md` - Documentation update

**Test Results:**
✅ All 3 new govdao database tests passing
✅ All 2 new govdao handler tests passing
✅ All existing tests still passing (zero regressions)
✅ Code compiles cleanly

**Known Limitations Removed:**
- "GovDAO Bot Single-Chain Scope" limitation is REMOVED - GovDAO is now fully multi-chain
```

---

## Ordre d'Implémentation

1. **Phase 1:** Database functions (db_telegram.go, db.go)
2. **Phase 2:** Telegram state management (validator.go)
3. **Phase 3:** Startup hydration (telegram.go)
4. **Phase 4:** GovDAO handlers (govdao.go)
5. **Phase 5:** main.go integration
6. **Phase 6:** Bug fix (govdao.go token)
7. **Phase 7:** API webhooks (api.go)
8. **Phase 8:** Database webhook functions (db.go)
9. **Tests:** Add all tests
10. **Documentation:** Update feat-multi-chain.md

---

## Checklist d'Implémentation

- [ ] Phase 1 - Database functions
- [ ] Phase 2 - Telegram state management
- [ ] Phase 3 - Startup hydration
- [ ] Phase 4 - GovDAO handlers
- [ ] Phase 5 - main.go integration
- [ ] Phase 6 - Bug fix
- [ ] Phase 7 - API webhooks
- [ ] Phase 8 - Database webhook functions
- [ ] Tests added
- [ ] Documentation updated
- [ ] `go build ./...` successful
- [ ] `go test ./...` passing (35+ tests)
- [ ] Code review ready

---

**READY FOR IMPLEMENTATION** ✅
