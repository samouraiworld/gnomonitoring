# Multi-Chain Support: Architecture Design

**Status:** Planning
**Date:** 2026-03-19
**Impact:** MAJOR - Refactoring transversal des 3 composants: Config, DB, Boucles de collecte

---

## 1. VISION

Transformer Gnomonitoring d'un **système mono-chain** en **multi-chain**:
- Support de plusieurs RPC endpoints (test12, gnoland1, etc.)
- Plusieurs indexers GraphQL par chaîne
- Plusieurs gno.web instances
- Sélection de la chaîne via paramètre ou menu interactif
- **Base de données unique SQLite** avec colonne `chain_id` dans les tables critiques
- **Métriques Prometheus** avec label `chain`

---

## 2. SCOPE DU PROJET

### 2.1 IN SCOPE
- ✅ Support de N chaînes Gno parallèles
- ✅ Configuration multi-chain (YAML + Go structs)
- ✅ Boucles realtime indépendantes par chaîne
- ✅ Base de données unique avec `chain_id` scope
- ✅ API REST paramétrisée par chain
- ✅ Métriques Prometheus avec label `chain`
- ✅ Webhooks/Alertes scopées par chaîne
- ✅ Bots Telegram multi-chain
- ✅ Scheduler rapports multi-chain

### 2.2 OUT OF SCOPE
- ❌ Migration automatique des données existantes (manual migration)
- ❌ UI/Frontend changes (hors scope backend)
- ❌ Gestion de consensus inter-chaînes
- ❌ Cross-chain bridges/relays

---

## 3. ARCHITECTURE DE CONFIGURATION

### 3.1 Structure YAML (config.yaml)

**Actuel (mono-chain):**
```yaml
backend_port: "8989"
metrics_port: 8888
rpc_endpoint: "https://rpc.betanet.testnets.gno.land"
graphql: "https://indexer.betanet.testnets.gno.land/graphql/query"
gnoweb: "https://betanet.testnets.gno.land"
dev_mode: true
clerk_secret_key: "..."
token_telegram_validator: "..."
token_telegram_govdao: "..."
```

**Nouveau (multi-chain):**
```yaml
backend_port: "8989"
metrics_port: 8888
dev_mode: true
clerk_secret_key: "..."
token_telegram_validator: "..."
token_telegram_govdao: "..."

chains:
  betanet:
    rpc_endpoint: "https://rpc.betanet.testnets.gno.land"
    graphql: "https://indexer.betanet.testnets.gno.land/graphql/query"
    gnoweb: "https://betanet.testnets.gno.land"
    enabled: true

  gnoland1:
    rpc_endpoint: "https://rpc.gnoland.test/..."
    graphql: "https://indexer.gnoland.test/graphql/query"
    gnoweb: "https://gnoland.test"
    enabled: true

  test12:
    rpc_endpoint: "https://rpc.test12.test/..."
    graphql: "https://indexer.test12.test/graphql/query"
    gnoweb: "https://test12.test"
    enabled: false  # À activer si besoin

allow_origin: "http://localhost:3000,https://example.com"
```

### 3.2 Structure Go (internal/fonction.go)

**Actuel:**
```go
type config struct {
    BackendPort            string
    AllowOrigin           string
    RPCEndpoint           string   // ← Unique
    MetricsPort           int
    Gnoweb                string
    Graphql               string
    ClerkSecretKey        string
    DevMode              bool
    TokenTelegramValidator string
    TokenTelegramGovdao   string
}
var Config config
```

**Nouveau:**
```go
type ChainConfig struct {
    ID               string // "betanet", "gnoland1", "test12"
    RPCEndpoint      string
    GraphqlEndpoint  string
    GnowebEndpoint   string
    Enabled          bool
}

type config struct {
    BackendPort            string
    AllowOrigin           string
    MetricsPort           int
    ClerkSecretKey        string
    DevMode              bool
    TokenTelegramValidator string
    TokenTelegramGovdao   string
    Chains                map[string]*ChainConfig  // ID → Config
}

var Config config
var EnabledChains []string  // Filtered list of chain IDs sorted

// Helper function
func GetChainConfig(chainID string) (*ChainConfig, error) {
    if cfg, ok := Config.Chains[chainID]; ok && cfg.Enabled {
        return cfg, nil
    }
    return nil, fmt.Errorf("chain %q not found or disabled", chainID)
}
```

### 3.3 Fichier: internal/fonction.go - Modifications

**Functions à ajouter:**
```go
func (c *config) GetEnabledChainIDs() []string
func (c *config) GetChain(chainID string) (*ChainConfig, error)
func (c *config) ValidateChainID(chainID string) error
```

