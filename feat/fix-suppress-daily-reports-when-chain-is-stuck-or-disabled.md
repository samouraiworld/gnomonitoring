# Fix: Suppress daily reports when chain is stuck or disabled

## Problem

When a chain gets stuck (block height stops progressing) or is manually disabled from the admin panel, daily reports (Discord/Slack/Telegram) keep firing anyway. We want to suppress them automatically — without touching each user's individual preferences.

## Solution

Add a global per-chain in-memory boolean flag `reportsEnabled[chainID]`. Both the stuck-detection loop and the admin-disable handler write to it. The two report-schedule loops (`SheduleUserReport`, `SheduleTelegramReport`) read it before firing any report.

No DB migration needed. No user preference is touched. The flag resets to `true` when the chain recovers or is re-enabled.

---

## Action Plan

### Step 1 — Add the in-memory registry to `gnovalidator_realtime.go`

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

Add alongside the existing per-chain maps at the top of the file:

```go
var reportsEnabled   = make(map[string]bool)
var reportsEnabledMu sync.RWMutex
```

Add three helper functions (following the `IsAlertSent` / `SetAlertSent` pattern):

- `IsReportsEnabled(chainID string) bool` — returns `true` if missing (safe default)
- `SetReportsEnabled(chainID string, enabled bool)` — sets the flag under write lock
- (no explicit reset needed — `SetReportsEnabled(chainID, true)` covers it)

---

### Step 2 — Set the flag when chain gets stuck; reset when it recovers

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

Inside `CollectParticipation`:

- In the `shouldAlert` / stagnation branch (where the stuck alert is dispatched):
  ```go
  SetReportsEnabled(chainID, false)
  ```
- In the recovery branch (where `SetAlertSent(chainID, "all", false)` is already called):
  ```go
  SetReportsEnabled(chainID, true)
  ```

---

### Step 3 — Guard the two report-schedule loops

**File:** `backend/internal/gnovalidator/gnovalidator_report.go`

In `SheduleUserReport`, inside the `case <-timer.C:` branch, before `SendDailyStatsForUser`:
```go
if !IsReportsEnabled(internal.Config.DefaultChain) {
    log.Printf("[report] chain %s reports suppressed (stuck or disabled), skipping user %s", ...)
    continue
}
```

In `SheduleTelegramReport`, same location, using the function-scoped `chainID`:
```go
if !IsReportsEnabled(chainID) {
    log.Printf("[report][%s] reports suppressed (stuck or disabled), skipping chat %d", ...)
    continue
}
```

> Direct calls from the Telegram `/report` command (manual, on-demand) intentionally bypass this gate.

---

### Step 4 — Set the flag when admin enables/disables a chain

**File:** `backend/internal/api/api-admin.go`

In `handlePutChain`:

- In the `!*body.Enabled` branch (where `chainmanager.Cancel(chainID)` is already called):
  ```go
  gnovalidator.SetReportsEnabled(chainID, false)
  ```
- In the re-enable branch:
  ```go
  gnovalidator.SetReportsEnabled(chainID, true)
  ```

---

### Step 5 — Surface the flag in the admin status endpoint (recommended)

**File:** `backend/internal/api/api-admin.go`

In `handleGetStatus`, extend `chainStatus` struct:
```go
ReportsEnabled bool `json:"reports_enabled"`
```
Populate with `gnovalidator.IsReportsEnabled(chainID)`. No new endpoint needed.

---

### Step 6 — Initialize the flag to `true` at startup for all enabled chains

**File:** `backend/main.go`

In the loop that starts monitoring goroutines for each enabled chain, add:
```go
gnovalidator.SetReportsEnabled(chainID, true)
```

This makes the default explicit and prevents a race on first report tick.

---

## Summary of changes

| File | Change |
|---|---|
| `gnovalidator/gnovalidator_realtime.go` | Add `reportsEnabled` map + helpers; set/reset flag in stagnation path |
| `gnovalidator/gnovalidator_report.go` | Guard `SheduleUserReport` and `SheduleTelegramReport` with `IsReportsEnabled` |
| `api/api-admin.go` | Call `SetReportsEnabled` on enable/disable; surface flag in status endpoint |
| `main.go` | Initialize flag to `true` at startup for each enabled chain |

No DB migration. No new package. No change to user preference tables.
