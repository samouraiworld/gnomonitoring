# feat-add-endpoint-addr-moniker

## Goal

1. Create a `GET /addr_moniker?addr=...` endpoint to resolve a moniker from a validator address.
2. Modify `valoper.go` to populate the `addr_monikers` table (upsert) during `InitMonikerMap`.
3. Prepare the removal of the `moniker` column from `daily_participations` (normalization).

---

## Impact Analysis

### Risk level: HIGH

The `moniker` column is present at **6 layers** of the system:

| Layer | Number of affected points |
| --- | --- |
| Insert into `daily_participations` | 5 functions |
| SQL metric queries | 6 queries |
| SQLite view `daily_missing_series` | 1 view (critical) |
| Alert detection | 2 functions |
| Telegram lookups | 3 functions |
| API/bot response structs | 6 structs |

---

## Phase 1 — Populate `addr_monikers` from `valoper.go` (MULTI-CHAIN)

### File: `backend/internal/gnovalidator/valoper.go`

#### Change: end of `InitMonikerMap()` with chain_id parameter

The function signature is now:
```go
func InitMonikerMap(db *gorm.DB, chainID string) {
    // ... existing code ...

    // After building MonikerMap[chainID][addr] = moniker, persist to addr_monikers
    for addr, moniker := range MonikerMap[chainID] {
        if err := database.UpsertAddrMoniker(db, chainID, addr, moniker); err != nil {
            log.Printf("⚠️ Failed to upsert addr_moniker chain=%s addr=%s: %v", chainID, addr, err)
        }
    }
    log.Printf("✅ addr_monikers table synced for chain=%s (%d entries)", chainID, len(MonikerMap[chainID]))
}
```

### File: `backend/internal/database/db.go`

#### Updated function signature

```go
func UpsertAddrMoniker(db *gorm.DB, chainID, addr, moniker string) error {
    return db.Exec(`
        INSERT INTO addr_monikers (chain_id, addr, moniker)
        VALUES (?, ?, ?)
        ON CONFLICT(chain_id, addr) DO UPDATE SET moniker = excluded.moniker
    `, chainID, addr, moniker).Error
}
```

### `AddrMoniker` model in `db_init.go` (VERIFIED)

Already correctly defined with multi-chain support:

```go
type AddrMoniker struct {
    ID      uint   `gorm:"primaryKey;autoIncrement;column:id"`
    ChainID string `gorm:"column:chain_id;not null;default:betanet;uniqueIndex:uniq_chain_addr,priority:1"`
    Addr    string `gorm:"column:addr;not null;uniqueIndex:uniq_chain_addr,priority:2"`
    Moniker string `gorm:"column:moniker;not null"`
}
```

The unique index is **composite** on `(chain_id, addr)` — no migration needed. ✅

---

## Phase 2 — New API endpoint `GET /addr_moniker` (MULTI-CHAIN)

### File: `backend/internal/database/db.go` (lookup)

#### Updated lookup function with chainID

```go
func GetMonikerByAddr(db *gorm.DB, chainID, addr string) (string, error) {
    var result struct{ Moniker string }
    err := db.Table("addr_monikers").
        Select("moniker").
        Where("chain_id = ? AND addr = ?", chainID, addr).
        First(&result).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return "", nil
    }
    return result.Moniker, err
}
```

### File: `backend/internal/api/api.go`

#### Updated handler with chain scoping