**Load order:**
1. Parse YAML
2. Valider que `chains` n'est pas empty
3. Filter les chaînes `enabled: true`
4. Stocker dans `EnabledChains` (sort alphabétique)

---

## 4. MODIFICATIONS DE LA BASE DE DONNÉES

### 4.1 Schema - Nouvelles Colonnes

Tables **CRITIQUES** (doivent avoir `chain_id`):
- `daily_participations` - **PRIMARY**
- `alert_logs`
- `addr_monikers`
- `govdao` (proposals)

Tables **AFFECTÉES** (doivent filtrer par chain):
- `alert_contacts` (add `chain_id` optional pour future usage)
- `webhooks_validator` (add `chain_id` pour filtrer alertes par chaîne)
- `webhooks_govdao` (add `chain_id`)
- `telegram_validator_subs` (add `chain_id`)
- `telegram_hour_reports` (add `chain_id`)

### 4.2 Migration SQLite (db_init.go)

**STEP 1: Alter tables existantes (MIGRATION)**

```go
// ADD COLUMNS
ALTER TABLE daily_participations
  ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';

ALTER TABLE alert_logs
  ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';

ALTER TABLE addr_monikers
  ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';

ALTER TABLE govdao
  ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';

ALTER TABLE webhooks_validator
  ADD COLUMN chain_id TEXT DEFAULT NULL;  // NULL = all chains

ALTER TABLE webhooks_govdao
  ADD COLUMN chain_id TEXT DEFAULT NULL;

ALTER TABLE telegram_validator_subs
  ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';

ALTER TABLE telegram_hour_reports
  ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';

// DROP & RECREATE INDEXES
DROP INDEX idx_dp_addr;
DROP INDEX idx_dp_block_height;
DROP INDEX idx_dp_date;
DROP INDEX idx_dp_addr_participated;
DROP INDEX idx_dp_addr_date;

// NEW COMPOUND INDEXES
CREATE INDEX idx_dp_chain_block_height ON daily_participations(chain_id, block_height);
CREATE INDEX idx_dp_chain_addr ON daily_participations(chain_id, addr);
CREATE INDEX idx_dp_chain_date ON daily_participations(chain_id, date);
CREATE INDEX idx_dp_chain_addr_participated ON daily_participations(chain_id, addr, participated);

CREATE INDEX idx_al_chain_addr ON alert_logs(chain_id, addr);
CREATE INDEX idx_tvs_chain_addr_chatid ON telegram_validator_subs(chain_id, addr, chat_id);

// UNIQUE CONSTRAINTS
ALTER TABLE addr_monikers ADD UNIQUE(chain_id, addr);
ALTER TABLE daily_participations ADD UNIQUE(chain_id, block_height, addr);
```

**STEP 2: Schema pour nouvelles tables (CREATE)**

Aucune nouvelle table, mais modifier les constraints:

```sql
-- Remplacer
UNIQUE(block_height, addr)
-- Par
UNIQUE(chain_id, block_height, addr)

-- Remplacer
UNIQUE(addr)  -- dans addr_monikers
-- Par
UNIQUE(chain_id, addr)
```

### 4.3 Go Model Updates (db_init.go)

**DailyParticipation struct:**
```go
type DailyParticipation struct {
    ID          uint
    ChainID     string    // ← NEW
    BlockHeight int
    Addr        string
    Participated bool
    TxContrib   bool
    Date        time.Time
    Proposer    bool
}

// Table index
func (DailyParticipation) TableName() string {
    return "daily_participations"
}
```

**AlertLog struct:**
```go
type AlertLog struct {
    ID          uint
    ChainID     string    // ← NEW
    Addr        string
    Level       string    // "WARNING", "CRITICAL", "RESOLVED", "MUTED"
    StartHeight int
    EndHeight   int
    SkippedCount int
    CreatedAt   time.Time
}
```

**AddrMoniker struct:**
```go
type AddrMoniker struct {
    ID      uint
    ChainID string    // ← NEW
    Addr    string    // Primary key (with ChainID)
    Moniker string
}
```

**Webhook structures:**
```go
type WebhookValidator struct {
    ID       uint
    UserID   string
    URL      string
    ChainID  *string   // NULL = listen to all chains
    Alerts   []string  // ["WARNING", "CRITICAL"]
    Mentions []string  // Discord role IDs
}

type WebhookGovdao struct {
    ID      uint
    UserID  string
    URL     string
    ChainID *string   // NULL = all chains
}
```

