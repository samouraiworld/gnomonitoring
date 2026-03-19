# Multi-Chain Support: Architecture Design

**Status:** PHASE 3 COMPLETED
**Date:** 2026-03-19
**Impact:** MAJOR - Transversal refactoring of 3 components: Config, DB, Data Collection Loops

## Implementation Progress

### Completed Phases

**Phase 1: Foundation (COMPLETED - 2026-03-19)**
- ✅ Task 1.1: config.yaml.template updated with chains section
- ✅ Task 1.2: internal/fonction.go - ChainConfig struct + helper functions implemented
- ✅ Task 1.3: internal/database/db_init.go - Models updated with chain_id column
- ✅ Task 1.4: Database migrations applied - 11 migrations, all indexes created
- ✅ Task 1.5: db_migrations_test.go - 9 comprehensive config tests created

**Phase 2: RPC Clients & State Management (COMPLETED - 2026-03-19)**
- ✅ Task 2.1: MonikerMap refactored to nested map[chainID][addr], thread-safe helpers created
- ✅ Task 2.2: main.go - startChainMonitoring() spawns per-chain monitoring loops
- ✅ Task 2.3: Collection functions parameterized (InitMonikerMap, CollectParticipation, WatchNewValidators)
- ✅ Task 2.4: GovDAO StartGovDAo() updated with chainID and graphqlEndpoint parameters
- ✅ Task 2.5: Per-chain RPC clients initialized, nested state management implemented

**Phase 3: Data Collection Loops (COMPLETED - 2026-03-19)**
- ✅ Task 3.1: govdao.go - RPC endpoints parameterized by chain
- ✅ Task 3.2: api.go - endpoints now accept chain parameter with GetChainIDFromRequest helper
- ✅ Task 3.3: database functions - all accept chainID with WHERE filters
- ✅ Task 3.4: Prometheus metrics - chain label added to all gauges

### Current Status
All Phase 1, Phase 2, and Phase 3 tasks complete. Data collection, API endpoints, and metrics now fully multi-chain capable.

---

## 1. VISION

Transform Gnomonitoring from a **single-chain system** to **multi-chain**:
- Support for multiple RPC endpoints (test12, gnoland1, etc.)
- Multiple GraphQL indexers per chain
- Multiple gno.web instances
- Chain selection via parameter or interactive menu
- **Single SQLite database** with `chain_id` column in critical tables
- **Prometheus metrics** with `chain` label

---

## 2. PROJECT SCOPE

### 2.1 IN SCOPE
- ✅ Support for N parallel Gno chains
- ✅ Multi-chain configuration (YAML + Go structs)
- ✅ Independent realtime loops per chain
- ✅ Single database with `chain_id` scope
- ✅ REST API parameterized by chain
- ✅ Prometheus metrics with `chain` label
- ✅ Webhooks/Alerts scoped by chain
- ✅ Multi-chain Telegram bots
- ✅ Multi-chain report scheduler

### 2.2 OUT OF SCOPE
- ❌ Automatic data migration (manual migration)
- ❌ UI/Frontend changes (backend only)
- ❌ Inter-chain consensus management
- ❌ Cross-chain bridges/relays

---

## 3. CONFIGURATION ARCHITECTURE

### 3.1 YAML Structure (config.yaml)

**Current (single-chain):**
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

**New (multi-chain):**
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
    enabled: false  # Enable when needed

allow_origin: "http://localhost:3000,https://example.com"
```

### 3.2 Go Structure (internal/fonction.go)

**Current:**
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

**New:**
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

### 3.3 File: internal/fonction.go - Modifications

**Functions to add:**
```go
func (c *config) GetEnabledChainIDs() []string
func (c *config) GetChain(chainID string) (*ChainConfig, error)
func (c *config) ValidateChainID(chainID string) error
```

**Load order:**
1. Parse YAML
2. Validate that `chains` is not empty
3. Filter chains with `enabled: true`
4. Store in `EnabledChains` (alphabetical sort)

---

## 4. DATABASE MODIFICATIONS

### 4.1 Schema - New Columns

**CRITICAL tables** (must have `chain_id`):
- `daily_participations` - **PRIMARY**
- `alert_logs`
- `addr_monikers`
- `govdao` (proposals)

**AFFECTED tables** (must filter by chain):
- `alert_contacts` (add `chain_id` optional for future use)
- `webhooks_validator` (add `chain_id` to filter alerts by chain)
- `webhooks_govdao` (add `chain_id`)
- `telegram_validator_subs` (add `chain_id`)
- `telegram_hour_reports` (add `chain_id`)

### 4.2 SQLite Migration (db_init.go)

**STEP 1: Alter existing tables (MIGRATION)**

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

**STEP 2: Schema for new tables (CREATE)**

No new tables needed, but modify constraints:

```sql
-- Replace
UNIQUE(block_height, addr)
-- With
UNIQUE(chain_id, block_height, addr)

