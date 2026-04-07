# Fix: Chain Stagnation Alert — 6-Hour Delay Root Cause & Plan

## Context

`gnoland1` was stuck on the same block for ~6 hours. An alert was eventually received, but 6 hours late. This document explains the root causes and the fix plan.

---

## Root Cause Analysis

### Cause #1 — CRITICAL: `lastProgressTime` is global, shared across all chains

**File:** `gnovalidator/gnovalidator_realtime.go:64`

```go
var lastProgressTime = time.Now()  // ← single global, not per-chain
```

Every time **any** chain produces a new block, the progress path executes:

```go
// line 178-180
SetLastHeight(chainID, latest)
timeMu.Lock()
lastProgressTime = time.Now()  // ← resets the timer for ALL chains
timeMu.Unlock()
```

The stagnation check reads the same global timer:

```go
// line 133-137
timeMu.Lock()
lpt := lastProgressTime
timeMu.Unlock()
if lph != 0 && latest == lph {
    if !IsAlertSent(chainID, "all") && time.Since(lpt) > 2*time.Minute {
```

**Consequence:** If `betanet` is healthy and producing a block every ~3 seconds, it resets `lastProgressTime` continuously. Even if `gnoland1` is stuck, `time.Since(lpt)` will always be < 3 seconds. The stagnation condition can never trigger for `gnoland1` as long as `betanet` is alive.

The alert only fired after ~6 hours because `betanet` also stopped (or was restarted), letting the global timer finally go stale.

> This is already acknowledged in CLAUDE.md Known Limitations, but the severity was underestimated — it completely silences stagnation alerts in a multi-chain setup.

---

### Cause #2 — CRITICAL: `alertSent[chainID]["all"]` is never reset at the same height

**File:** `gnovalidator/gnovalidator_realtime.go:170`

```go
SetAlertSent(chainID, "all", true)
```

Once set to `true`, this flag is only reset to `false` when the chain recovers (new block detected). While the chain is stuck at the same height, the condition `!IsAlertSent(chainID, "all")` is permanently `false`. No re-alerts can fire.

This means: even after fixing Cause #1, the alert fires once and then goes silent until recovery. For a 6-hour outage, operators get zero follow-up alerts.

---

### Cause #3 — HIGH: `GetTimeOfAlert` blocks re-alerts for the same block height

**File:** `gnovalidator/gnovalidator_realtime.go:155-168`
**File:** `database/db_metrics.go:456-469`

```go
send_at, err := database.GetTimeOfAlert(db, chainID, latest)
// ...
if send_at.IsZero() {
    // only send alert if no row in alert_logs for this height
    internal.SendInfoValidator(...)
    database.InsertAlertlog(...)
}
```

Once an alert row exists in `alert_logs` for `(chainID, start_height=H, end_height=H)`, no further alert is ever sent for that height. Combined with Cause #2, this is a double lock: in-memory flag AND database gate both prevent re-alerts.

---

### Cause #4 — HIGH: `return` instead of `continue` on DB errors kills the goroutine

**File:** `gnovalidator/gnovalidator_realtime.go:139-143` and `155-158`

```go
blockTime, err := database.GetTimeOfBlock(db, chainID, latest)
if err != nil {
    log.Printf(...)
    return   // ← exits the goroutine entirely, not just the iteration
}
```

If the DB is briefly locked during the stagnation check, the entire monitoring loop for that chain dies silently. No panic recovery wraps this path (the `defer recover()` is at the top of the goroutine and does catch it, but only panics — not returns). Monitoring stops until service restart.

---

### Cause #5 — MEDIUM: Threshold is 2 minutes, not appropriate for quick detection

The current threshold `time.Since(lpt) > 2*time.Minute` is too coarse for a 3-second block time chain. A 20-second threshold would detect stagnation much earlier.

---

## Fix Plan

### Step 1 — Scope `lastProgressTime` per chain

Replace the single global `lastProgressTime time.Time` with a per-chain map, identical to how `lastProgressHeight` is already structured.

```go
// Before
var lastProgressTime = time.Now()

// After
var lastProgressTime = make(map[string]time.Time)
```

Update all reads and writes to use `chainID` as key:

- Line 134: `lpt := lastProgressTime[chainID]`
- Line 173: `lastProgressTime[chainID] = time.Now()`
- Line 179: `lastProgressTime[chainID] = time.Now()`
- Initialize on first access: if key missing, default to `time.Now()`

Also apply the same fix to `lastRPCErrorAlert` (same global-shared bug).

**Files:** `gnovalidator/gnovalidator_realtime.go`

---

### Step 2 — Allow periodic re-alerts while chain is stuck

The current behavior sends exactly one alert and goes silent. Add a re-alert mechanism:

- Track `lastStagnationAlertTime[chainID]` (per-chain, separate from `lastProgressTime`)
- After the first alert fires, allow re-alerts every N minutes (e.g. every 30 min) while the chain remains stuck
- Do not use `IsAlertSent` as a permanent gate — use it only to enforce the re-alert interval

This means:

- Remove the `GetTimeOfAlert` DB gate that permanently blocks re-alerts at the same height
- Replace with time-based re-alert logic using `lastStagnationAlertTime[chainID]`

---

### Step 3 — Change threshold from 2 minutes to 20 seconds

```go
// Before
if !IsAlertSent(chainID, "all") && time.Since(lpt) > 2*time.Minute {

// After
if time.Since(lastStagnationAlertTime[chainID]) > 30*time.Minute || firstAlert {
    // ... and stagnation detected for > 20 seconds
```

Proposed thresholds:

- First alert: 20 seconds of no new block
- Re-alerts: every 30 minutes while still stuck

---

### Step 4 — Fix `return` → `continue` for DB errors in stagnation path

Replace all `return` with `continue` in the DB error handlers inside the stagnation detection block so a transient DB error does not kill the goroutine.

**Lines to fix:** 141-143, 157-159

---

### Step 5 — Add chain stagnation info to Telegram `/status` command

When a chain is currently stagnating, the `/status` command should show it explicitly. This gives operators a way to check state proactively without waiting for an alert.

**File:** `telegram/validator.go` (or `govdao.go`)

---

## Summary Table

| # | Fix | Severity | File |
|---|-----|----------|------|
| 1 | Scope `lastProgressTime` per chain | **CRITICAL** | `gnovalidator_realtime.go` |
| 2 | Allow periodic re-alerts while stuck | **CRITICAL** | `gnovalidator_realtime.go` |
| 3 | Lower threshold to 20 seconds | **HIGH** | `gnovalidator_realtime.go` |
| 4 | `return` → `continue` on DB errors | **HIGH** | `gnovalidator_realtime.go` |
| 5 | Show stagnation in `/status` | **MEDIUM** | `telegram/validator.go` |

---

## Files Impacted

- `backend/internal/gnovalidator/gnovalidator_realtime.go` — main stagnation loop
- `backend/internal/database/db_metrics.go` — `GetTimeOfAlert` (may be simplified or removed)
- `backend/internal/telegram/validator.go` — `/status` command display