**Telegram structures:**
```go
type TelegramValidatorSub struct {
    ChatID  int64
    ChainID string    // ← NEW
    Addr    string
    Moniker string
}

type TelegramHourReport struct {
    ChatID  int64
    ChainID string    // ← NEW
    Hour    int
    Minute  int
    TZName  string
}
```

---

## 5. BOUCLES REALTIME - COLLECTE DE DONNÉES

### 5.1 Architecture Generalisée (main.go)

**Actuel:**
```go
func main() {
    // ...
    go gnovalidator.StartMetricsUpdater()
    go gnovalidator.StartCollectParticipation()
    go gnovalidator.WatchNewValidators()
    go gnovalidator.WatchValidatorAlerts()
    go govdao.StartGovDAo()
    // ...
}
```

**Nouveau - Spinner par chaîne:**
```go
func main() {
    // Load enabled chains from config
    for _, chainID := range EnabledChains {
        chainCfg, _ := GetChainConfig(chainID)

        // Créer RPC client pour cette chaîne
        go startChainMonitoring(chainID, chainCfg)
    }

    // Server commun
    go api.Start()
    go prometheus.StartMetricsServer()
    // ...
}

func startChainMonitoring(chainID string, cfg *ChainConfig) {
    // Initialize per-chain resources
    rpcClient := rpcclient.NewHTTPClient(cfg.RPCEndpoint)
    client := gnoclient.Client{RPCClient: rpcClient}

    // Per-chain globals (voir section 5.2)
    initChainState(chainID, client)

    // Start per-chain loops
    go gnovalidator.StartMetricsUpdater(chainID)
    go gnovalidator.StartCollectParticipation(chainID, client)
    go gnovalidator.WatchNewValidators(chainID, client)
    go gnovalidator.WatchValidatorAlerts(chainID)
    go govdao.StartGovDAo(chainID, cfg.GraphqlEndpoint)

    log.Printf("Started monitoring for chain: %s", chainID)
}
```

### 5.2 Global State - Scoped par ChainID

**Actuel (gnovalidator_realtime.go):**
```go
var MonikerMap = make(map[string]string)  // addr → moniker
var MonikerMutex sync.RWMutex
var lastProgressHeight = 0
var alertSent = make(map[string]bool)
var restoredNotified = make(map[string]bool)
```

**Nouveau - Nested map:**
```go
// MonikerMap[chainID][addr] = moniker
var MonikerMap = make(map[string]map[string]string)
var MonikerMutex sync.RWMutex

// Per-chain progress
var lastProgressHeight = make(map[string]int)  // chainID → height
var heightMutex sync.RWMutex

// Per-chain alert tracking
var alertSent = make(map[string]map[string]bool)  // chainID → {addr → sent}
var alertMutex sync.RWMutex

var restoredNotified = make(map[string]map[string]bool)
var restoreMutex sync.RWMutex

// Helper functions
func GetMonikerMap(chainID string) map[string]string {
    MonikerMutex.RLock()
    defer MonikerMutex.RUnlock()
    if m, ok := MonikerMap[chainID]; ok {
        return m
    }
    return make(map[string]string)
}

func SetMoniker(chainID, addr, moniker string) {
    MonikerMutex.Lock()
    defer MonikerMutex.Unlock()
    if _, ok := MonikerMap[chainID]; !ok {
        MonikerMap[chainID] = make(map[string]string)
    }
    MonikerMap[chainID][addr] = moniker
}
```

### 5.3 Modifications des Fonctions Clés

**InitMonikerMap (valoper.go)**

**Avant:**
```go
func InitMonikerMap(db *gorm.DB) error {
    // Fetch from valopers.Render
    // Fetch from genesis
    // Merge into MonikerMap
}
```

**Après:**
```go
func InitMonikerMap(db *gorm.DB, chainID string, client *gnoclient.Client) error {
    monikers := make(map[string]string)

    // 1. DB cache (override prioritaire)
    var dbMonikers []AddrMoniker
    db.Where("chain_id = ?", chainID).Find(&dbMonikers)
    for _, m := range dbMonikers {
        monikers[m.Addr] = m.Moniker
    }

    // 2. valopers.Render (Gno realm)
    // Query avec client (pas global Config.RPCEndpoint)
    // ...

    // 3. Genesis
    // ...

    // 4. Active validators
    // ...

    // Update global map
    SetMonikerMap(chainID, monikers)
    return nil
}
```

**CollectParticipation (gnovalidator_realtime.go)**

