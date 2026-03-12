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

## Phase 1 — Populate `addr_monikers` from `valoper.go`

### File: `backend/internal/gnovalidator/valoper.go`

#### Change: end of `InitMonikerMap()` (after the loop that builds `MonikerMap`)

After the final loop that fills `MonikerMap`, add an upsert to `addr_monikers`:

```go
// After building MonikerMap, persist to addr_monikers
for addr, moniker := range MonikerMap {
    if err := database.UpsertAddrMoniker(db, addr, moniker); err != nil {
        log.Printf("⚠️ Failed to upsert addr_moniker %s: %v", addr, err)
    }
}
log.Printf("✅ addr_monikers table synced (%d entries)", len(MonikerMap))
```

### File: `backend/internal/database/db.go`

#### New function to add

```go
func UpsertAddrMoniker(db *gorm.DB, addr, moniker string) error {
    return db.Exec(`
        INSERT INTO addr_monikers (addr, moniker)
        VALUES (?, ?)
        ON CONFLICT(addr) DO UPDATE SET moniker = excluded.moniker
    `, addr, moniker).Error
}
```

### `AddrMoniker` model in `db_init.go`

Verify that the model has a unique constraint on `addr`:

```go
type AddrMoniker struct {
    Addr    string `gorm:"primaryKey"`
    Moniker string
}
```

If `Addr` is not a `primaryKey` or has no unique index, the following migration is required:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_addr_monikers_addr ON addr_monikers(addr);
```

---

## Phase 2 — New API endpoint `GET /addr_moniker`

### File: `backend/internal/database/db.go` (lookup)

#### New lookup function

```go
func GetMonikerByAddr(db *gorm.DB, addr string) (string, error) {
    var result struct{ Moniker string }
    err := db.Table("addr_monikers").
        Select("moniker").
        Where("addr = ?", addr).
        First(&result).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return "", nil
    }
    return result.Moniker, err
}
```

### File: `backend/internal/api/api.go`

#### New handler

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
    moniker, err := database.GetMonikerByAddr(db, addr)
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

---

## Phase 3 — Remove `moniker` from `daily_participations`

### 3.1 — Data migration query

Before dropping the column, back-populate `addr_monikers` from existing data:

```sql
-- Populate addr_monikers from daily_participations (historical data)
INSERT INTO addr_monikers (addr, moniker)
SELECT DISTINCT addr, moniker
FROM daily_participations
WHERE moniker IS NOT NULL AND moniker != '' AND moniker != 'unknown'
ON CONFLICT(addr) DO UPDATE SET moniker = excluded.moniker;

-- Verify the result
SELECT COUNT(*) FROM addr_monikers;
```

### 3.2 — Drop the column (SQLite)

SQLite does not support `ALTER TABLE DROP COLUMN` before version 3.35. The migration requires recreating the table:

```sql
-- Step 1: Create the new table without moniker
CREATE TABLE daily_participations_new (
    date            DATETIME,
    block_height    INTEGER,
    addr            TEXT,
    participated    BOOLEAN,
    tx_contribution BOOLEAN,
    PRIMARY KEY (block_height, addr)
);

-- Step 2: Copy data
INSERT INTO daily_participations_new (date, block_height, addr, participated, tx_contribution)
SELECT date, block_height, addr, participated, tx_contribution
FROM daily_participations;

-- Step 3: Drop the old table
DROP TABLE daily_participations;

-- Step 4: Rename
ALTER TABLE daily_participations_new RENAME TO daily_participations;

-- Step 5: Recreate indexes if needed
CREATE INDEX IF NOT EXISTS idx_dp_addr ON daily_participations(addr);
CREATE INDEX IF NOT EXISTS idx_dp_date ON daily_participations(date);
```

> ⚠️ This migration must be run outside of production, with a prior backup (`cp db/webhooks.db db/webhooks.db.bak`).

---

## Phase 4 — SQL metric query changes

All the following queries must replace `dp.moniker` or `moniker` with a `LEFT JOIN addr_monikers`.

### JOIN pattern to use

```sql
LEFT JOIN addr_monikers am ON am.addr = dp.addr
```

And replace `moniker` in SELECT/GROUP BY with `COALESCE(am.moniker, dp.addr) AS moniker`.

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

**After:**

```sql
SELECT dp.addr,
    COALESCE(am.moniker, dp.addr) AS moniker,
    ROUND(SUM(dp.participated) * 100.0 / COUNT(*), 1) AS participation_rate
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE dp.date >= %s AND dp.date < %s
GROUP BY dp.addr
ORDER BY participation_rate ASC
```

---

### 4.2 — `UptimeMetricsaddr()` — `db_metrics.go`

**After:**

```sql
WITH bounds AS (...),
base AS (
    SELECT
        p.addr,
        SUM(CASE WHEN p.participated THEN 1 ELSE 0 END) AS ok,
        COUNT(*) AS total
    FROM daily_participations p
    JOIN bounds b ON p.block_height BETWEEN b.start_h AND b.end_h
    GROUP BY p.addr
)
SELECT
    COALESCE(am.moniker, base.addr) AS moniker,
    base.addr,
    100.0 * ok / total AS uptime