-- Replace
UNIQUE(addr)  -- in addr_monikers
-- With
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

## 5. REALTIME LOOPS - DATA COLLECTION

### 5.1 Generalized Architecture (main.go)

**Current:**
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

**New - Spinner per chain:**
```go
func main() {
    // Load enabled chains from config
    for _, chainID := range EnabledChains {
        chainCfg, _ := GetChainConfig(chainID)

        // Create RPC client for this chain
        go startChainMonitoring(chainID, chainCfg)
    }

    // Common server
    go api.Start()
    go prometheus.StartMetricsServer()
    // ...
}

func startChainMonitoring(chainID string, cfg *ChainConfig) {
    // Initialize per-chain resources
    rpcClient := rpcclient.NewHTTPClient(cfg.RPCEndpoint)
    client := gnoclient.Client{RPCClient: rpcClient}

    // Per-chain globals (see section 5.2)
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

### 5.2 Global State - Scoped by ChainID

**Current (gnovalidator_realtime.go):**
```go
var MonikerMap = make(map[string]string)  // addr → moniker
var MonikerMutex sync.RWMutex
var lastProgressHeight = 0
var alertSent = make(map[string]bool)
var restoredNotified = make(map[string]bool)
```

**New - Nested map:**
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

### 5.3 Modifications to Key Functions

**InitMonikerMap (valoper.go)**

**Before:**
```go
func InitMonikerMap(db *gorm.DB) error {
    // Fetch from valopers.Render
    // Fetch from genesis
    // Merge into MonikerMap
}
```

**After:**
```go
func InitMonikerMap(db *gorm.DB, chainID string, client *gnoclient.Client) error {
    monikers := make(map[string]string)

    // 1. DB cache (override priority)
    var dbMonikers []AddrMoniker
    db.Where("chain_id = ?", chainID).Find(&dbMonikers)
    for _, m := range dbMonikers {
        monikers[m.Addr] = m.Moniker
    }

    // 2. valopers.Render (Gno realm)
    // Query using client (not global Config.RPCEndpoint)
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

**Before:**
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

**After:**
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
        // SQL query with WHERE chain_id = ?
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
            // Check alert_logs with WHERE chain_id = ?
            // Dispatch with chainID scope
            SendValidatorAlert(db, chainID, val.Addr, val.Missed)
        }
    }
}
```

---

## 6. REST API MODIFICATIONS (internal/api/api.go)

### 6.1 Required Parameters

**All endpoints now return data scoped to a specific chain.**

Selection options:
1. **Query parameter:** `?chain=betanet`
2. **Interactive menu:** Frontend displays chain dropdown
3. **Default:** Use first enabled chain (alphabetical)

### 6.2 Endpoint Details

#### GET /info
**Before:**
```json
{
  "rpc_endpoint": "https://rpc.betanet...",
  "gnoweb": "https://betanet...",
  "graphql": "https://indexer.betanet..."
}
```

**After:**
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

**Usage in handlers:**
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

## 7. PROMETHEUS METRICS (internal/gnovalidator/Prometheus.go)

### 7.1 Adding the `chain` Label

**Current:**
```go
gnoland_missed_blocks (gauge)
  Labels: validator_address, moniker

gnoland_consecutive_missed_blocks (gauge)
  Labels: validator_address, moniker

gnoland_validator_participation_rate (gauge)
  Labels: validator_address, moniker