**Avant:**
```go
func CollectParticipation(db *gorm.DB) {
    for {
        height := lastProgressHeight + 1
        block, _ := client.Block(height)

        // Extract addresses from block.Block.LastCommit.Precommits
        for _, precommit := range block.Block.LastCommit.Precommits {
            db.Create(&DailyParticipation{
                BlockHeight: height,
                Addr: precommit.ValidatorAddress,
                Participated: true,
            })
        }
        lastProgressHeight = height
    }
}
```

**Après:**
```go
func CollectParticipation(db *gorm.DB, chainID string, client *gnoclient.Client) {
    for {
        // Get per-chain height
        heightMutex.RLock()
        height := lastProgressHeight[chainID] + 1
        heightMutex.RUnlock()

        block, _ := client.Block(height)

        batch := []DailyParticipation{}
        for _, precommit := range block.Block.LastCommit.Precommits {
            batch = append(batch, DailyParticipation{
                ChainID:     chainID,        // ← ADD
                BlockHeight: height,
                Addr:        precommit.ValidatorAddress,
                Participated: true,
            })
        }

        db.CreateInBatches(batch, 165)

        heightMutex.Lock()
        lastProgressHeight[chainID] = height
        heightMutex.Unlock()
    }
}
```

**WatchValidatorAlerts (gnovalidator_realtime.go)**

```go
func WatchValidatorAlerts(db *gorm.DB, chainID string) {
    for range time.Tick(1 * time.Minute) {
        // Requête SQL avec WHERE chain_id = ?
        var missingVals []struct {
            Addr    string
            Missed  int
            Start   int
            End     int
        }

        db.Raw(`
            SELECT addr, missed_count as missed, start_height as start, end_height as end
            FROM daily_missing_series
            WHERE chain_id = ? AND ...
        `, chainID).Scan(&missingVals)

        for _, val := range missingVals {
            // Check alert_logs avec WHERE chain_id = ?
            // Dispatch avec chainID scope
            SendValidatorAlert(db, chainID, val.Addr, val.Missed)
        }
    }
}
```

---

## 6. MODIFICATIONS API REST (internal/api/api.go)

### 6.1 Paramètres Requis

**Tous les endpoints retournent maintenant data scopée à une chaîne.**

Options de sélection:
1. **Query parameter:** `?chain=betanet`
2. **Menu interactif:** Frontend affiche dropdown chains
3. **Default:** Utiliser la première chaîne enabled (alphabetique)

### 6.2 Endpoints Détail

#### GET /info
**Avant:**
```json
{
  "rpc_endpoint": "https://rpc.betanet...",
  "gnoweb": "https://betanet...",
  "graphql": "https://indexer.betanet..."
}
```

**Après:**
```json
{
  "chains": {
    "betanet": {
      "rpc_endpoint": "https://rpc.betanet...",
      "gnoweb": "https://betanet...",
      "graphql": "https://indexer.betanet..."
    },
    "gnoland1": { ... }
  },
  "enabled_chains": ["betanet", "gnoland1"]
}
```

#### GET /block_height?chain=betanet
```json
{
  "chain": "betanet",
  "current_height": 12345,
  "last_update": "2026-03-19T10:30:00Z"
}
```

#### GET /Participation?chain=betanet&address=g1xxxx&limit=100
```go
// DB Query
db.Where("chain_id = ? AND addr = ?", chainID, address)
   .Order("block_height DESC")
   .Limit(limit)
   .Find(&records)
```

#### GET /uptime?chain=betanet&address=g1xxxx
```go
// Compute uptime for specific chain
// Query daily_participations WHERE chain_id = ? AND addr = ?
// Return percentage for last N days
```

#### GET /missing_block?chain=betanet
```go
// List validators with missed blocks on specific chain
// Query daily_missing_series WHERE chain_id = ?
```

#### POST /webhooks/validator?chain=betanet
```go
// Create webhook scoped to chain (optionally)
type CreateWebhookReq struct {
    URL     string
    ChainID *string  // nil = all chains
    Alerts  []string
    Mentions []string
}
```

#### GET /addr_moniker?chain=betanet&addr=g1xxxx
```go
// Query addr_monikers WHERE chain_id = ? AND addr = ?
```

### 6.3 Validation Helper

```go
func GetChainIDFromRequest(r *http.Request) (string, error) {
    chainID := r.URL.Query().Get("chain")
    if chainID == "" {
        // Default to first enabled chain
        if len(EnabledChains) > 0 {
            chainID = EnabledChains[0]
        } else {
            return "", fmt.Errorf("no chains enabled")
        }
    }
    if err := Config.ValidateChainID(chainID); err != nil {
        return "", err
    }
    return chainID, nil
}
```

