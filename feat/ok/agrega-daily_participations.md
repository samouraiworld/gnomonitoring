# feat: Aggregation of daily_participations

## Motivation

The `daily_participations` table stores **one row per (chain_id, addr, block_height)**.
On a chain with 100 validators and ~720 blocks/hour, this represents ~1.7M rows/day.
Metrics queries (Prometheus, Telegram, API) aggregate almost systematically by day,
but must scan millions of raw rows to achieve this.

The goal is to stay with **SQLite** (no migration to PostgreSQL/MySQL) by adding an
aggregated table `daily_participation_agrega` that pre-calculates daily statistics per validator.

---

## Strategy: dual table with sliding window of raw data

| Layer | Table | Granularity | Role |
|---|---|---|---|
| Recent raw data | `daily_participations` | 1 row / block | Realtime alerts, streak detection |
| Historical aggregated data | `daily_participation_agrega` | 1 row / (addr, day) | Current month metrics, 30 days, uptime, TX contrib... |

**Raw data retention window** : 2 days minimum, 7 days recommended for safety.

The only need for raw data relates to **alerts** :
- `WatchValidatorAlerts` / `SendResolveAlerts` : **24h** window
- `GetMissedBlocksWindow` (1h) : **1h** window

The "last 100 blocks" metrics (`GetActiveValidatorCount`, `GetAvgParticipationRate`) and
"last 200 blocks" metrics (`CalculateConsecutiveMissedBlocks`) are in reality equivalent to
**a few minutes to a few hours** of blocks — they are always covered by the 2-day retention.

7 days provides a comfortable margin to absorb a potential delay in the aggregation job.
Beyond that, raw rows are purged after aggregation.

---

## Proposed `daily_participation_agrega` Table

```sql
CREATE TABLE IF NOT EXISTS daily_participation_agrega (
    chain_id               TEXT     NOT NULL,
    addr                   TEXT     NOT NULL,
    block_date             DATE     NOT NULL,  -- YYYY-MM-DD (UTC day)
    moniker                TEXT,
    participated_count     INTEGER  NOT NULL,  -- SUM(participated)
    missed_count           INTEGER  NOT NULL,  -- SUM(1 - participated) == total_blocks - participated_count
    tx_contribution_count  INTEGER  NOT NULL,  -- SUM(tx_contribution)
    total_blocks           INTEGER  NOT NULL,  -- COUNT(*) for the day
    first_block_height     INTEGER  NOT NULL,  -- MIN(block_height) for the day
    last_block_height      INTEGER  NOT NULL,  -- MAX(block_height) for the day
    PRIMARY KEY (chain_id, addr, block_date)
);

-- Index for date-range queries on a chain
CREATE INDEX IF NOT EXISTS idx_dpa_chain_date      ON daily_participation_agrega(chain_id, block_date);
-- Covering index for per-validator queries on a chain
CREATE INDEX IF NOT EXISTS idx_dpa_chain_addr_date ON daily_participation_agrega(chain_id, addr, block_date);
```

### Column Justification

| Column | Why |
|---|---|
| `participated_count` | Replaces `SUM(participated)` on rate queries (uptime, participation rate) |
| `missed_count` | Replaces `SUM(CASE WHEN participated = 0 THEN 1 ELSE 0 END)` (missing blocks month, window) |
| `tx_contribution_count` | Replaces `SUM(tx_contribution)` (TX contrib metric) |
| `total_blocks` | Denominator for rate calculations — avoids a `COUNT(*)` |
| `first_block_height` / `last_block_height` | Allows reconstructing block continuity for `operation_time` and `first_seen` |

---

## Classification of Existing Queries

### Queries that Can Migrate to `daily_participation_agrega`

| Function | Current Window | Equivalent Aggregated Query |
|---|---|---|
| `GetCurrentPeriodParticipationRate` | Current month | `SUM(participated_count) * 100.0 / SUM(total_blocks)` GROUP BY addr WHERE block_date IN month |
| `UptimeMetricsaddr` | Last 30 days | `SUM(participated_count) * 100.0 / SUM(total_blocks)` WHERE block_date >= NOW()-30d |
| `TxContrib` | Current month | `SUM(tx_contribution_count) * 100.0 / NULLIF(total_tx, 0)` |
| `MissingBlock` | Current month | `SUM(missed_count)` WHERE block_date IN month |
| `GetMissedBlocksWindow` (7d) | 7 days | `SUM(missed_count)` WHERE block_date >= NOW()-7d |
| `GetFirstSeen` | All history | `MIN(block_date)` WHERE participated_count > 0 (approx. of exact MIN(date)) |
| `OperationTimeMetricsaddr` | All history | `MAX(block_date) WHERE missed_count > 0` and `MAX(block_date) WHERE participated_count > 0` |
| `CalculateRate` (daily report) | Specific day | Direct SELECT on the row for that day |
| `CalculateMissedBlocks` (today) | Today | `missed_count` WHERE block_date = TODAY |