```go
func GetAddrMonikerHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
    EnableCORS(w)
    if r.Method != http.MethodGet {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    addr := r.URL.Query().Get("addr")
    if addr == "" {
        http.Error(w, "Missing addr parameter", http.StatusBadRequest)
        return
    }

    // Get chain from query param; default to default chain
    chainID := r.URL.Query().Get("chain")
    if chainID == "" {
        chainID = internal.Config.DefaultChain
    }

    // Validate chain
    if err := internal.Config.ValidateChainID(chainID); err != nil {
        http.Error(w, fmt.Sprintf("Invalid chain: %v", err), http.StatusBadRequest)
        return
    }

    moniker, err := database.GetMonikerByAddr(db, chainID, addr)
    if err != nil {
        http.Error(w, fmt.Sprintf("Failed to get moniker: %v", err), http.StatusInternalServerError)
        return
    }
    if moniker == "" {
        http.Error(w, "Address not found on this chain", http.StatusNotFound)
        return
    }
    json.NewEncoder(w).Encode(map[string]string{"chain": chainID, "addr": addr, "moniker": moniker})
}
```

#### Registration in `StartWebhookAPI()`

```go
mux.HandleFunc("/addr_moniker", func(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        GetAddrMonikerHandler(w, r, db)
    case http.MethodOptions:
        EnableCORS(w)
        w.WriteHeader(http.StatusOK)
    default:
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
    }
})
```

Public endpoint, no Clerk middleware (consistent with `/uptime`, `/block_height`, etc.).

**Usage:** `GET /addr_moniker?addr=gno1...&chain=betanet` or just `GET /addr_moniker?addr=gno1...` (uses default chain).

---

## Phase 3 — Remove `moniker` from `daily_participations` (MULTI-CHAIN)

### 3.1 — Data migration query

Before dropping the column, back-populate `addr_monikers` from existing data across **all chains**:

```sql
-- Populate addr_monikers from daily_participations (historical data, per chain)
INSERT INTO addr_monikers (chain_id, addr, moniker)
SELECT DISTINCT chain_id, addr, moniker
FROM daily_participations
WHERE moniker IS NOT NULL AND moniker != '' AND moniker != 'unknown'
ON CONFLICT(chain_id, addr) DO UPDATE SET moniker = excluded.moniker;

-- Verify the result
SELECT chain_id, COUNT(*) as count FROM addr_monikers GROUP BY chain_id;
```

### 3.2 — Drop the column (SQLite with multi-chain)

SQLite does not support `ALTER TABLE DROP COLUMN` before version 3.35. The migration requires recreating the table:

```sql
-- Step 1: Create the new table without moniker
CREATE TABLE daily_participations_new (
    date            DATETIME,
    block_height    INTEGER,
    chain_id        TEXT NOT NULL DEFAULT 'betanet',
    addr            TEXT,
    participated    BOOLEAN,
    tx_contribution BOOLEAN,
    UNIQUE(chain_id, block_height, addr)
);

-- Step 2: Copy data
INSERT INTO daily_participations_new (date, block_height, chain_id, addr, participated, tx_contribution)
SELECT date, block_height, chain_id, addr, participated, tx_contribution
FROM daily_participations;

-- Step 3: Drop the old table
DROP TABLE daily_participations;

-- Step 4: Rename
ALTER TABLE daily_participations_new RENAME TO daily_participations;

-- Step 5: Recreate multi-chain indexes
CREATE INDEX IF NOT EXISTS idx_dp_chain_block_height ON daily_participations(chain_id, block_height);
CREATE INDEX IF NOT EXISTS idx_dp_chain_addr ON daily_participations(chain_id, addr);
CREATE INDEX IF NOT EXISTS idx_dp_chain_date ON daily_participations(chain_id, date);
CREATE INDEX IF NOT EXISTS idx_dp_chain_addr_participated ON daily_participations(chain_id, addr, participated);
```

> ⚠️ This migration must be run outside of production, with a prior backup (`cp db/webhooks.db db/webhooks.db.bak`).

---

## Phase 4 — SQL metric query changes (MULTI-CHAIN)

All the following queries must replace `dp.moniker` or `moniker` with a `LEFT JOIN addr_monikers`.

### JOIN pattern to use (with chain_id scoping)

```sql
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
```

And replace `moniker` in SELECT/GROUP BY with `COALESCE(am.moniker, dp.addr) AS moniker`.