**Usage dans handlers:**
```go
func GetBlockHeightHandler(w http.ResponseWriter, r *http.Request) {
    chainID, err := GetChainIDFromRequest(r)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    // Use chainID in queries
    // ...
}
```

---

## 7. MÉTRIQUES PROMETHEUS (internal/gnovalidator/Prometheus.go)

### 7.1 Ajout du Label `chain`

**Actuel:**
```go
gnoland_missed_blocks (gauge)
  Labels: validator_address, moniker

gnoland_consecutive_missed_blocks (gauge)
  Labels: validator_address, moniker

gnoland_validator_participation_rate (gauge)
  Labels: validator_address, moniker
```

**Nouveau - Ajouter label `chain`:**
```go
gnoland_missed_blocks (gauge)
  Labels: chain, validator_address, moniker
  Example: gnoland_missed_blocks{chain="betanet", validator_address="g1xxx", moniker="Moniker"}

gnoland_consecutive_missed_blocks (gauge)
  Labels: chain, validator_address, moniker

gnoland_validator_participation_rate (gauge)
  Labels: chain, validator_address, moniker
```

### 7.2 Code (Prometheus.go)

**Avant:**
```go
var (
    missedBlocksGauge = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "gnoland_missed_blocks",
        },
        []string{"validator_address", "moniker"},
    )
)

func UpdateMetrics(db *gorm.DB) {
    var metrics []struct {
        Addr string
        Missed int
    }
    db.Raw("SELECT addr, COUNT(*) as missed FROM daily_participations WHERE participated=false GROUP BY addr").Scan(&metrics)

    for _, m := range metrics {
        missedBlocksGauge.WithLabelValues(m.Addr, GetMoniker(m.Addr)).Set(float64(m.Missed))
    }
}
```

**Après:**
```go
var (
    missedBlocksGauge = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "gnoland_missed_blocks",
        },
        []string{"chain", "validator_address", "moniker"},  // ← ADD chain
    )
)

func UpdateMetrics(db *gorm.DB, chainID string) {
    var metrics []struct {
        Addr string
        Missed int
    }
    db.Raw(
        "SELECT addr, COUNT(*) as missed FROM daily_participations WHERE chain_id = ? AND participated=false GROUP BY addr",
        chainID,
    ).Scan(&metrics)

    monikers := GetMonikerMap(chainID)
    for _, m := range metrics {
        moniker := monikers[m.Addr]
        missedBlocksGauge.WithLabelValues(chainID, m.Addr, moniker).Set(float64(m.Missed))
    }
}
```

### 7.3 Scrape Prometheus Config

**Example prometheus.yml:**
```yaml
scrape_configs:
  - job_name: 'gnomonitoring'
    static_configs:
      - targets: ['localhost:8888']
    metric_relabel_configs:
      # Group by chain for dashboards
      - source_labels: [chain]
        regex: (.+)
        action: keep
```

---

## 8. WEBHOOKS ET ALERTES (interno/fonction.go)

### 8.1 Dispatch Alert - Chain Awareness

**Avant:**
```go
func SendAllValidatorAlerts(db *gorm.DB) {
    // Get all active webhooks
    var webhooks []WebhookValidator
    db.Find(&webhooks)

    for _, webhook := range webhooks {
        SendValidatorAlerts(db, webhook)
    }
}

func SendValidatorAlerts(db *gorm.DB, webhook WebhookValidator) {
    // Query alert_logs (no chain filter)
    var alerts []AlertLog
    db.Where("skipped = false").Find(&alerts)

    // POST to webhook
    for _, alert := range alerts {
        PostAlertToWebhook(webhook.URL, alert)
    }
}
```

**Après:**
```go
func SendAllValidatorAlerts(db *gorm.DB) {
    // Get all active webhooks
    var webhooks []WebhookValidator
    db.Find(&webhooks)

    for _, webhook := range webhooks {
        // Webhook peut listen à 1 ou plusieurs chaînes
        chains := webhook.GetScopes()  // nil = all chains
        SendValidatorAlerts(db, webhook, chains)
    }
}

func SendValidatorAlerts(db *gorm.DB, webhook WebhookValidator, chainIDs []string) {
    for _, chainID := range chainIDs {
        // Query avec chain filter
        var alerts []AlertLog
        db.Where("chain_id = ? AND skipped = false", chainID).Find(&alerts)

        for _, alert := range alerts {
            // Enrich avec info chaîne
            PostAlertToWebhook(webhook.URL, alert, chainID)
        }
    }
}

type WebhookValidator struct {
    // ...
    ChainID *string  // nil = all chains, else "betanet" etc
}

func (w WebhookValidator) GetScopes() []string {
    if w.ChainID == nil {
        return EnabledChains  // all chains
    }
    return []string{*w.ChainID}
}
```

