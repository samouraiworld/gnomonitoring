# Analysis of Prometheus Metrics — Gnomonitoring

> Generated on 2026-03-24. Based on source code analysis without any modifications.

---

## Table of Contents

1. [General Architecture](#1-general-architecture)
2. [Inventory of Metrics and Their Calculations](#2-inventory-of-metrics-and-their-calculations)
3. [Critical Inconsistencies](#3-critical-inconsistencies)
4. [Semantic Issues](#4-semantic-issues)
5. [Performance Issues (Telegram Slowness)](#5-performance-issues-telegram-slowness)
6. [PostgreSQL Question](#6-postgresql-question)
7. [Summary Table](#7-summary-table)

---

## 1. General Architecture

```
main.go
  ├── gnovalidator.Init()              → registers 13 Prometheus metrics
  ├── gnovalidator.StartMetricsUpdater(db) → goroutine, loop every 5 min
  │       └── UpdatePrometheusMetricsFromDB(db, chainID, ctx)  [timeout 2 min/chain]
  └── gnovalidator.StartPrometheusServer(port) → expose /metrics

Telegram bot → handlers in telegram/validator.go
  └── calls the same database.* functions used by Prometheus
```

Metrics are updated **every 5 minutes**. Each chain is processed in parallel in its own goroutine with a 2-minute timeout.

---

## 2. Inventory of Metrics and Their Calculations

### 2.1 Phase 1 — Per-Validator Metrics

#### `gnoland_validator_participation_rate`

- **File**: `gnovalidator/metric.go` — `CalculateValidatorRates()`
- **Window**: Last **10,000 blocks** (correlated subquery on MAX(block_height))
- **SQL**:
  ```sql
  SELECT dp.addr, COALESCE(am.moniker, dp.moniker) AS moniker,
         COUNT(*) AS total_blocks,
         SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) AS participated_blocks
  FROM daily_participations dp
  LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
  WHERE dp.chain_id = ?
    AND dp.block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 10000
  GROUP BY dp.addr
  ```
- **Calculation**: `participated_blocks / total_blocks * 100` (in Go, not in SQL)
- **Called by**: Prometheus only

---

#### `gnoland_validator_uptime`

- **File**: `database/db_metrics.go` — `UptimeMetricsaddr()`
- **Window**: Last **500 blocks** (two steps: MAX then query)
- **SQL**:
  ```sql
  -- Step 1
  SELECT COALESCE(MAX(block_height), 0) FROM daily_participations WHERE chain_id = ?
  -- Step 2
  SELECT COALESCE(am.moniker, dp.addr) AS moniker, dp.addr,
         100.0 * SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) / COUNT(*) AS uptime
  FROM daily_participations dp
  LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
  WHERE dp.chain_id = ? AND dp.block_height > ?   -- ? = maxHeight - 500
  GROUP BY dp.addr
  ```
- **Called by**: Prometheus AND Telegram (`/uptime`)

---

#### `gnoland_validator_tx_contribution`

- **File**: `database/db_metrics.go` — `TxContrib()`
- **Window**: **Current calendar month** (date >= start of month, date < start of next month)
- **SQL**:
  ```sql
  SELECT COALESCE(am.moniker, dp.addr) AS moniker, dp.addr,
    ROUND((SUM(dp.tx_contribution) * 100.0 /
      (SELECT SUM(tx_contribution) FROM daily_participations
       WHERE chain_id = ? AND date >= ? AND date < ?)), 1) AS tx_contrib
  FROM daily_participations dp
  LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
  WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < ?
  GROUP BY dp.addr
  ```
- **IMPORTANT**: `tx_contribution` is a **boolean** (0/1) in the DB. `SUM()` counts the number of blocks where this validator was the **proposer** AND the block contained transactions.
- **Called by**: Prometheus (hardcoded `"current_month"`) AND Telegram `/tx_contrib` (configurable)

---

#### `gnoland_validator_operation_time`

- **File**: `database/db_metrics.go` — `OperationTimeMetricsaddr()`
- **Window**: Full history (no limit)
- **SQL**:
  ```sql
  WITH last_down AS (
    SELECT addr, chain_id, MAX(date) AS last_down_date
    FROM daily_participations WHERE chain_id = ? AND participated = 0
    GROUP BY chain_id, addr
  ),
  last_up AS (
    SELECT addr, chain_id, MAX(date) AS last_up_date
    FROM daily_participations WHERE chain_id = ? AND participated = 1
    GROUP BY chain_id, addr
  )
  SELECT COALESCE(am.moniker, ld.addr) AS moniker, ld.addr,
         ROUND(julianday(lu.last_up_date) - julianday(ld.last_down_date), 1) AS days_diff
  FROM last_down ld
  LEFT JOIN last_up lu ON lu.chain_id = ld.chain_id AND lu.addr = ld.addr
  LEFT JOIN addr_monikers am ON ...
  ```
- **Calculation**: `MAX(participation_date) - MAX(non_participation_date)`
- **Called by**: Prometheus AND Telegram (`/operation_time`)

---

#### `gnoland_validator_missing_blocks_month`

- **File**: `database/db_metrics.go` — `MissingBlock()`
- **Window**: **Current calendar month**
- **SQL**: `SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END)` over the month
- **Called by**: Prometheus AND Telegram (`/missing`)

---

#### `gnoland_validator_first_seen_unix`

- **File**: `database/db_metrics.go` — `GetFirstSeen()`
- **Window**: Full history
- **SQL**: `MIN(dp.date)` where `participated = 1`
- **Conversion**: Parsed in Go with two possible layouts, converted to Unix timestamp
- **Called by**: Prometheus only

---

#### `gnoland_missed_blocks` and `gnoland_consecutive_missed_blocks`

- **File**: `gnovalidator/metric.go`
- **`missed_blocks`**: blocks missed **today only** (`date >= date('now')`)
- **`consecutive_missed_blocks`**: streak calculated in Go on the **last 200 blocks**
- **Called by**: Prometheus only

---

### 2.2 Phase 2 — Per-Chain Metrics

#### `gnoland_chain_active_validators`

- **Window**: Last 100 blocks — `COUNT(DISTINCT addr) WHERE participated = 1`

#### `gnoland_chain_avg_participation_rate`

- **Window**: Last 100 blocks — `AVG(CAST(participated AS FLOAT)) * 100`

#### `gnoland_chain_current_height`

- `MAX(block_height)` — no window

All three use a preliminary `MAX(block_height)`. **Three separate MAX queries** for the same chain during the same update cycle.

---

### 2.3 Phase 3 — Alert Metrics

#### `gnoland_active_alerts`
```sql
WITH latest_alerts AS (
    SELECT addr, MAX(sent_at) as last_sent
    FROM alert_logs WHERE chain_id = ?
    GROUP BY addr
)
SELECT COUNT(*) FROM alert_logs al
INNER JOIN latest_alerts la ON al.addr = la.addr AND al.sent_at = la.last_sent
WHERE al.chain_id = ? AND al.level = ?
```
**Actual semantics**: counts validators whose **last alert** has the given level. It is not "unresolved" in the strict sense.

#### `gnoland_alerts_total`
```sql
SELECT COUNT(*) FROM alert_logs WHERE chain_id = ? AND level = ?
```
Cumulative count of all alerts sent.

---

## 3. Critical Inconsistencies

### 🔴 IC-1 : `participation_rate` Prometheus ≠ `/status` Telegram

This is the primary cause of value differences between Prometheus and Telegram.

| | Prometheus (`gnoland_validator_participation_rate`) | Telegram (`/status`) |
|---|---|---|
| **Function** | `CalculateValidatorRates()` | `GetCurrentPeriodParticipationRate()` |
| **File** | `gnovalidator/metric.go` | `database/db_metrics.go` |
| **Window** | Last 10,000 **blocks** | Current calendar **month** |
| **Type** | Sliding, block-based | Calendar-based, resets on the 1st of the month |

**Concrete example**: on the 25th of the month, if a validator has participated well over the last 10 days but missed many blocks at the start of the month:

- Prometheus (10K blocks ≈ 30 days): shows the last 30 days
- Telegram (current month): shows from the 1st of the month

The two metrics can diverge significantly depending on the period and validator behavior.

---

### 🔴 IC-2 : `tx_contribution` — boolean misinterpreted + silent division by zero

**The `tx_contribution` column is a `bool` (0/1)**, not an integer or float.

It is `true` only if: the validator was the **proposer** of the block AND the block contained transactions.
```go
// sync.go line 210
TxContribution: hasTx && (addr == txProp)
```

**The SQL calculation**:
```sql
SUM(dp.tx_contribution) * 100.0 / (SELECT SUM(tx_contribution) ...)
```

**Problem**: if no block in the month has `tx_contribution = 1` (chain without transactions, or proposer not identifiable), the denominator is **0** → SQLite returns **NULL** → the metric displays **0%** for all validators without error.

This is why `tx_contribution` does not work on certain chains in production. Possible causes:

- The chain uses a GraphQL indexer that does not provide the proposer field
- The proposer field is not in the block data fetched by the RPC
- The chain was backfilled with code that did not populate `tx_contribution`

---

### 🟠 IC-3 : `uptime` — Prometheus and Telegram use the same function

Unlike participation rate, `uptime` uses the **same function** (`UptimeMetricsaddr`) on both sides. Values **should be identical**, with a maximum offset of 45 seconds (Telegram cache TTL) + 5 minutes (Prometheus cycle).

If you observe differences, it could come from:

1. The Telegram cache (45s) returning stale data while Prometheus is more recent
2. A validator that changed moniker between the two calls (COALESCE on `addr_monikers`)

---

### 🟠 IC-4 : `AlertLog` does not have a `resolved_at` column

The `AlertLog` schema (db_init.go lines 101-112) **does not contain a `resolved_at` column**. The CLAUDE.md documentation mentions `WHERE sent_at IS NOT NULL AND resolved_at IS NULL` but that is not what the actual code does.

The `GetActiveAlertCount` query uses a heuristic: "is the last alert for this validator at level X?" This is not the same as an alert being "unresolved".

Problematic case: a validator CRITICAL that then receives a WARNING alert → it no longer appears in `active_alerts{level="CRITICAL"}` but in `level="WARNING"`, even if the CRITICAL problem is not resolved.

---

## 4. Semantic Issues

### 🟡 SEM-1 : `operation_time` — incorrect calculation for the announced semantic

**Description in code**: "Days since last validator downtime event"

**Actual calculation**:
```
julianday(MAX(date WHERE participated=1)) - julianday(MAX(date WHERE participated=0))
```

This calculation gives `last_participation_date - last_absence_date`. This is **not** the time since the last downtime, but the delta between the last presence and the last absence.

**Problematic cases**:

- **Negative** value if the validator is currently down (last_down > last_up)
- Very small value (e.g., 0.3) if the validator had a micro-absence yesterday but participated recently
- The value does not represent "how long the validator has been stable"

**What the metric should be**: `julianday('now') - julianday(last_down_date)` if `last_up > last_down`, otherwise 0 or negative.

---

### 🟡 SEM-2 : `uptime` — 500-block window very short

500 blocks is hardcoded. On different chains:

- Block time 2s → 500 blocks = **~17 minutes**
- Block time 6s → 500 blocks = **~50 minutes**

This window is extremely sensitive to punctual incidents. A 30-minute restart can drop uptime to 0%, even if the validator is at 99.9% over 30 days.

---

### 🟡 SEM-3 : `first_seen` — fragile date parsing

The parsing in `Prometheus.go` (lines 298-314) supports two formats:
```go
"2006-01-02 15:04:05-07:00"  // with timezone
"2006-01-02 15:04:05"        // without timezone
```

If SQLite returns a date in the format `"2006-01-02"` (date only, without time), both **parsers fail silently** and the validator is excluded from the `first_seen_unix` metric without clear error logs.

---

## 5. Performance Issues (Telegram Slowness)

### Identified Causes

#### PERF-1 : SQLite — concurrency between 3 actors

Three components access SQLite at the same time:

1. **Validator monitoring loop**: continuous writes every block (~2-6s per block)
2. **Prometheus updater**: reads every 5 min (heavy queries)
3. **Telegram bot**: on-demand reads

SQLite in WAL mode allows one reader concurrent with a write, but **writes block other writes** and **heavy reads in WAL can block checkpoints**. The Telegram bot may wait for the validator monitoring loop to release the lock.

#### PERF-2 : Telegram cache too short (45 seconds)

```go
const cacheTTL = 45 * time.Second
```

On a chain with a block every 2 seconds, this cache expires very quickly. For commands like `/uptime` that perform heavy calculations, 45s forces a new SQL query every 45s per active chat.

#### PERF-3 : `CalculateValidatorRates` — correlated subquery

```sql
WHERE dp.block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 10000
```

SQLite evaluates this subquery **only once** (not correlated per row), so it is not an N+1. But it executes on a potentially 14M+ row table (test11). The `idx_dp_chain_addr_blockheight` index should cover this query but performance depends on selectivity.

#### PERF-4 : 3 redundant `MAX(block_height)` queries per Prometheus cycle

In `UpdatePrometheusMetricsFromDB`, the 3 Phase 2 metrics each make their own `MAX(block_height)`:

- `GetActiveValidatorCount`: 1 MAX
- `GetAvgParticipationRate`: 1 MAX
- `GetCurrentChainHeight`: 1 MAX (implicitly via `GetCurrentChainHeight`)

That is **3 identical queries** on the same table during the same cycle for the same chain.

#### PERF-5 : Scalar subquery in `TxContrib`

```sql
SUM(dp.tx_contribution) * 100.0 / (SELECT SUM(tx_contribution) FROM daily_participations WHERE ...)
```

The inner subquery is not correlated (it does not reference the outer query), SQLite can optimize it. But it performs a full scan of the current month **for each call** to `TxContrib()`, in addition to the main scan.

---

## 6. PostgreSQL Question

### Would migrating to PostgreSQL help?

**For Prometheus metrics (5 min cycle)**: probably not necessary. The queries are well-indexed and the 2 min/chain timeout is more than sufficient. The real gain would be minimal to medium-term.

**For Telegram slowness**: yes, PostgreSQL would help significantly because:

- True MVCC concurrency (multi-readers/writers without exclusive locks)
- Native connection pooling (pgx/pgbouncer)
- More sophisticated query planner
- Scalar subqueries are better optimized

**However**, before migrating, there are quick wins to be made on SQLite:

1. **Increase Telegram cache TTL** from 45s to 2-3 minutes
2. **Enable `PRAGMA busy_timeout`** to avoid silent `database is locked` errors
3. **Share the MAX(block_height) value** between the 3 Phase 2 queries
4. **Fix the `tx_contribution` bug** (integer column type instead of boolean, or adapted query)

---

## 7. Summary Table

| Metric | Prometheus Window | Telegram Window | Consistent? | Issue |
|---|---|---|---|---|
| `participation_rate` | 10,000 blocks | Calendar month | ❌ **NO** | Different windows |
| `uptime` | 500 blocks | 500 blocks | ✅ Yes | Window too short |
| `tx_contribution` | Current month | Current month | ✅ Yes | Boolean → div/0 on some chains |
| `operation_time` | Full history | Full history | ✅ Yes | Formula semantically incorrect |
| `missing_blocks_month` | Current month | Current month | ✅ Yes | — |
| `first_seen_unix` | Full history | N/A | — | Fragile date parsing |
| `missed_blocks` | Today | N/A | — | Reset daily at midnight |
| `consecutive_missed_blocks` | 200 blocks | N/A | — | — |
| `active_alerts` | — | — | — | No `resolved_at` → imprecise heuristic |

---

## Summary of Priority Actions

| Priority | Issue | Impact |
|---|---|---|
| 🔴 P0 | `tx_contribution` returns 0 on chains without proposer data | Silent metric |
| 🔴 P0 | `participation_rate` Prometheus ≠ Telegram `/status` | User confusion |
| 🟠 P1 | `uptime` — 500-block window too short and inconsistent | Unstable values |
| 🟠 P1 | `consecutive_missed_blocks` — snapshot without temporal history | No curves possible |
| 🟠 P1 | `operation_time` — incorrect formula | Negative or misleading values |
| 🟠 P1 | Telegram slowness — cache TTL too short | Degraded UX |
| 🟡 P2 | 3× redundant `MAX(block_height)` per cycle | Unnecessary queries |
| 🟡 P2 | `first_seen_unix` — date parsing can fail silently | Missing validators |
| 🟡 P2 | `active_alerts` — no `resolved_at` column | Imprecise semantics |

---

## 8. Plan of Modifications (Validated)

The 4 requested modifications are documented below with affected files, change logic, and points of attention.

---

### MOD-1 : `participation_rate` → align with calendar month

**Objective**: Prometheus and Telegram `/status` display the same value.

**Change**: In `Prometheus.go`, replace the call to `CalculateValidatorRates()` with `database.GetCurrentPeriodParticipationRate(db, chainID, "current_month")`.

**Affected files**:

- `backend/internal/gnovalidator/Prometheus.go` — line 206, replace the call
- `backend/internal/gnovalidator/metric.go` — `CalculateValidatorRates()` becomes unused, should be removed (or kept for future use)

**Detail of the change**:

```go
// BEFORE (Prometheus.go ~line 206)
stats, err := CalculateValidatorRates(db, chainID)
// ...
for _, stat := range stats {
    ValidatorParticipation.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(stat.Rate)
}

// AFTER
rates, err := database.GetCurrentPeriodParticipationRate(db, chainID, "current_month")
// ...
for _, r := range rates {
    ValidatorParticipation.WithLabelValues(chainID, r.Addr, r.Moniker).Set(r.ParticipationRate)
}
```

**Points of attention**:

- The return type changes: `[]ValidatorStat` → `[]database.ParticipationRate` (fields `Addr`, `Moniker`, `ParticipationRate`)
- The Prometheus metric keeps the same name (`gnoland_validator_participation_rate`), no impact on existing dashboards
- The test `Prometheus_test.go` that tests `ValidatorParticipation` will need to be updated (test data uses `block_height` for the window; will need to add a `date` field to fixtures)
- Remove `CalculateValidatorRates()` from `metric.go` if it is no longer called anywhere

---

### MOD-2 : `uptime` → last 30 days (Prometheus + Telegram)

**Objective**: Significant window (~30 days), consistent between Prometheus and Telegram, and maintainable long-term.

**Change**: Modify `UptimeMetricsaddr()` in `db_metrics.go` to use a date-based window of 30 days instead of 500 blocks.

**Affected file**:

- `backend/internal/database/db_metrics.go` — function `UptimeMetricsaddr()` lines 159-190

**Detail of the change**:

```go
// BEFORE: two steps (MAX block_height, then filter on height > max - 500)

// AFTER: date-based filtering
query := `
    SELECT
        COALESCE(am.moniker, dp.addr) AS moniker,
        dp.addr,
        100.0 * SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) / COUNT(*) AS uptime
    FROM daily_participations dp
    LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
    WHERE
        dp.chain_id = ?
        AND dp.date >= date('now', '-30 days')
    GROUP BY dp.addr
    ORDER BY uptime ASC`
```

**Advantages**:

- Same window regardless of chain speed (no bias from block time)
- The `idx_dp_chain_date_addr` index covers this query (chain_id + date)
- The query becomes a single step (removes the preliminary MAX)

**Points of attention**:

- On chains with many validators and blocks/day (e.g., test11 at 2s/block = ~1.3M rows over 30 days), the `date >= date('now', '-30 days')` filter can still be slow if the index on `date` is not efficient. **Check with `EXPLAIN QUERY PLAN`** on prod before deploying.
- The first month of data on a new chain will give uptime based on less than 30 days — this is acceptable.
- Unit tests for `UptimeMetricsaddr` use `block_height` to simulate the window; fixtures will need to be adapted to use dates relative to `now` (e.g., `time.Now().AddDate(0, 0, -5)` to simulate "5 days within the window").

---

### MOD-3 : `tx_contribution` → fix silent division by zero

**Exact problem**: The `tx_contribution` column is a `bool` (0/1). If no block in the month has a proposer identified (= `SUM(tx_contribution) = 0` for the entire chain), the denominator is 0 → SQLite returns NULL → the metric is 0 for all validators without error or log.

**Change**: Use `NULLIF()` to transform a division by zero into an explicit NULL, then return NULL (0.0 in Go) with a distinct warning log.

**Affected file**:

- `backend/internal/database/db_metrics.go` — function `TxContrib()` lines 192-217

**Detail of the SQL change**:

```sql
-- BEFORE
ROUND((SUM(dp.tx_contribution) * 100.0 /
    (SELECT SUM(tx_contribution) FROM daily_participations
     WHERE chain_id = ? AND date >= ? AND date < ?)), 1) AS tx_contrib

-- AFTER: NULLIF protects against division by zero
ROUND((SUM(dp.tx_contribution) * 100.0 /
    NULLIF((SELECT SUM(tx_contribution) FROM daily_participations
            WHERE chain_id = ? AND date >= ? AND date < ?), 0)), 1) AS tx_contrib
```

**With this fix**:

- If the total is 0, each validator receives `NULL` instead of `0.0`
- In Go, the `TxContrib float64` field receives `0.0` (Go's zero value) → surface behavior identical, but...
- In `Prometheus.go`, we can detect that `len(txStats) > 0` but all values are 0 and log an explicit warning: `"⚠️ [chain] TxContribution: total=0, proposer data absent"`

**Note**: The real fix long-term is to verify **why** `tx_contribution = false` for all rows on these chains. Possible causes to investigate per chain:

1. The chain was backfilled with `sync.go` (`BackfillRange`) which may not populate the proposer correctly
2. The chain's RPC does not provide proposer information
3. The proposer address format does not match validator addresses in the DB

---

### MOD-4 : `consecutive_missed_blocks` → temporal curve

**Objective**: Able to plot in Grafana/Prometheus a curve `(time, num_missed_blocks)` over a period, instead of just a snapshot of the current streak.

**Understanding of the need**: Prometheus scrapes metrics every 15-30 seconds (configurable). If we expose a gauge "blocks missed in the last X hours/days", Prometheus automatically builds the time-series. The curve is read in Grafana with a simple graph panel.

**Proposed architecture**: Replace the single metric `gnoland_consecutive_missed_blocks` (instantaneous streak) with a new window-based metric.

**New metric**: `gnoland_missed_blocks_window`

- **Type**: Gauge
- **Labels**: `chain`, `validator_address`, `moniker`, `window` (`"1h"`, `"24h"`, `"7d"`)
- **Value**: number of blocks missed in the time window

This gives 3 time-series per validator: over 1h, 24h, 7 days. Grafana can then display the evolution of each curve over time.

**Affected files**:

- `backend/internal/gnovalidator/Prometheus.go` — add the new metric, call in `UpdatePrometheusMetricsFromDB`
- `backend/internal/database/db_metrics.go` — new function `GetMissedBlocksWindow(db, chainID, window string)`
- `backend/internal/gnovalidator/Prometheus.go` — removal (optional) of the old `ConsecutiveMissedBlocks`

**Detail of the SQL for the new function**:

```sql
-- Blocks missed in the last X hours/days (parameterized by a date calculated in Go)
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END) AS missed_count
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
WHERE dp.chain_id = ?
  AND dp.date >= ?    -- boundary calculated in Go: time.Now().Add(-windowDuration)
GROUP BY dp.addr
```

**Call in Prometheus.go**:

```go
windows := map[string]time.Duration{
    "1h":  time.Hour,
    "24h": 24 * time.Hour,
    "7d":  7 * 24 * time.Hour,
}
for label, dur := range windows {
    since := time.Now().Add(-dur)
    stats, err := database.GetMissedBlocksWindow(db, chainID, since)
    // ...
    for _, s := range stats {
        MissedBlocksWindow.WithLabelValues(chainID, s.Addr, s.Moniker, label).Set(float64(s.MissedCount))
    }
}
```

**In Grafana**: a query `gnoland_missed_blocks_window{window="24h"}` gives the curve of blocks missed over 24h over time, for each validator.

**Points of attention**:
- The old metric `gnoland_consecutive_missed_blocks` can be kept in parallel temporarily to avoid breaking existing alerts
- The 3 windows (1h, 24h, 7d) triple the number of time-series → monitor if many validators (cardinality)
- The `idx_dp_chain_date_addr` index is used by this query (filter on `chain_id + date`)

---

### Table of Modifications

| # | Metric | Main File | Complexity | Tests to Update |
|---|---|---|---|---|
| MOD-1 | `participation_rate` | `Prometheus.go`, `metric.go` | Low | `Prometheus_test.go` |
| MOD-2 | `uptime` | `db_metrics.go` | Low | `db_metrics_test.go`, `Prometheus_test.go` |
| MOD-3 | `tx_contribution` | `db_metrics.go` | Low | `db_metrics_test.go` |
| MOD-4 | `consecutive_missed_blocks` → `missed_blocks_window` | `Prometheus.go`, `db_metrics.go` | Medium | `Prometheus_test.go` |