**CRITICAL:** Every query must include a `WHERE chain_id = ?` clause (or GORM equivalent) to prevent cross-chain data leakage.

---

### 4.1 — `GetCurrentPeriodParticipationRate()` — `db_metrics.go`

**Before:**

```sql
SELECT addr, moniker,
    ROUND(SUM(participated) * 100.0 / COUNT(*), 1) AS participation_rate
FROM daily_participations
WHERE date >= %s AND date < %s
GROUP BY addr, moniker
ORDER BY participation_rate ASC
```

**After (with chain_id scoping):**

```sql
SELECT dp.addr,
    COALESCE(am.moniker, dp.addr) AS moniker,
    ROUND(SUM(dp.participated) * 100.0 / COUNT(*), 1) AS participation_rate
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < ?
GROUP BY dp.addr
ORDER BY participation_rate ASC
```

---

### 4.2 — `UptimeMetricsaddr()` — `db_metrics.go`

**After (with chain_id scoping):**

```sql
WITH bounds AS (...),
base AS (
    SELECT
        p.addr,
        p.chain_id,
        SUM(CASE WHEN p.participated THEN 1 ELSE 0 END) AS ok,
        COUNT(*) AS total
    FROM daily_participations p
    WHERE p.chain_id = ?
    JOIN bounds b ON p.block_height BETWEEN b.start_h AND b.end_h
    GROUP BY p.chain_id, p.addr
)
SELECT
    COALESCE(am.moniker, base.addr) AS moniker,
    base.addr,
    100.0 * ok / total AS uptime
FROM base
LEFT JOIN addr_monikers am ON am.chain_id = base.chain_id AND am.addr = base.addr
ORDER BY uptime ASC
```

---

### 4.3 — `OperationTimeMetricsaddr()` — `db_metrics.go`

**After (with chain_id scoping):**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    MAX(dp.date) AS last_down_date,
    (SELECT MAX(date) FROM daily_participations d2
     WHERE d2.chain_id = dp.chain_id AND d2.addr = dp.addr AND d2.participated = 1) AS last_up_date,
    ROUND(...) AS days_diff
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
WHERE dp.chain_id = ? AND dp.participated = 0
GROUP BY dp.chain_id, dp.addr
```

---

### 4.4 — `TxContrib()` — `db_metrics.go`

**After (with chain_id scoping):**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    ROUND((SUM(dp.tx_contribution) * 100.0 / ...), 1) AS tx_contrib
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < ?
GROUP BY dp.addr
```

---

### 4.5 — `MissingBlock()` — `db_metrics.go`

**After (with chain_id scoping):**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END) AS missing_block
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < ?
GROUP BY dp.addr
```

---

### 4.6 — `CalculateRate()` — `gnovalidator_report.go`

**After (with chain_id scoping):**

```sql
SELECT
    dp.addr,
    COALESCE(am.moniker, dp.addr) AS moniker,
    COUNT(*) AS total_blocks,
    SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) AS participated_blocks
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
WHERE dp.chain_id = ? AND date(dp.date) = ?
GROUP BY dp.addr
```

---

## Phase 5 — Rewrite the `daily_missing_series` view (MULTI-CHAIN)

**File: `backend/internal/database/db.go` — `CreateMissingBlocksView()`**

This is the most critical change as this view feeds the real-time alert system.

**After (with chain_id scoping):**

```sql
CREATE VIEW IF NOT EXISTS daily_missing_series AS
WITH ranked AS (
    SELECT
        dp.chain_id,
        dp.addr,
        COALESCE(am.moniker, dp.addr) AS moniker,
        dp.date,
        dp.block_height,
        dp.participated,
        CASE
            WHEN dp.participated = 0 AND LAG(dp.participated) OVER
                (PARTITION BY dp.chain_id, dp.addr, DATE(dp.date) ORDER BY dp.block_height) = 1
            THEN 1
            WHEN dp.participated = 0 AND LAG(dp.participated) OVER
                (PARTITION BY dp.chain_id, dp.addr, DATE(dp.date) ORDER BY dp.block_height) IS NULL
            THEN 1
            ELSE 0
        END AS new_seq
    FROM daily_participations dp
    LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
    WHERE dp.date >= datetime('now', '-24 hours')
),
grouped AS (
    SELECT *,
        SUM(new_seq) OVER (PARTITION BY chain_id, addr, DATE(date) ORDER BY block_height) AS seq_id
    FROM ranked
)
SELECT
    chain_id,
    addr,
    moniker,
    DATE(date) AS date,
    TIME(date) AS time_block,
    MIN(block_height) OVER (PARTITION BY chain_id, addr, DATE(date), seq_id) AS start_height,
    block_height AS end_height,
    SUM(1) OVER (
        PARTITION BY chain_id, addr, DATE(date), seq_id
        ORDER BY block_height
        ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
    ) AS missed
