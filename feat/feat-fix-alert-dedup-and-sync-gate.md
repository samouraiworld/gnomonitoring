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

### Bug 4 — Permanently dead validators generate daily alert noise

With time-based dedup (24 h window for CRITICAL), a validator that has been absent for weeks
still triggers one CRITICAL alert every 24 h forever. This is noise: the operator already knows
the validator is gone.

---

## Goal

1. Replace `start_height`-based dedup with **time-based dedup**: at most one alert per
   `(chain_id, addr, level)` per configurable window (default 24 h for CRITICAL, 6 h for WARNING).
2. Add a **per-chain sync gate**: `WatchValidatorAlerts` skips alert processing until the chain
   is caught up (gap ≤ sync threshold).
3. Remove the broken MUTED mechanism from the warning/critical path (replaced by time-based dedup).
4. Fix `SendResolveAlerts` which shares the same `PARTITION BY DATE(date)` sliding-window problem.
5. Reduce unnecessary DB queries per cycle: replace the per-row loop with a single last-row-per-sequence query.
6. **Silence alerts for permanently dead validators**: stop sending alerts when a validator has had
   no participation for more than a configurable number of days (default 7).

---

## Implementation plan

### ✅ Step 1 — Per-chain sync gate

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

### ✅ Step 2 — Replace the missed-blocks query with a last-row-per-sequence query

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

The current query returns one row per block where `participated = 0`, causing N DB dedup queries
per dead validator per cycle. Replace with a query that returns only the **last row of each
contiguous missed sequence**:

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

Note: `PARTITION BY DATE(date)` removed — sequences scoped by `(addr, moniker)` only,
so a dead validator's sequence is no longer reset at midnight.

### ✅ Step 3 — Time-based dedup (replace start_height logic)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

Remove the three old dedup checks (`start_height` check, BETWEEN range check, mute check).
Replace with a single time-based check:

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

### ✅ Step 4 — Fix SendResolveAlerts (same sliding-window problem)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

Replace the CTE with the same sequence-based query from Step 2. Remove the
`MuteDurationMinutes`/`ResolveMuteAfterN` backoff — replaced by the existing `end_height` dedup:

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

### ✅ Step 5 — Add `ResendHoursForLevel` to Thresholds

**File:** `backend/internal/gnovalidator/thresholds.go`

Removed `MuteAfterNAlerts`, `MuteDurationMinutes`, `ResolveMuteAfterN`.
Added `AlertCriticalResendHours` (24), `AlertWarningResendHours` (6), and `ResendHoursForLevel()`.

### ✅ Step 6 — Register new admin_config defaults, remove old ones

**File:** `backend/internal/database/db_init.go`

Added `alert_critical_resend_hours = 24`, `alert_warning_resend_hours = 6`.
Removed `mute_after_n_alerts`, `mute_duration_minutes`, `resolve_mute_after_n`.

### ✅ Step 7 — Remove mute fields from admin API

**File:** `backend/internal/api/api-admin.go`

Not needed — `handleGetThresholds`/`handlePutThresholds` pass the DB config map directly
without referencing struct fields. No change required.

---

### Step 8 — Silence alerts for permanently dead validators

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`
**File:** `backend/internal/gnovalidator/thresholds.go`
**File:** `backend/internal/database/db_init.go`

#### Problem

With time-based dedup (24 h for CRITICAL), a validator absent for weeks still triggers one
CRITICAL alert per day forever. Once a validator has had no `participated = 1` row for more than
N days, it is considered permanently gone and further alerts are pure noise.

#### Approach

Before sending an alert, check when the validator last participated. If the last participation
date is older than `DeadValidatorSilenceDays` (default 7), skip the alert silently.

The check is a single cheap query on `daily_participations`:

```sql
SELECT MAX(date) FROM daily_participations
WHERE chain_id = ? AND addr = ? AND participated = 1
```

If the result is NULL (validator never participated in DB) or older than `now - N days`, skip.

#### Implementation

**`thresholds.go`** — add one field:

```go
type Thresholds struct {
    // existing fields ...
    DeadValidatorSilenceDays int  // default 7; 0 = feature disabled
}

// In activeThresholds defaults:
DeadValidatorSilenceDays: 7,

