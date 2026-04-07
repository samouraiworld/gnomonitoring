# feat: fix alert deduplication and add sync gate

## Problem

Three independent bugs cause alert spam on chains with long-dead validators:

### Bug 1 — Sliding `start_height` breaks dedup

`WatchValidatorAlerts` queries missed-block windows over a rolling `-2 hours` window with
`PARTITION BY DATE(date)`. For a validator that has been dead longer than 2 hours, the first block
in the window (= `start_height`) advances with every new block on-chain (~every few seconds).

Each new `start_height` is not in `alert_logs` → dedup check misses it → alert re-sent.

The `PARTITION BY DATE(date)` also resets the sequence at midnight, creating a burst of fresh
`start_height` values every day at 00:00 UTC → daily spam.

### Bug 2 — Mute mechanism is dead code

The mute check queries `alert_logs WHERE level = "MUTED"`, but the insert inside the mute branch
uses the actual level (`"WARNING"` / `"CRITICAL"`). `MUTED` rows are never inserted → mute
condition `count >= MuteAfterNAlerts` is always false → mute never activates.

### Bug 3 — Alerts fire during backfill

`WatchValidatorAlerts` starts immediately alongside `CollectParticipation`. During a large backfill
(gap > 500 blocks), participation rows are written in bulk for historical blocks. The 2h window
query picks up those old missed blocks and fires alerts for events that happened days or weeks ago,
all at the same timestamp.

---

## Goal

1. Replace `start_height`-based dedup with **time-based dedup**: at most one alert per
   `(chain_id, addr, level)` per configurable window (default 24 h for CRITICAL, 6 h for WARNING).
2. Add a **per-chain sync gate**: `WatchValidatorAlerts` skips alert processing until the chain
   is caught up (gap ≤ sync threshold).
3. Remove the broken MUTED mechanism from the warning/critical path (replaced by time-based dedup).
4. Fix `SendResolveAlerts` which shares the same `PARTITION BY DATE(date)` sliding-window problem.
5. Reduce unnecessary DB queries per cycle: replace the per-row loop with a single last-row-per-sequence query.

---

## Implementation plan

### Step 1 — Per-chain sync gate

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

Add a package-level sync state map (one entry per chainID). The Go zero value (`false`) is already
correct for "not yet synced", so no explicit initialization is needed at startup.

```go
var (
    chainSynced   = map[string]bool{}
    chainSyncedMu sync.RWMutex
)

func setChainSynced(chainID string, v bool) {
    chainSyncedMu.Lock()
    chainSynced[chainID] = v
    chainSyncedMu.Unlock()
}

func isChainSynced(chainID string) bool {
    chainSyncedMu.RLock()
    defer chainSyncedMu.RUnlock()
    return chainSynced[chainID]
}
```

In `CollectParticipation` around line 240, set the flag based on gap size:

```go
if latest-currentHeight > 500 {
    setChainSynced(chainID, false)   // mark unsynced while backfilling
    stop := latest - 200
    if stop < currentHeight {
        stop = latest
    }
    log.Printf("[monitor][%s] backfill [%d..%d] (gap=%d)", chainID, currentHeight, stop, latest-currentHeight)
    if err := BackfillParallel(db, client, chainID, currentHeight, stop, GetMonikerMap(chainID)); err != nil {
        log.Printf("[monitor][%s] backfill error: %v", chainID, err)
    } else {
        currentHeight = stop + 1
        log.Printf("[monitor][%s] backfill complete up to %d, switching to realtime", chainID, stop)
    }
    continue
}
// gap is small → chain is considered synced
setChainSynced(chainID, true)
```

In `WatchValidatorAlerts`, add an early return at the top of the loop body (before the query):

```go
if !isChainSynced(chainID) {
    select {
    case <-ctx.Done():
        return
    case <-time.After(checkInterval):
    }
    continue
}
```

### Step 2 — Replace the missed-blocks query with a last-row-per-sequence query

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

The current query (lines 360–397) returns one row per block where `participated = 0`, causing
N DB dedup queries per dead validator per cycle (Bug 6 from review). Replace with a query that
returns only the **last row of each contiguous missed sequence**:

```sql
WITH ranked AS (
    SELECT
        addr,
        moniker,
        block_height,
        participated,
        CASE
            WHEN participated = 0
             AND LAG(participated) OVER (PARTITION BY addr, moniker ORDER BY block_height) IS NOT DISTINCT FROM 0
            THEN 0 ELSE 1
        END AS new_seq
    FROM daily_participations
    WHERE chain_id = ? AND date >= datetime('now', '-2 hours')
),
grouped AS (
    SELECT
        addr,
        moniker,
        block_height,
        participated,
        SUM(new_seq) OVER (PARTITION BY addr, moniker ORDER BY block_height) AS seq_id
    FROM ranked
),
sequences AS (
    SELECT
        addr,
        moniker,
        MIN(block_height) AS start_height,
        MAX(block_height) AS end_height,
        COUNT(*)          AS missed
    FROM grouped
    WHERE participated = 0
    GROUP BY addr, moniker, seq_id
)
SELECT addr, moniker, start_height, end_height, missed
FROM sequences
WHERE missed >= ?
ORDER BY addr, start_height
```

Bind parameters: `chainID`, `GetThresholds().WarningThreshold`.

Note: the `PARTITION BY DATE(date)` is removed deliberately — sequences are now scoped by
`(addr, moniker)` only, so a dead validator does not get its sequence reset at midnight.

### Step 3 — Time-based dedup (replace start_height logic)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

Remove the three old dedup checks (lines 423–496: `start_height` check, BETWEEN range check,
mute check). Replace with a single time-based check:

```go
t := GetThresholds()
resendHours := t.ResendHoursForLevel(level)
window := fmt.Sprintf("-%d hours", resendHours)

var recentCount int64
err := db.Raw(`
    SELECT COUNT(*) FROM alert_logs
    WHERE chain_id = ? AND addr = ? AND level = ?
    AND skipped = 1
    AND sent_at >= datetime('now', ?)
`, chainID, addr, level, window).Scan(&recentCount).Error

if err != nil {
    log.Printf("[validator][%s] DB error checking alert_logs: %v", chainID, err)
    continue
}
if recentCount > 0 {
    continue
}
```

Note on `skipped` semantics: in the current codebase `skipped = true` (= 1) means "the alert was
actually dispatched" (confusingly named). The query above is correct.

The send + log path is unchanged:
```go
if err := internal.SendAllValidatorAlerts(chainID, missed, today, level, addr, moniker, start_height, end_height, db); err != nil {
    log.Printf("[validator][%s] SendAllValidatorAlerts error: %v", chainID, err)
}
if err := database.InsertAlertlog(db, chainID, addr, moniker, level, start_height, end_height, true, time.Now(), ""); err != nil {
    log.Printf("[monitor][%s] InsertAlertlog error: %v", chainID, err)
}
```

### Step 4 — Fix SendResolveAlerts (same sliding-window problem)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go` (lines 517–638)

`SendResolveAlerts` uses the same `PARTITION BY DATE(date)` and `-2 hours` window as the alert
query (lines 527–569). Replace its CTE with the same sequence-based query from Step 2 to avoid
midnight resets. Also remove the `MuteDurationMinutes`/`ResolveMuteAfterN` backoff (lines 592–611)
— it is replaced by the fact that `RESOLVED` rows are already logged with `end_height`, so the
existing `count > 0` check at line 588 prevents duplicate resolves.

The resolve path (`end_height` dedup) remains:
```go
var count int64
err := db.Raw(`
    SELECT COUNT(*) FROM alert_logs
    WHERE chain_id = ? AND addr = ? AND level = "RESOLVED"
    AND end_height = ?
`, chainID, a.Addr, a.EndHeight).Scan(&count).Error
if count > 0 {
    continue
}
```

The `MuteDurationMinutes` and `ResolveMuteAfterN` fields in `Thresholds` are no longer used in
`SendResolveAlerts` after this change (they were only used in the removed backoff block at
lines 592–611). They must be removed from `Thresholds` in Step 5 simultaneously to avoid
a compile error.

### Step 5 — Add `ResendHoursForLevel` to Thresholds

**File:** `backend/internal/gnovalidator/thresholds.go`

Add two new fields and remove the four fields that are now unused:

```go
type Thresholds struct {
    WarningThreshold            int
    CriticalThreshold           int
    // MuteAfterNAlerts — REMOVED (replaced by time-based dedup)
    // MuteDurationMinutes — REMOVED (replaced by time-based dedup)
    // ResolveMuteAfterN — REMOVED (replaced by end_height dedup in SendResolveAlerts)
    AlertCriticalResendHours    int   // NEW: default 24
    AlertWarningResendHours     int   // NEW: default 6
    StagnationFirstAlertSeconds int
    StagnationRepeatMinutes     int
    RPCErrorCooldownMinutes     int
    NewValidatorScanMinutes     int
    AlertCheckIntervalSeconds   int
    RawRetentionDays            int
    AggregatorPeriodMinutes     int
    RecentBlocksWindow          int
}
```

Defaults and `LoadThresholds`:

```go
activeThresholds = Thresholds{
    AlertCriticalResendHours: 24,
    AlertWarningResendHours:  6,
    // other fields unchanged...
}

// In LoadThresholds:
AlertCriticalResendHours: database.GetAdminConfigInt(db, "alert_critical_resend_hours", 24),
AlertWarningResendHours:  database.GetAdminConfigInt(db, "alert_warning_resend_hours",  6),
```

Add helper:

```go
func (t Thresholds) ResendHoursForLevel(level string) int {
    if level == "CRITICAL" {
        return t.AlertCriticalResendHours
    }
    return t.AlertWarningResendHours
}
```

### Step 6 — Register new admin_config defaults, remove old ones

**File:** `backend/internal/database/db_init.go`

In `SeedAdminConfig` defaults map:

```go
// Add:
"alert_critical_resend_hours": "24",
"alert_warning_resend_hours":  "6",
// Remove:
"mute_after_n_alerts":    "1",
"mute_duration_minutes":  "60",
"resolve_mute_after_n":   "4",
```

### Step 7 — Remove mute fields from admin API

**File:** `backend/internal/api/api-admin.go`

Remove `MuteAfterNAlerts`, `MuteDurationMinutes`, and `ResolveMuteAfterN` from the
`handleGetThresholds` / `handlePutThresholds` request/response structs.

---

## Behaviour after fix

| Situation | Before fix | After fix |
|---|---|---|
| Validator goes down | Alert sent | Alert sent |
| Still down 20 s later | Re-alerted (new start_height) | Silenced (within resend window) |
| Still down at midnight | Burst re-alert (date partition reset) | Silenced (within 24 h window) |
| Still down after 24 h | Re-alerted (daily burst) | 1 reminder sent (then silenced 24 h) |
| Chain doing backfill | Historical alerts fire | No alerts until synced |
| Validator recovers | RESOLVED sent | RESOLVED sent (unchanged) |
| Validator recovers at midnight | Possible phantom resolve | Correct (sequence no longer date-partitioned) |

---

## Files changed

| File | Change |
|---|---|
| `backend/internal/gnovalidator/gnovalidator_realtime.go` | Add `chainSynced` map + helpers; replace missed-blocks query (remove DATE partition); replace 3 dedup checks with single time-based check; add sync gate at top of alert loop; fix `SendResolveAlerts` query + remove mute backoff |
| `backend/internal/gnovalidator/thresholds.go` | Add `AlertCriticalResendHours`, `AlertWarningResendHours`, `ResendHoursForLevel`; remove `MuteAfterNAlerts`, `MuteDurationMinutes`, `ResolveMuteAfterN` |
| `backend/internal/database/db_init.go` | Add `alert_critical_resend_hours`, `alert_warning_resend_hours`; remove mute defaults |
| `backend/internal/api/api-admin.go` | Remove mute fields from threshold GET/PUT handlers |

No DB migration needed — `alert_logs` schema unchanged, new config keys auto-inserted by `db_init`.

## Known limitations after fix

- `skipped` column in `alert_logs` has inverted semantics (`true` = dispatched, not suppressed).
  No rename in this PR — too many read paths. Document it in a follow-up.
- `InsertAlertlog` uses `OnConflict{DoNothing: true}` on an auto-increment PK, so DB-level dedup
  never fires. Acceptable since Go-level dedup is the primary guard after this fix.
- Metrics updater retains `recent_blocks_window` in `Thresholds` but `db_init.go` does not seed
  its default. Pre-existing issue, not introduced here.