### 8.2 Alert Message Format

**Enrichir avec info chaîne:**
```go
func FormatValidatorAlert(alert AlertLog, chainID string, moniker string) string {
    level := "⚠️ WARNING"
    if alert.Level == "CRITICAL" {
        level = "🚨 CRITICAL"
    }

    return fmt.Sprintf(
        "%s **[%s]** Validator **%s** (%s) missed %d blocks (#%d–#%d)",
        level,
        chainID,           // ← ADD
        moniker,
        alert.Addr,
        alert.EndHeight - alert.StartHeight + 1,
        alert.StartHeight,
        alert.EndHeight,
    )
}
```

---

## 9. BOTS TELEGRAM (internal/telegram/validator.go & govdao.go)

### 9.1 Validator Bot - Menu Chaîne

**Avant:**
```go
func HandleCommand(cmd string, params map[string]string) string {
    switch cmd {
    case "/status":
        return GetValidatorStatus(params["address"])
    case "/uptime":
        return GetValidatorUptime(params["address"])
    // ...
    }
}
```

**Après - Chain-aware:**
```go
func HandleCommand(cmd string, params map[string]string) string {
    // Get chain from params or user preference
    chainID := params.Get("chain")
    if chainID == "" {
        // Default user preference from DB
        chatID := GetChatIDFromContext()
        chainID = GetUserChainPreference(chatID)
    }

    if err := Config.ValidateChainID(chainID); err != nil {
        return fmt.Sprintf("Invalid chain: %s", chainID)
    }

    switch cmd {
    case "/status":
        return GetValidatorStatus(chainID, params["address"])
    case "/uptime":
        return GetValidatorUptime(chainID, params["address"])
    case "/chain":
        return ListAvailableChains()
    case "/setchain":
        return SetUserChainPreference(chatID, params["chain"])
    // ...
    }
}
```

### 9.2 Commands Updates

**New Commands:**
- `/chain` - List available chains
- `/setchain test12` - Set preferred chain for this chat

**Updated Commands - Add optional `?chain=` param:**
- `/status ?chain=betanet ?address=g1xxx`
- `/uptime ?chain=betanet ?address=g1xxx`
- `/subscribe ?chain=betanet g1xxx`

**Subscriptions storage:**
```go
// Before: (chat_id, addr)
// After: (chat_id, chain_id, addr) - UNIQUE

db.Create(&TelegramValidatorSub{
    ChatID: chatID,
    ChainID: chainID,  // ← NEW
    Addr: validatorAddr,
})
```

### 9.3 GovDAO Bot - Chain Scope

```go
func StartGovDAo(chainID string, graphqlEndpoint string) {
    // Connect to GovDAO via GraphQL (chain-specific)
    // Monitor proposals on this chain only

    for {
        proposals, _ := QueryGovDAoProposals(graphqlEndpoint)

        for _, proposal := range proposals {
            // Store avec chain_id
            db.Create(&GovdaoProposal{
                ChainID: chainID,
                // ...
            })

            // Dispatch to webhooks listening to this chain
            SendGovDAoAlert(db, chainID, proposal)
        }
    }
}
```

---

## 10. SCHEDULER RAPPORTS (internal/scheduler/scheduler.go)

### 10.1 Per-Chain Scheduling

**Avant:**
```go
func StartAllTelegram(db *gorm.DB) {
    var reports []TelegramHourReport
    db.Find(&reports)

    for _, report := range reports {
        StartForTelegram(report.ChatID, report.Hour, report.Minute, report.TZName)
    }
}

type TelegramHourReport struct {
    ChatID  int64
    Hour    int
    Minute  int
    TZName  string
}
```

**Après:**
```go
func StartAllTelegram(db *gorm.DB) {
    var reports []TelegramHourReport
    db.Find(&reports)

    for _, report := range reports {
        // Per-chain report scheduling
        StartForTelegram(
            report.ChatID,
            report.ChainID,    // ← NEW
            report.Hour,
            report.Minute,
            report.TZName,
        )
    }
}

type TelegramHourReport struct {
    ChatID  int64
    ChainID string    // ← NEW
    Hour    int
    Minute  int
    TZName  string
}

func StartForTelegram(chatID int64, chainID string, hour, minute int, tz string) {
    // Build report pour cette chaîne spécifique
    // Query daily_participations WHERE chain_id = ?
}
```