FROM base
LEFT JOIN addr_monikers am ON am.addr = base.addr
ORDER BY uptime ASC
```

---

### 4.3 — `OperationTimeMetricsaddr()` — `db_metrics.go`

**After:**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    MAX(dp.date) AS last_down_date,
    (SELECT MAX(date) FROM daily_participations d2
     WHERE d2.addr = dp.addr AND d2.participated = 1) AS last_up_date,
    ROUND(...) AS days_diff
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE dp.participated = 0
GROUP BY dp.addr
```

---

### 4.4 — `TxContrib()` — `db_metrics.go`

**After:**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    ROUND((SUM(dp.tx_contribution) * 100.0 / ...), 1) AS tx_contrib
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE dp.date >= %s AND dp.date < %s
GROUP BY dp.addr
```

---

### 4.5 — `MissingBlock()` — `db_metrics.go`

**After:**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END) AS missing_block
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE dp.date >= %s AND dp.date < %s
GROUP BY dp.addr
```

---

### 4.6 — `CalculateRate()` — `gnovalidator_report.go`

**After:**

```sql
SELECT
    dp.addr,
    COALESCE(am.moniker, dp.addr) AS moniker,
    COUNT(*) AS total_blocks,
    SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) AS participated_blocks
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE date(dp.date) = ?
GROUP BY dp.addr
```

---

## Phase 5 — Rewrite the `daily_missing_series` view

**File: `backend/internal/database/db.go` — `CreateMissingBlocksView()`**

This is the most critical change as this view feeds the real-time alert system.

**After:**

```sql
CREATE VIEW IF NOT EXISTS daily_missing_series AS
WITH ranked AS (
    SELECT
        dp.addr,
        COALESCE(am.moniker, dp.addr) AS moniker,
        dp.date,
        dp.block_height,
        dp.participated,
        CASE
            WHEN dp.participated = 0 AND LAG(dp.participated) OVER
                (PARTITION BY dp.addr, DATE(dp.date) ORDER BY dp.block_height) = 1
            THEN 1
            WHEN dp.participated = 0 AND LAG(dp.participated) OVER
                (PARTITION BY dp.addr, DATE(dp.date) ORDER BY dp.block_height) IS NULL
            THEN 1
            ELSE 0
        END AS new_seq
    FROM daily_participations dp
    LEFT JOIN addr_monikers am ON am.addr = dp.addr
    WHERE dp.date >= datetime('now', '-24 hours')
),
grouped AS (
    SELECT *,
        SUM(new_seq) OVER (PARTITION BY addr, DATE(date) ORDER BY block_height) AS seq_id
    FROM ranked
)
SELECT
    addr,
    moniker,
    DATE(date) AS date,
    TIME(date) AS time_block,
    MIN(block_height) OVER (PARTITION BY addr, DATE(date), seq_id) AS start_height,
    block_height AS end_height,
    SUM(1) OVER (
        PARTITION BY addr, DATE(date), seq_id
        ORDER BY block_height
        ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
    ) AS missed
FROM grouped
WHERE participated = 0
ORDER BY addr, date, seq_id, block_height;
```

> ⚠️ The view must be `DROP`ped then recreated at startup. Modify `CreateMissingBlocksView()` to do a `DROP VIEW IF EXISTS daily_missing_series` before the `CREATE VIEW`.

---

## Phase 6 — Telegram queries (`db_telegram.go`)

### `GetValidatorStatusList()`

**After:**

```sql
WITH v AS (
    SELECT DISTINCT dp.addr, COALESCE(am.moniker, dp.addr) AS moniker
    FROM daily_participations dp
    LEFT JOIN addr_monikers am ON am.addr = dp.addr
)
SELECT v.moniker, v.addr,
    CASE WHEN s.activate = 1 THEN 'on' ELSE 'off' END AS status
FROM v
LEFT JOIN telegram_validator_subs s ON s.addr = v.addr AND s.chat_id = ?
ORDER BY status DESC
```

