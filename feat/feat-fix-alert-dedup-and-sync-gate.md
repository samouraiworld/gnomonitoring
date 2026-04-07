# feat: fix alert deduplication and add sync gate

## Problem

Two independent bugs cause alert spam on chains with long-dead validators:

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
3. Remove the broken MUTED mechanism (replaced by time-based dedup).

---

## Implementation plan

### Step 1 — Per-chain sync gate

**File:** `internal/gnovalidator/gnovalidator_realtime.go`

Add a package-level sync state map (one entry per chainID):

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

In `CollectParticipation`, set the flag to `true` once the gap drops below the backfill threshold:

```go
// existing backfill block (gap > 500) — chain not yet synced
if latest-currentHeight > 500 {
    setChainSynced(chainID, false)
    // ... existing BackfillParallel logic ...
    continue
}

// gap is small → chain is considered synced
setChainSynced(chainID, true)
```

In `WatchValidatorAlerts`, add an early return at the top of the check loop:

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

### Step 2 — Time-based dedup (replace start_height logic)

**File:** `internal/gnovalidator/gnovalidator_realtime.go`

Add two new `admin_config` keys (see Step 4):
- `alert_critical_resend_hours` — default `24`
- `alert_warning_resend_hours` — default `6`

Replace the three dedup checks (check 1 by `start_height`, check 2 by BETWEEN range, mute check)
with a single time-based check:

```go
// Was an alert of this level already sent for this validator recently?
resendHours := th.ResendHoursForLevel(level)  // 24 for CRITICAL, 6 for WARNING
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
    continue  // already notified within the resend window — skip silently
}
```

Remove the three old checks (lines 424–496 in current code).

The send + log path stays the same:
```go
if err := internal.SendAllValidatorAlerts(...); err != nil { ... }
if err := database.InsertAlertlog(db, chainID, addr, moniker, level,
    start_height, end_height, true, time.Now(), ""); err != nil { ... }
```

### Step 3 — Add `ResendHoursForLevel` to Thresholds

**File:** `internal/gnovalidator/thresholds.go`

Add two new fields:

```go
type Thresholds struct {
    // existing fields ...
    AlertCriticalResendHours int
    AlertWarningResendHours  int
}

// defaults
activeThresholds = Thresholds{
    // existing ...
    AlertCriticalResendHours: 24,
    AlertWarningResendHours:  6,
}

func (t Thresholds) ResendHoursForLevel(level string) int {
    if level == "CRITICAL" {
        return t.AlertCriticalResendHours
    }
    return t.AlertWarningResendHours
}
```

Load from DB in `LoadThresholds`:

```go
AlertCriticalResendHours: database.GetAdminConfigInt(db, "alert_critical_resend_hours", 24),
AlertWarningResendHours:  database.GetAdminConfigInt(db, "alert_warning_resend_hours",  6),
```

### Step 4 — Register new admin_config defaults

**File:** `internal/database/db_init.go`

Add to the `defaultConfigs` map:

```go
"alert_critical_resend_hours": "24",
"alert_warning_resend_hours":  "6",
```

### Step 5 — Remove MuteAfterNAlerts and MuteDurationMinutes

**File:** `internal/gnovalidator/thresholds.go` + `internal/database/db_init.go`

The `MuteAfterNAlerts` and `MuteDurationMinutes` fields are replaced by the time-based dedup.
Remove them from `Thresholds`, `LoadThresholds`, and `admin_config` defaults.

Also remove the corresponding admin API fields from `handleGetThresholds` /
`handlePutThresholds` in `internal/api/api-admin.go`.

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

---

## Files changed

| File | Change |
|---|---|
| `internal/gnovalidator/gnovalidator_realtime.go` | Add `chainSynced` map + helpers; replace 3 dedup checks with single time-based check; add sync gate at top of alert loop |
| `internal/gnovalidator/thresholds.go` | Add `AlertCriticalResendHours`, `AlertWarningResendHours`, `ResendHoursForLevel`; remove `MuteAfterNAlerts`, `MuteDurationMinutes` |
| `internal/database/db_init.go` | Add `alert_critical_resend_hours`, `alert_warning_resend_hours` to defaults; remove mute defaults |
| `internal/api/api-admin.go` | Remove mute fields from threshold GET/PUT handlers |

No DB migration needed — `alert_logs` schema unchanged, new config keys auto-inserted by `db_init`.