---

## 11. PLAN D'IMPLÉMENTATION

### Phase 1: Foundation (Semaine 1)
**Objectif:** Config + Database ready

- [ ] Mettre à jour config.yaml structure (YAML + Go structs)
- [ ] Ajouter validation ChainID
- [ ] Créer migrations SQLite (ALTER TABLE + indexes)
- [ ] Tester migrations sur DB existante

**Files à modifier:**
- `internal/fonction.go` - New ChainConfig struct + GetChain helpers
- `config.yaml.template` - Add chains section
- `internal/database/db_init.go` - Update models + migrations
- `internal/database/db_migrations.go` - ← Create if needed

**Tests:**
- Config loading avec multiple chains
- Validation chainID
- Database queries avec chain_id WHERE clause

---

### Phase 2: RPC Clients & State Management (Semaine 2)
**Objectif:** Per-chain RPC clients, nested state maps

- [ ] Refactor MonikerMap → nested map[chainID][addr]
- [ ] Refactor per-chain globals (lastProgressHeight, alertSent, etc)
- [ ] Créer helper functions pour accès thread-safe
- [ ] Update RPC client initialization

**Files à modifier:**
- `internal/gnovalidator/gnovalidator_realtime.go` - Global state refactor
- `main.go` - Spinner per-chain monitoring
- `internal/fonction.go` - Add initChainState() function

**Tests:**
- Concurrent access aux nested maps
- Per-chain height tracking

---

### Phase 3: Data Collection Loops (Semaine 3)
**Objectif:** Boucles realtime scoped par chaîne

- [ ] Update InitMonikerMap(chainID, client)
- [ ] Update CollectParticipation(chainID, client)
- [ ] Update WatchNewValidators(chainID, client)
- [ ] Update WatchValidatorAlerts(chainID)

**Files à modifier:**
- `internal/gnovalidator/valoper.go` - Add chainID param
- `internal/gnovalidator/gnovalidator_realtime.go` - All collection functions
- `internal/gnovalidator/metric.go` - Add chainID param

**Tests:**
- Multiple chains running in parallel
- Data isolation par chain
- SQL WHERE chain_id filters

---

### Phase 4: API + Prometheus (Semaine 4)
**Objectif:** REST endpoints scoped + metrics labels

- [ ] Add chain parameter validation helper
- [ ] Update tous les GET endpoints (add WHERE chain_id = ?)
- [ ] Update POST endpoints (store chain_id)
- [ ] Add Prometheus label "chain"
- [ ] Update metric queries (GROUP BY chain, addr)

**Files à modifier:**
- `internal/api/api.go` - All endpoints
- `internal/gnovalidator/Prometheus.go` - Add chain label
- `internal/database/db_metrics.go` - Add chainID param

**Tests:**
- Endpoint returns correct chain data
- Prometheus metrics have chain label
- Cross-chain data isolation

---

### Phase 5: Webhooks & Alerts (Semaine 5)
**Objectif:** Chain-aware alert dispatch

- [ ] Update WebhookValidator.ChainID
- [ ] Update SendAllValidatorAlerts() → per-chain dispatch
- [ ] Update alert_logs queries (add chain filter)
- [ ] Format alerts avec chain info

**Files à modifier:**
- `internal/fonction.go` - SendValidatorAlerts refactor
- `internal/database/db_init.go` - WebhookValidator model

**Tests:**
- Webhooks listen to correct chains
- Alerts filtered properly

---

### Phase 6: Telegram (Semaine 6)
**Objectif:** Bots support multi-chain

- [ ] Add TelegramValidatorSub.ChainID
- [ ] Add TelegramHourReport.ChainID
- [ ] New commands: /chain, /setchain
- [ ] Update /subscribe, /status, /uptime (add ?chain=)
- [ ] Update GovDAO bot (chain scope)

**Files à modifier:**
- `internal/telegram/validator.go` - Command handler refactor
- `internal/telegram/govdao.go` - Chain scope
- `internal/database/db_init.go` - Telegram models

**Tests:**
- Bot commands work per-chain
- Subscriptions isolated by chain

---

### Phase 7: Scheduler (Semaine 7)
**Objectif:** Reports per-chain

- [ ] Update TelegramHourReport model
- [ ] Update HTTP report scheduler
- [ ] Per-chain report generation

**Files à modifier:**
- `internal/scheduler/scheduler.go` - Chain awareness