FROM grouped
WHERE participated = 0
ORDER BY chain_id, addr, date, seq_id, block_height;
```

> ⚠️ The view must be `DROP`ped then recreated at startup. Modify `CreateMissingBlocksView()` to do a `DROP VIEW IF EXISTS daily_missing_series` before the `CREATE VIEW`.

---

## Phase 6 — Telegram queries (`db_telegram.go`) (MULTI-CHAIN)

### `GetValidatorStatusList()`

**After (with chain_id scoping):**

```sql
WITH v AS (
    SELECT DISTINCT dp.addr, COALESCE(am.moniker, dp.addr) AS moniker
    FROM daily_participations dp
    LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
    WHERE dp.chain_id = ?
)
SELECT v.moniker, v.addr,
    CASE WHEN s.activate = 1 THEN 'on' ELSE 'off' END AS status
FROM v
LEFT JOIN telegram_validator_subs s ON s.chain_id = ? AND s.addr = v.addr AND s.chat_id = ?
ORDER BY status DESC
```

### `GetAllValidators()`

**After (with chain_id scoping):**

```sql
SELECT DISTINCT dp.addr, COALESCE(am.moniker, dp.addr) AS moniker
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
WHERE dp.chain_id = ?
```

### `ResolveAddrs()`

**After (with chain_id scoping):**

```go
err := db.Raw(`
    SELECT am.addr, COALESCE(am.moniker, am.addr) AS moniker
    FROM addr_monikers am
    WHERE am.chain_id = ? AND am.addr IN ?
`, chainID, addrs).Scan(&results).Error
```

---

## Phase 7 — Removals in insertion code (MULTI-CHAIN)

### `gnovalidator_realtime.go` — `SaveParticipation()` with chain_id

```go
// Before
stmt := `INSERT OR REPLACE INTO daily_participations
    (date, block_height, moniker, addr, participated, tx_contribution)
    VALUES (?, ?, ?, ?, ?, ?)`
tx.Exec(stmt, timeStp, blockHeight, moniker, valAddr, ...)

// After (remove moniker, add chain_id)
stmt := `INSERT OR REPLACE INTO daily_participations
    (date, block_height, chain_id, addr, participated, tx_contribution)
    VALUES (?, ?, ?, ?, ?, ?)`
tx.Exec(stmt, timeStp, blockHeight, chainID, valAddr, ...)
```

### `sync.go` — `dpRow` struct + `flushChunk()` with chain_id

```go
// Remove the Moniker field, keep ChainID
type dpRow struct {
    Date           time.Time
    BlockHeight    int64
    ChainID        string
    // Moniker removed
    Addr           string
    Participated   bool
    TxContribution bool
}

// flushChunk(): remove moniker from INSERT and ON CONFLICT UPDATE
q := `INSERT INTO daily_participations
    (date, block_height, chain_id, addr, participated, tx_contribution)
    VALUES `