### Queries That Must Stay on `daily_participations` (raw data)

These queries require block-level granularity :

| Function | Reason |
|---|---|
| `CalculateConsecutiveMissedBlocks` | In-memory streak calculation block by block, ORDER BY block_height |
| `WatchValidatorAlerts` | `daily_missing_series` view with `LAG()` on consecutive block heights |
| `SendResolveAlerts` | Checks `participated` at exact block `end_height + 1` |
| `GetAvgParticipationRate` | Average over last 100 blocks (block-level granularity) |
| `GetActiveValidatorCount` | DISTINCT addr over last 100 blocks |
| `GetCurrentChainHeight` | MAX(block_height) - no date |

### Queries with Short Window to Keep on Raw Data

| Function | Window | Note |
|---|---|---|
| `GetMissedBlocksWindow` (1h) | 1 hour | Too short for daily aggregation, keep on raw data |
| `GetMissedBlocksWindow` (24h) | 24h | On the edge — OK on raw data with 7-day retention |

---

## Required Changes by Component

### 1. `internal/database/db_init.go`

- Add the `CREATE TABLE IF NOT EXISTS daily_participation_agrega` and its indexes.
- Add an idempotent migration (verification via `pragma_table_info`) for existing DBs.
- Add purge policy : delete `daily_participations` rows older than 7 days
  (can be done in `InitDB` or in the aggregation job itself).

### 2. New Aggregation Job: `internal/gnovalidator/aggregator.go`

Logic :
1. For each enabled chain, find the last `block_date` present in `daily_participation_agrega`.
2. Aggregate complete days not yet aggregated (days `< today`) from `daily_participations`.
3. Do an UPSERT in `daily_participation_agrega` (ON CONFLICT(chain_id, addr, block_date) DO UPDATE).
4. Delete `daily_participations` rows older than 7 days.

The job runs :
- **At startup** (catch up all non-aggregated history).
- **Once per hour** (or at midnight UTC to aggregate the previous day) via a goroutine in `main.go`.

```go
func StartAggregator(db *gorm.DB) {
    runAggregation(db) // catch up at startup
    ticker := time.NewTicker(1 * time.Hour)
    for range ticker.C {
        runAggregation(db)
    }
}

func runAggregation(db *gorm.DB) {
    for _, chainID := range internal.EnabledChains {
        aggregateChain(db, chainID)
        pruneRawData(db, chainID, 7*24*time.Hour)
    }
}
```

> **Note on startup sync** : if a new chain is added, `runAggregation` at startup
> will process all of that chain's backfilled history automatically — no special logic is
> needed in `sync.go`. Just launch `StartAggregator` after `BackfillParallel`.

### 3. `internal/database/db_metrics.go`

Rewrite the functions listed in "Queries that can migrate" to target `daily_participation_agrega`.
Example for `UptimeMetricsaddr` :

```sql
-- Before (on daily_participations)
SELECT addr, 100.0 * SUM(CASE WHEN participated THEN 1 ELSE 0 END) / COUNT(*) AS uptime
FROM daily_participations
WHERE chain_id = ? AND date >= date('now', '-30 days')
GROUP BY addr

-- After (on daily_participation_agrega)
SELECT addr, 100.0 * SUM(participated_count) / SUM(total_blocks) AS uptime
FROM daily_participation_agrega
WHERE chain_id = ? AND block_date >= date('now', '-30 days')
GROUP BY addr
```

### 4. `internal/gnovalidator/gnovalidator_report.go`

`CalculateRate(date)` : replace the query with a direct SELECT on `daily_participation_agrega`
where `block_date = date`.

### 5. `internal/gnovalidator/metric.go`

`CalculateMissedBlocks()` : if the target date is `>= yesterday`, keep on `daily_participations`.
If the date is old (edge case for manual report), use `daily_participation_agrega`.

### 6. `main.go`

Add the aggregation job launch for each enabled chain, after backfill startup :

```go
go gnovalidator.StartAggregator(db)
```

---

## Impact on Sync Startup for a New Chain

1. `BackfillParallel` populates `daily_participations` (raw data, all historical blocks).
2. `StartAggregator` (on next tick or immediately at startup) detects that `daily_participation_agrega`
   is empty for this chain and aggregates all available history.
3. After aggregation, raw data older than 7 days is purged.

No change in `sync.go` is needed — aggregation logic is independent of backfill.

---

## Expected Performance Gains