### `GetAllValidators()`

**After:**

```sql
SELECT DISTINCT am.addr, COALESCE(am.moniker, dp.addr) AS moniker
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
```

### `ResolveAddrs()`

**After:**

```go
err := db.Raw(`
    SELECT am.addr, COALESCE(am.moniker, am.addr) AS moniker
    FROM addr_monikers am
    WHERE am.addr IN ?
`, addrs).Scan(&results).Error
```

---

## Phase 7 — Removals in insertion code

### `gnovalidator_realtime.go` — `SaveParticipation()`

```go
// Before
stmt := `INSERT OR REPLACE INTO daily_participations
    (date, block_height, moniker, addr, participated, tx_contribution)
    VALUES (?, ?, ?, ?, ?, ?)`
tx.Exec(stmt, timeStp, blockHeight, moniker, valAddr, ...)

// After
stmt := `INSERT OR REPLACE INTO daily_participations
    (date, block_height, addr, participated, tx_contribution)
    VALUES (?, ?, ?, ?, ?)`
tx.Exec(stmt, timeStp, blockHeight, valAddr, ...)
```

### `sync.go` — `dpRow` struct + `flushChunk()`

```go
// Remove the Moniker field from the dpRow struct
type dpRow struct {
    Date           time.Time
    BlockHeight    int64
    // Moniker removed
    Addr           string
    Participated   bool
    TxContribution bool
}

// flushChunk(): remove moniker from INSERT and ON CONFLICT UPDATE
q := `INSERT INTO daily_participations
    (date, block_height, addr, participated, tx_contribution)
    VALUES `
// ON CONFLICT: remove "moniker = excluded.moniker"
```

### `db_init.go` — `DailyParticipation` struct

```go
type DailyParticipation struct {
    Date           time.Time
    BlockHeight    int64
    // Moniker string   ← REMOVE
    Addr           string
    Participated   bool
    TxContribution bool
}
```

---

## Summary of files to modify

| File | Changes | Priority |
| --- | --- | --- |
| `database/db.go` | `UpsertAddrMoniker()`, `GetMonikerByAddr()`, rewrite `daily_missing_series` view | **Critical** |
| `database/db_init.go` | Remove `Moniker` from `DailyParticipation`, unique index on `addr_monikers` | **Critical** |
| `database/db_metrics.go` | 5 SQL queries with JOIN `addr_monikers` | **Critical** |
| `gnovalidator/valoper.go` | Call `UpsertAddrMoniker` at end of `InitMonikerMap` | **Critical** |
| `gnovalidator/gnovalidator_realtime.go` | Remove `moniker` from INSERTs, `WatchValidatorAlerts`, `SendResolveAlerts` | **Critical** |
| `gnovalidator/sync.go` | Remove `Moniker` from `dpRow`, `flushChunk`, `BackfillRange`, `BackfillParallel` | **Critical** |
| `gnovalidator/gnovalidator_report.go` | Rewrite `CalculateRate()` with JOIN | High |
| `database/db_telegram.go` | 3 queries with JOIN `addr_monikers` | High |
| `api/api.go` | New handler `GetAddrMonikerHandler` + route `/addr_moniker` | Medium |
| `gnovalidator_report_test.go` | Update test fixtures | Low |

## Files with no required changes

- `telegram/validator.go` — formatters receive already-resolved structs, no direct DB access
- `telegram/telegram.go` — same
- `gnovalidator/Prometheus.go` — no access to `daily_participations`
- `internal/fonction.go` — receives moniker as a parameter

---

## Recommended execution order

1. **Add `UpsertAddrMoniker`** in `db.go` and call it from `valoper.go` → populate the table without breaking anything.
2. **Add the new endpoint** `/addr_moniker` → immediately testable after step 1.
3. **Rewrite the 5 metric queries** with JOIN → test in parallel with the old code.
4. **Rewrite the view** `daily_missing_series` → test the alert system in staging.
5. **Rewrite the 3 Telegram queries** in `db_telegram.go`.
6. **Rewrite `CalculateRate()`** in `gnovalidator_report.go`.
7. **Run the SQL migration** (backup → INSERT INTO addr_monikers → table recreation).
8. **Remove `Moniker`** from `DailyParticipation`, `dpRow`, all INSERTs.
9. **Run `go build ./...` and tests**.