**Tests:**
- Reports generated correctly per chain

---

### Phase 8: Integration & Cleanup (Semaine 8)
**Objectif:** Testing, documentation, cleanup

- [ ] Integration tests (multi-chain end-to-end)
- [ ] Update CLAUDE.md avec multi-chain patterns
- [ ] Documentation config.yaml
- [ ] Perf testing avec N=5 chaînes
- [ ] Data migration script (si needed)

---

## 12. POINTS CRITIQUES À SURVEILLER

### 12.1 Migration Données Existantes
**Problème:** Données existantes n'ont pas `chain_id`

**Solution:**
```sql
-- Default existing records to 'betanet'
UPDATE daily_participations SET chain_id = 'betanet' WHERE chain_id IS NULL;
UPDATE alert_logs SET chain_id = 'betanet' WHERE chain_id IS NULL;
-- Etc.
```

### 12.2 Backward Compatibility
**Problème:** Anciens endpoints sans `?chain=` doivent rester functional

**Solution:** Default au premier enabled chain (alphabetique)

```go
func GetChainIDFromRequest(r *http.Request) string {
    if chainID := r.URL.Query().Get("chain"); chainID != "" {
        return chainID  // Use specified
    }
    return EnabledChains[0]  // Default to first
}
```

### 12.3 Performance SQLite
**Risk:** Avec N chaînes × M validateurs, tables deviennent grandes

**Mitigations:**
- Indexes composites: `(chain_id, addr)`, `(chain_id, block_height)`
- Partitioning par date si besoin (future)
- Archive old data: `DELETE FROM daily_participations WHERE date < DATE_SUB(NOW(), INTERVAL 1 YEAR) AND chain_id = ?`

### 12.4 Thread Safety
**Risk:** Nested maps + concurrent updates → race conditions

**Solution:**
- Garder MonikerMutex global (protège toutes les maps)
- Utiliser helpers avec defer unlock
- Test avec `-race` flag

```bash
go test -race ./internal/gnovalidator/...
```

---

## 13. RESTER SUR SQLITE

**Q: Pourquoi SQLite et pas PostgreSQL?**

**Réponses:**
✅ Deployment simple (single file db)
✅ Monitoring à petite/moyenne échelle (< 1M records/chain)
✅ Development facile (testoutils.NewTestDB)
✅ Aucun service external

**Avec N chaînes:**
- 5 chaînes × 10K blocks/jour = ~50K inserts/jour = ~18.25M/year
- SQLite supporte ça sans problème (indexes bien configurés)

**Si future scaling needed:**
- Archivage: `DELETE old records WHERE date < ...`
- Partitioning par date
- Ou migration vers PostgreSQL (après multi-chain stable)

---

## 14. CHECKLIST FINALE

### Code Changes
- [ ] Config structure updated
- [ ] Database migrations applied
- [ ] RPC clients per-chain
- [ ] Global state nested by chainID
- [ ] All collection loops parameterized
- [ ] API endpoints scoped
- [ ] Prometheus metrics labeled
- [ ] Webhooks chain-aware
- [ ] Telegram commands updated
- [ ] Scheduler per-chain

### Testing
- [ ] Unit tests updated (chainID param)
- [ ] Integration tests (multi-chain)
- [ ] Race condition tests (`-race`)
- [ ] Load tests (N chains parallel)
- [ ] Data isolation verified

### Documentation
- [ ] CLAUDE.md updated
- [ ] config.yaml.template documented
- [ ] API swagger/postman updated
- [ ] Telegram commands documented

### Deployment
- [ ] Database migration script
- [ ] Rollback plan
- [ ] Monitoring/alerting setup
- [ ] Config template updated

---

## 15. RÉFÉRENCES & PATTERNS

### Go Patterns Utilisés
- **Nested Maps:** `map[string]map[string]string`
- **Mutex RWLock:** Read-heavy access pattern
- **Context:** Passer chainID via fonction params (non context.Context pour simplicité)
- **Functional Options:** Future pour ChainConfig

### SQL Patterns
- **Upserts:** `ON CONFLICT(chain_id, addr) DO UPDATE SET`
- **Compound indexes:** `(chain_id, other_columns)`
- **GROUP BY:** Ajouter `chain_id` systématiquement

### Testing
- **Per-chain DB:** `testoutils.NewTestDB(t, "test_chain")`
- **Mock RPC:** Extend mocks pour retourner data par chainID
- **Integration:** Start multiple monitoring loops in test

---

**Document Status:** Ready for Architecture Review
**Next Step:** Validation + Planning Session avec team