// In LoadThresholds:
DeadValidatorSilenceDays: database.GetAdminConfigInt(db, "dead_validator_silence_days", 7),
```

**`db_init.go`** — seed the new key:

```go
"dead_validator_silence_days": "7",
```

**`gnovalidator_realtime.go`** — add the check inside the alert loop, after the time-based dedup
check and before `SendAllValidatorAlerts`:

```go
// Silence permanently dead validators: skip if no participation in the last N days.
if t.DeadValidatorSilenceDays > 0 {
    silenceWindow := fmt.Sprintf("-%d days", t.DeadValidatorSilenceDays)
    var lastParticipated string
    err := db.Raw(`
        SELECT COALESCE(MAX(date), '') FROM daily_participations
        WHERE chain_id = ? AND addr = ? AND participated = 1
    `, chainID, addr).Scan(&lastParticipated).Error
    if err != nil {
        log.Printf("[validator][%s] DB error checking last participation: %v", chainID, err)
        continue
    }
    var isSilenced int64
    err = db.Raw(`
        SELECT CASE WHEN ? = '' OR ? < datetime('now', ?) THEN 1 ELSE 0 END
    `, lastParticipated, lastParticipated, silenceWindow).Scan(&isSilenced).Error
    if err != nil {
        log.Printf("[validator][%s] DB error evaluating silence window: %v", chainID, err)
        continue
    }
    if isSilenced == 1 {
        continue
    }
}
```

Alternatively (simpler, single query):

```go
if t.DeadValidatorSilenceDays > 0 {
    silenceWindow := fmt.Sprintf("-%d days", t.DeadValidatorSilenceDays)
    var activeRecently int64
    err := db.Raw(`
        SELECT COUNT(*) FROM daily_participations
        WHERE chain_id = ? AND addr = ? AND participated = 1
        AND date >= datetime('now', ?)
    `, chainID, addr, silenceWindow).Scan(&activeRecently).Error
    if err != nil {
        log.Printf("[validator][%s] DB error checking silence: %v", chainID, err)
        continue
    }
    if activeRecently == 0 {
        continue
    }
}
```

The second form is simpler and preferred. It returns 0 when the validator has not participated
in the last N days (or never), which covers both the "dead forever" and "never seen" cases.

#### Behaviour

| Situation | Before Step 8 | After Step 8 |
| --- | --- | --- |
| Validator down for 6 days | 1 alert/day | 1 alert/day (still within window) |
| Validator down for 7+ days | 1 alert/day forever | Silenced — no more alerts |
| Validator comes back after 8 days | RESOLVED sent | RESOLVED sent (silence only blocks new alerts, not resolves) |
| `dead_validator_silence_days = 0` | — | Feature disabled, behaves like before |

Note: `SendResolveAlerts` is **not** gated by the silence check. If a validator that was silenced
comes back online (`participated = 1` at `end_height + 1`), the RESOLVED alert still fires normally.
The silence only prevents new WARNING/CRITICAL alerts from being sent.

---

## Behaviour summary (all steps)

| Situation | Before | After |
| --- | --- | --- |
| Validator goes down | Alert sent | Alert sent |
| Still down 20 s later | Re-alerted (new start_height) | Silenced (within resend window) |
| Still down at midnight | Burst re-alert (date partition reset) | Silenced (within 24 h window) |
| Still down after 24 h | Re-alerted (daily burst) | 1 reminder sent (then silenced 24 h) |
| Still down after 7 days | 1 alert/day forever | Silenced permanently |
| Chain doing backfill | Historical alerts fire | No alerts until synced |
| Validator recovers | RESOLVED sent | RESOLVED sent (unchanged) |
| Validator recovers at midnight | Possible phantom resolve | Correct (sequence no longer date-partitioned) |

---

## Files changed

| File | Status | Change |
| --- | --- | --- |
| `backend/internal/gnovalidator/gnovalidator_realtime.go` | ✅ done | Sync gate, sequence query, time-based dedup, SendResolveAlerts fix |
| `backend/internal/gnovalidator/thresholds.go` | ✅ done + Step 8 | ResendHoursForLevel; add DeadValidatorSilenceDays |
| `backend/internal/database/db_init.go` | ✅ done + Step 8 | New resend-hours keys; add dead_validator_silence_days |
| `backend/internal/api/api-admin.go` | ✅ no change needed | Handlers pass DB map directly |

No DB migration needed — `alert_logs` schema unchanged, new config keys auto-inserted by `db_init`.

## Known limitations

- `skipped` column in `alert_logs` has inverted semantics (`true` = dispatched, not suppressed).
  No rename in this PR — too many read paths. Document it in a follow-up.
- `InsertAlertlog` uses `OnConflict{DoNothing: true}` on an auto-increment PK, so DB-level dedup
  never fires. Acceptable since Go-level dedup is the primary guard after this fix.
- Metrics updater retains `recent_blocks_window` in `Thresholds` but `db_init.go` does not seed
  its default. Pre-existing issue, not introduced here.