| Query | Before | After |
|---|---|---|
| Uptime 30d (100 validators, 1 year of data) | ~4.3M rows scanned | ~3,000 rows (1 row/day/validator) |
| TX contrib current month | ~5M rows/month | ~3,000 rows |
| Missing blocks month | ~5M rows/month | ~3,000 rows |
| Participation rate (daily report) | ~100K rows/day | 1 row/validator |
| OperationTime (all history) | Growing indefinitely | Stable after aggregation |

---

## What Does NOT Change

- The `daily_participations` table remains the source of truth for recent data.
- All alert logic (`WatchValidatorAlerts`, `SendResolveAlerts`, `daily_missing_series`) stays unchanged.
- Chain metrics (active validators, avg participation, chain height) stay on `daily_participations`.
- The GORM `DailyParticipation` model and realtime/backfill inserts stay unchanged.

---

## Suggested Implementation Order

1. ✅ **DB Migration** — `daily_participation_agrega` table + indexes added in `db_init.go`
2. ✅ **Aggregator** — `aggregator.go` implemented with upsert + purge
3. ✅ **Aggregator Tests** — 5 tests pass (basic, today excluded, idempotent, multiple days, prune)
4. ✅ **Migrate db_metrics.go** — 6 functions migrated, all tests pass
5. ✅ **Migrate gnovalidator_report.go** — `CalculateRate` migrated
6. ✅ **Update main.go** — `StartAggregator(db)` added
7. **Benchmark** — compare Prometheus endpoint response times before/after

---

## Progress

### ✅ Step 1 — DB Migration (`db_init.go`)

- GORM struct `DailyParticipationAgrega` added (composite PRIMARY KEY : `chain_id`, `addr`, `block_date`)
- Added to `AutoMigrate`
- Function `CreateAggregaIndexes` created : indexes `idx_dpa_chain_date` and `idx_dpa_chain_addr_date`
- Called in `InitDB` after `CreateOrReplaceIndexes`

> Note : GORM generates table name `daily_participation_agregas` (automatic plural).

### ✅ Step 2 — Aggregator (`gnovalidator/aggregator.go`)

- `StartAggregator(db)` : long-running goroutine, immediate pass at startup then every hour
- `AggregateChain(chainID)` : UPSERT in `daily_participation_agregas` for all complete days `< today`
- `PruneRawData(chainID)` : deletes `daily_participations` rows older than 7 days
- Called in `main.go` after `StartMetricsUpdater`

### ✅ Step 3 — Aggregator Tests (`gnovalidator/aggregator_test.go`)

- `TestAggregateChain_Basic` — verifies totals for 2 validators over 1 past day
- `TestAggregateChain_TodayExcluded` — verifies current day is not aggregated
- `TestAggregateChain_Idempotent` — two successive passes give same result
- `TestAggregateChain_MultipleDays` — each distinct past day produces its own row
- `TestPruneRawData` — old rows are deleted, recent rows kept

### ✅ Step 4 — Migration `db_metrics.go`

6 functions migrated to a 3-branch UNION :
- **Branch 1** : `daily_participation_agregas` (complete past days — fast path in production)
- **Branch 2** : `daily_participations` fallback LEFT JOIN (past days not yet in agrega — covers tests and new chains)
- **Branch 3** : `daily_participations` today (current day is never in agrega)

Functions migrated :
- `GetCurrentPeriodParticipationRate` — participation rate (month/week/year/all_time)
- `UptimeMetricsaddr` — uptime 30 days
- `TxContrib` — TX contribution (month/week/year/all_time), total via CTE
- `MissingBlock` — missed blocks (month/week/year/all_time)
- `OperationTimeMetricsaddr` — last downtime/uptime (all history), combined CTEs
- `GetFirstSeen` — first occurrence (all history)

Functions kept on `daily_participations` (block-level granularity needed) :
- `GetMissedBlocksWindow` (1h/24h/7d) — within 7-day retention window
- `GetActiveValidatorCount`, `GetAvgParticipationRate`, `GetCurrentChainHeight`
- `GetTimeOfBlock`, alerts, monikers

> The UNION approach guarantees backward compatibility : if agrega is empty (tests, new chain),
> the fallback branch takes over without modifying test assertions.

### ✅ Step 5 — Migration `gnovalidator_report.go`

- `CalculateRate(db, chainID, date)` : both queries merged into one with UNION agrega + raw fallback
- Returns per validator : `total_blocks`, `participated_count`, `first_block_height`, `last_block_height`
- Min/max of global block heights calculated in Go during result scanning

### ✅ Step 6 — Update `main.go`

- `gnovalidator.StartAggregator(db)` added after `StartMetricsUpdater`
- Launched at startup : immediate aggregation pass then every hour