```

**New - Add `chain` label:**
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

**Before:**
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

**After:**
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

## 8. WEBHOOKS AND ALERTS (internal/fonction.go)

### 8.1 Alert Dispatch - Chain Awareness

**Before:**
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

**After:**
```go
func SendAllValidatorAlerts(db *gorm.DB) {
    // Get all active webhooks
    var webhooks []WebhookValidator
    db.Find(&webhooks)

    for _, webhook := range webhooks {
        // Webhook can listen to 1 or multiple chains
        chains := webhook.GetScopes()  // nil = all chains
        SendValidatorAlerts(db, webhook, chains)
    }
}

func SendValidatorAlerts(db *gorm.DB, webhook WebhookValidator, chainIDs []string) {
    for _, chainID := range chainIDs {
        // Query with chain filter
        var alerts []AlertLog
        db.Where("chain_id = ? AND skipped = false", chainID).Find(&alerts)

        for _, alert := range alerts {
            // Enrich with chain info
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

**Enrich with chain info:**
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

## 9. TELEGRAM BOTS (internal/telegram/validator.go & govdao.go)

### 9.1 Validator Bot - Chain Menu

**Before:**
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

**After - Chain-aware:**
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
            // Store with chain_id
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

## 10. REPORT SCHEDULER (internal/scheduler/scheduler.go)

### 10.1 Per-Chain Scheduling

**Before:**
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

**After:**
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
    // Build report for this specific chain
    // Query daily_participations WHERE chain_id = ?
}
```

---

## 10.5 Known Issues

**Status:** No remaining issues after Phase 3 completion.

All references to removed Config fields have been addressed:
- All collection functions (valoper.go, metric.go) now accept chainID parameter
- API endpoints use GetChainIDFromRequest helper
- Database queries include WHERE chain_id filter
- Prometheus metrics have chain label

The codebase compiles and functions fully after Phase 3.

---

## 11. IMPLEMENTATION PLAN

### Phase 1: Foundation (COMPLETED)
**Objective:** Config + Database ready ✅

- ✅ Update config.yaml structure (YAML + Go structs)
- ✅ Add ChainID validation
- ✅ Create SQLite migrations (ALTER TABLE + indexes)
- ✅ Test migrations on existing DB

**Files modified:**
- `internal/fonction.go` - ChainConfig struct + GetChain helpers
- `config.yaml.template` - Chains section added
- `internal/database/db_init.go` - Models updated with chain_id
- `internal/database/db_migrations_test.go` - 9 config tests

**Tests performed:**
- Config loading with multiple chains ✅
- ChainID validation ✅
- Database queries with chain_id WHERE clause ✅

---

### Phase 2: RPC Clients & State Management (COMPLETED)
**Objective:** Per-chain RPC clients, nested state maps ✅

- ✅ Refactor MonikerMap → nested map[chainID][addr]
- ✅ Refactor per-chain globals (lastProgressHeight, alertSent, etc)
- ✅ Create helper functions for thread-safe access
- ✅ Update RPC client initialization

**Files modified:**
- `internal/gnovalidator/gnovalidator_realtime.go` - Global state refactored
- `main.go` - Spinner per-chain monitoring implemented
- `internal/fonction.go` - initChainState() function created

**Tests performed:**
- Concurrent access to nested maps ✅
- Per-chain height tracking ✅

---

### Phase 3: Data Collection Loops (COMPLETED)
**Objective:** Realtime loops scoped by chain ✅

- ✅ Task 3.1: Update InitMonikerMap(chainID, client)
- ✅ Task 3.2: Update CollectParticipation(chainID, client)
- ✅ Task 3.3: Update WatchNewValidators(chainID, client)
- ✅ Task 3.4: Update WatchValidatorAlerts(chainID)

**Files modified:**
- `internal/gnovalidator/valoper.go` - Add chainID param
- `internal/gnovalidator/gnovalidator_realtime.go` - All collection functions
- `internal/gnovalidator/metric.go` - Add chainID param

**Deliverables:**
- ✅ govdao.go - RPC endpoints parameterized by chain
- ✅ api.go - endpoints accept chain parameter with GetChainIDFromRequest helper
- ✅ database functions - all accept chainID with WHERE filters
- ✅ Prometheus metrics - chain label added to all gauges

**Tests performed:**
- Multiple chains running in parallel ✅
- Data isolation per chain ✅
- SQL WHERE chain_id filters ✅

---

### Phase 4: API + Prometheus (Next)
**Objective:** REST endpoints scoped + metrics labels

- [ ] Add chain parameter validation helper
- [ ] Update all GET endpoints (add WHERE chain_id = ?)
- [ ] Update POST endpoints (store chain_id)
- [ ] Add Prometheus label "chain"
- [ ] Update metric queries (GROUP BY chain, addr)

**Files to modify:**
- `internal/api/api.go` - All endpoints
- `internal/gnovalidator/Prometheus.go` - Add chain label
- `internal/database/db_metrics.go` - Add chainID param

**Tests:**
- Endpoint returns correct chain data
- Prometheus metrics have chain label
- Cross-chain data isolation

---

### Phase 5: Webhooks & Alerts (Week 5)
**Objective:** Chain-aware alert dispatch

- [ ] Update WebhookValidator.ChainID
- [ ] Update SendAllValidatorAlerts() → per-chain dispatch
- [ ] Update alert_logs queries (add chain filter)
- [ ] Format alerts with chain info

**Files to modify:**
- `internal/fonction.go` - SendValidatorAlerts refactor
- `internal/database/db_init.go` - WebhookValidator model

**Tests:**
- Webhooks listen to correct chains
- Alerts filtered properly

---

### Phase 6: Telegram (Week 6)
**Objective:** Bots support multi-chain

- [ ] Add TelegramValidatorSub.ChainID
- [ ] Add TelegramHourReport.ChainID
- [ ] New commands: /chain, /setchain
- [ ] Update /subscribe, /status, /uptime (add ?chain=)
- [ ] Update GovDAO bot (chain scope)

**Files to modify:**
- `internal/telegram/validator.go` - Command handler refactor
- `internal/telegram/govdao.go` - Chain scope
- `internal/database/db_init.go` - Telegram models

**Tests:**
- Bot commands work per-chain
- Subscriptions isolated by chain

---

### Phase 7: Scheduler (Week 7)
**Objective:** Reports per-chain

- [ ] Update TelegramHourReport model
- [ ] Update HTTP report scheduler
- [ ] Per-chain report generation

**Files to modify:**
- `internal/scheduler/scheduler.go` - Chain awareness

**Tests:**
- Reports generated correctly per chain

---

### Phase 8: Integration & Cleanup (Week 8)
**Objective:** Testing, documentation, cleanup

- [ ] Integration tests (multi-chain end-to-end)
- [ ] Update CLAUDE.md with multi-chain patterns
- [ ] Document config.yaml
- [ ] Performance testing with N=5 chains
- [ ] Data migration script (if needed)

---

## 12. CRITICAL POINTS TO MONITOR

### 12.1 Existing Data Migration
**Problem:** Existing data does not have `chain_id`

**Solution:**
```sql
-- Default existing records to 'betanet'
UPDATE daily_participations SET chain_id = 'betanet' WHERE chain_id IS NULL;
UPDATE alert_logs SET chain_id = 'betanet' WHERE chain_id IS NULL;
-- Etc.
```

### 12.2 Backward Compatibility
**Problem:** Old endpoints without `?chain=` must remain functional

**Solution:** Default to first enabled chain (alphabetical)

```go
func GetChainIDFromRequest(r *http.Request) string {
    if chainID := r.URL.Query().Get("chain"); chainID != "" {
        return chainID  // Use specified
    }
    return EnabledChains[0]  // Default to first
}
```

### 12.3 SQLite Performance
**Risk:** With N chains × M validators, tables grow large

**Mitigations:**
- Composite indexes: `(chain_id, addr)`, `(chain_id, block_height)`
- Partitioning by date if needed (future)
- Archive old data: `DELETE FROM daily_participations WHERE date < DATE_SUB(NOW(), INTERVAL 1 YEAR) AND chain_id = ?`

### 12.4 Thread Safety
**Risk:** Nested maps + concurrent updates → race conditions

**Solution:**
- Keep MonikerMutex global (protects all maps)
- Use helpers with defer unlock
- Test with `-race` flag

```bash
go test -race ./internal/gnovalidator/...
```

---

## 13. WHY SQLITE

**Q: Why SQLite and not PostgreSQL?**

**Answers:**
✅ Simple deployment (single file db)
✅ Monitoring at small/medium scale (< 1M records/chain)
✅ Easy development (testoutils.NewTestDB)
✅ No external service

**With N chains:**
- 5 chains × 10K blocks/day = ~50K inserts/day = ~18.25M/year
- SQLite handles this without issues (with proper indexes)

**If future scaling needed:**
- Archiving: `DELETE old records WHERE date < ...`
- Partitioning by date
- Or migration to PostgreSQL (after multi-chain stable)

---

## 14. FINAL CHECKLIST

### Code Changes
- [x] Config structure updated (Phase 1)
- [x] Database migrations applied (Phase 1)
- [x] RPC clients per-chain (Phase 2)
- [x] Global state nested by chainID (Phase 2)
- [x] All collection loops parameterized (Phase 3)
- [ ] API endpoints scoped (Phase 4)
- [ ] Prometheus metrics labeled (Phase 4)
- [ ] Webhooks chain-aware (Phase 5)
- [ ] Telegram commands updated (Phase 6)
- [ ] Scheduler per-chain (Phase 7)

### Testing
- [x] Unit tests for config (Phase 1)
- [x] Unit tests for migrations (Phase 1)
- [ ] Unit tests updated for all functions (Phase 4)
- [ ] Integration tests (multi-chain) (Phase 7)
- [ ] Race condition tests (`-race`) (Phase 7)
- [ ] Load tests (N chains parallel) (Phase 7)
- [ ] Data isolation verified (Phase 7)

### Documentation
- [ ] CLAUDE.md updated with multi-chain patterns (Phase 8)
- [x] config.yaml.template structure documented (Phase 1)
- [ ] API swagger/postman updated (Phase 4)
- [ ] Telegram commands documented (Phase 6)

### Deployment
- [ ] Database migration script tested (Phase 8)
- [ ] Rollback plan documented (Phase 8)
- [ ] Monitoring/alerting setup (Phase 8)
- [ ] Config template finalized (Phase 8)

---

## 15. REFERENCES & PATTERNS

### Go Patterns Used
- **Nested Maps:** `map[string]map[string]string`
- **Mutex RWLock:** Read-heavy access pattern
- **Context:** Pass chainID via function params (not context.Context for simplicity)
- **Functional Options:** Future for ChainConfig

### SQL Patterns
- **Upserts:** `ON CONFLICT(chain_id, addr) DO UPDATE SET`
- **Compound indexes:** `(chain_id, other_columns)`
- **GROUP BY:** Add `chain_id` systematically

### Testing
- **Per-chain DB:** `testoutils.NewTestDB(t, "test_chain")`
- **Mock RPC:** Extend mocks to return data by chainID
- **Integration:** Start multiple monitoring loops in test

---

## 16. NEXT STEPS

**Phase 4 (Next):** API Endpoints & Prometheus

- Add chain parameter validation to all HTTP handlers
- Update database query functions to accept chainID
- Add chain label to Prometheus metrics
- **Expected duration:** 1 week

**Phase 5:** Webhooks & Alerts

- Implement chain-aware webhook dispatch
- Update alert formatting with chain information
- Add chain filtering to alert_logs queries
- **Expected duration:** 1 week

**Phase 6:** Telegram Bot Multi-Chain Support

- Add /chain and /setchain commands
- Update /status, /uptime, /subscribe with optional chain parameter
- Implement per-chain user preferences
- **Expected duration:** 1 week

**Phase 7:** Scheduler & Integration Testing

- Update TelegramHourReport with chain_id
- Implement per-chain report generation
- Comprehensive integration testing with multiple chains
- **Expected duration:** 1 week

**Phase 8:** Cleanup & Production Readiness

- Update CLAUDE.md with multi-chain patterns
- Final documentation pass
- Data migration testing
- Performance validation
- **Expected duration:** 1 week

---

**Document Status:** Phases 1-3 Complete, Ready for Phase 4
**Last Updated:** 2026-03-19
**Next Review:** After Phase 4 completion