// ON CONFLICT: remove "moniker = excluded.moniker"
```

### `db_init.go` — `DailyParticipation` struct

```go
type DailyParticipation struct {
    Date           time.Time
    BlockHeight    int64
    ChainID        string
    // Moniker string   ← REMOVE
    Addr           string
    Participated   bool
    TxContribution bool
}
```

---

## Summary of files to modify (MULTI-CHAIN ADAPTED)

| File | Changes | Priority |
| --- | --- | --- |
| `database/db.go` | Update `UpsertAddrMoniker(chainID, ...)`, `GetMonikerByAddr(chainID, ...)`, rewrite `daily_missing_series` view with `chain_id` | **Critical** |
| `database/db_init.go` | Remove `Moniker` from `DailyParticipation` (keep `ChainID`); verify `AddrMoniker` has composite unique index `(chain_id, addr)` | **Critical** |
| `database/db_metrics.go` | 6 SQL queries with `LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr` + `WHERE chain_id = ?` | **Critical** |
| `gnovalidator/valoper.go` | Update `InitMonikerMap(db, chainID)` to call `UpsertAddrMoniker(db, chainID, ...)` per chain | **Critical** |
| `gnovalidator/gnovalidator_realtime.go` | Remove `moniker` from INSERTs, keep `chain_id`; update `WatchValidatorAlerts`, `SendResolveAlerts` queries | **Critical** |
| `gnovalidator/sync.go` | Remove `Moniker` from `dpRow`, keep `ChainID`; update `flushChunk`, `BackfillRange`, `BackfillParallel` | **Critical** |
| `gnovalidator/gnovalidator_report.go` | Rewrite `CalculateRate(chainID, ...)` with multi-chain JOIN pattern | High |
| `database/db_telegram.go` | Update 3 queries: pass `chainID` param, use multi-chain JOIN pattern | High |
| `api/api.go` | New handler `GetAddrMonikerHandler` + route `/addr_moniker?addr=...&chain=...` with chain validation | Medium |
| `gnovalidator_report_test.go` | Update test fixtures with `chain_id` | Low |

## Files with no required changes (MULTI-CHAIN ADAPTED)

- `telegram/validator.go` — formatters receive already-resolved structs with moniker, no direct DB access
- `telegram/telegram.go` — message sending logic unchanged
- `gnovalidator/Prometheus.go` — Prometheus queries hit `daily_participations` but use hardcoded queries (no moniker lookups from table)
- `internal/fonction.go` — alert formatting receives moniker from caller (before/after switch to JOINs, moniker is passed in)
- `internal/config.go` — multi-chain config loading, no moniker-related changes needed

---

## Recommended execution order (MULTI-CHAIN ADAPTED)

1. **Update `UpsertAddrMoniker(chainID, addr, moniker)` signature** in `db.go` and call from `InitMonikerMap(chainID)` in `valoper.go` → populate the table per chain without breaking anything.
2. **Add the new endpoint** `GET /addr_moniker?addr=...&chain=...` with chain validation → immediately testable after step 1.
3. **Update the 6 metric queries** with multi-chain JOIN pattern (`am.chain_id = dp.chain_id AND am.addr = dp.addr`) → test in parallel with the old code.
4. **Rewrite the view** `daily_missing_series` with `PARTITION BY chain_id, ...` → test the alert system in staging.
5. **Update the 3 Telegram queries** in `db_telegram.go` with multi-chain pattern + pass `chainID` param.
6. **Update `CalculateRate(chainID, ...)`** in `gnovalidator_report.go` with multi-chain signature and JOIN.
7. **Run the SQL migration** (backup → `INSERT INTO addr_monikers(chain_id, addr, moniker)` with DISTINCT per chain → table recreation with `chain_id`).
8. **Remove `Moniker`** from `DailyParticipation`, `dpRow`; keep `ChainID`; update all INSERTs to remove moniker parameter.
9. **Run `go build ./...` and all tests** (especially `multichain_integration_test.go`).
