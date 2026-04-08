# Alert Pipeline — Business Logic Review
**Date:** 2026-04-08  
**Scope:** `WatchValidatorAlerts`, `SendResolveAlerts`, dedup, RESOLVED logic, Slack/Telegram dispatch

---

## Summary of critical findings

| # | Finding | Severity |
|---|---------|----------|
| 1 | Dedup `NOT EXISTS` condition can fail to unlock after RESOLVED — new incident silenced | **HIGH** |
| 2 | Slack WARNING sends **empty message** — dead code inside `if level == "CRITICAL"` | **HIGH** |
| 3 | 10-block RESOLVED window misses recoveries where resume block > end_height+10 | **MEDIUM** |
| 4 | Premature RESOLVED: one-off participation spike clears a still-ongoing incident | **MEDIUM** |
| 5 | 2-hour CTE window resurfaces all old sequences → 100+ DB queries/cycle under oscillating validators | **MEDIUM** |
| 6 | `SendResolveAlerts` runs during backfill against partially-written data → spurious RESOLVED possible | **MEDIUM** |
| 7 | `skipped` column semantics inverted (`true` = sent, not skipped) — future maintenance hazard | **MEDIUM** |
| 8 | WatchValidatorAlerts not duplicated currently, but chain restart without context cancel would create duplicate goroutine | **LOW** |

---

## Bug 1 — Dedup NOT EXISTS condition (HIGH)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go` ~line 480

### The query

```sql
SELECT COUNT(*) FROM alert_logs al
WHERE al.chain_id = ? AND al.addr = ? AND al.level = ?
  AND al.skipped = 1
  AND al.sent_at >= datetime('now', ?)
  AND NOT EXISTS (
      SELECT 1 FROM alert_logs r
      WHERE r.chain_id = al.chain_id AND r.addr = al.addr
        AND r.level    = 'RESOLVED'
        AND r.sent_at  > al.sent_at
        AND ?          > r.end_height    -- ← last ? = start_height of NEW window
  )
```

### The intent

A RESOLVED clears the dedup only if the new incident started **after** the resolved period ended: `new_start_height > resolved.end_height`.

### The bug

The 2-hour CTE window can re-surface **old sequences** whose `start_height` is numerically lower than the resolved `end_height`. In that case `new_start_height > resolved.end_height` is **false** → `NOT EXISTS` succeeds → the old alert is counted → `recentCount > 0` → the new alert is silenced even though a RESOLVED was dispatched.

**Concrete example:**
- Validator down 1000→1029, CRITICAL sent, RESOLVED at end_height=1029
- Validator immediately down again 1033→1070
- CTE resurfaces the old sequence (start=1000) still in the 2-hour window
- Dedup checks old CRITICAL: `1000 > 1029` → false → NOT EXISTS = true → counted → new incident silenced

**Why it worked before:** when the chain was healthy and validators went down cleanly then recovered, old sequences dropped out of the 2-hour window quickly. Under oscillating validators that continuously cycle, old sequences stay in the window and trigger this path.

### Fix direction

Replace `AND ? > r.end_height` with `AND ? >= r.end_height` OR scope the comparison to `al.start_height` rather than the new window's `start_height`:

```sql
AND ? >= r.end_height    -- allow equality: new incident at exactly end_height is a re-incident
```

Or, simpler: only look at the **most recent** unresolved alert, not all of them, by changing `COUNT(*)` to a `MAX(sent_at)` pattern.

---

## Bug 2 — Slack WARNING sends empty message (HIGH)

**File:** `backend/internal/fonction.go` lines 400–409

### The code

```go
case "slack":
    if level == "CRITICAL" {
        ...
        fullMsg = fmt.Sprintf(...)      // CRITICAL message built

        if level == "WARNING" {         // ← DEAD CODE: can never be true inside CRITICAL block
            emoji = "⚠️"
            fullMsg = fmt.Sprintf(...)  // WARNING message never built
        }
    }
    sendErr := SendSlackAlert(fullMsg, wh.URL)  // sends "" for WARNING
```

For Slack WARNING alerts, `level == "CRITICAL"` is false so the outer block is skipped, `fullMsg` stays at its zero value (`""`), and an empty POST body is sent to the Slack webhook. The Slack API silently accepts it (or returns an error that is swallowed).

### Fix

Move the WARNING branch outside the CRITICAL block, mirroring the Discord structure:

```go
case "slack":
    if level == "CRITICAL" {
        ...
        fullMsg = fmt.Sprintf(...)
    }
    if level == "WARNING" {
        fullMsg = fmt.Sprintf(...)
    }
    sendErr := SendSlackAlert(fullMsg, wh.URL)
```

---

## Bug 3 — 10-block RESOLVED window too narrow (MEDIUM)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go` — `SendResolveAlerts`

### The query

```go
SELECT COALESCE(MIN(block_height), 0)
FROM daily_participations
WHERE chain_id = ? AND addr = ?
  AND block_height > ?       -- end_height
  AND block_height <= ? + 10 -- end_height + 10
  AND participated = 1
```

### The issue

If the validator recovers at `end_height + 11` or later (e.g. longer maintenance, slow chain), the query returns 0 → RESOLVED never fires → the pending alert remains in `alert_logs` indefinitely → dedup stays locked.

The pending alert is checked every 20 seconds but the window never widens. The only way to clear it is if a *new* alert fires (at a higher `end_height`) and its own RESOLVED succeeds.

**On slow chains** (1 block/minute), 10 blocks = 10 minutes. Any recovery longer than 10 minutes after the last missed block is undetectable by this query.

### Fix direction

Make the window configurable (e.g. `recent_blocks_window` from `admin_config`), or widen it significantly (e.g. 50 blocks). The risk of false positives (one-off participation spike) must be weighed against the risk of missed RESOLVEDs — see Bug 4.

---

## Bug 4 — Premature RESOLVED for oscillating validators (MEDIUM)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go` — `SendResolveAlerts`

### The scenario

- Validator misses blocks 1000–1029 (CRITICAL)
- At block 1032 the validator appears in one precommit (participated=1), but then misses 1033–1070
- `SendResolveAlerts` finds 1032 ∈ (1029, 1039] → RESOLVED sent
- Next cycle: new CRITICAL 1033–1070 fires → correct, dedup clears

From a user perspective: they receive **CRITICAL → RESOLVED → CRITICAL** within ~20 seconds for what is effectively a single long outage. This is confusing but technically correct.

**True false-positive case:** a validator participates at block 1032 (a genuine recovery), then goes back down seconds later due to a crash. The operator receives a RESOLVED that is accurate at T, then a new CRITICAL at T+20s. Acceptable for monitoring purposes.

### Fix direction

No code change strictly required. The behaviour is correct but noisy. Consider rate-limiting RESOLVED messages (e.g. minimum 1 minute between RESOLVED and a new CRITICAL for the same validator) to reduce noise.

---

## Bug 5 — 2-hour CTE window generates too many sequences (MEDIUM)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go` ~line 401

### The problem

The CTE uses `WHERE date >= datetime('now', '-2 hours')`. For a validator oscillating every few blocks over 2 hours, the CTE can return **30–50 distinct sequences** per validator per 20-second cycle. Each sequence triggers:
- 1 dedup DB query
- 1 dead-validator silence DB query

With 3 oscillating validators: up to **300 DB queries per cycle** on a single-connection SQLite. This serialises with `CollectParticipation` writes, causing measurable backpressure.

### Root cause

The window is time-based, so historical sequences that were already alerted are continuously re-evaluated. The dedup correctly suppresses them, but at the cost of 30+ DB round-trips per validator.

### Fix direction

Track the **last alerted `end_height` per (chain, addr, level)** in memory. Use a block-based lookback: only evaluate sequences whose `start_height > last_alerted_end_height`. This eliminates the re-evaluation of old sequences entirely and reduces queries-per-cycle to O(1) per validator.

---

## Bug 6 — RESOLVED fires during backfill against partial data (MEDIUM)

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go` — sync gate + `SendResolveAlerts`

### The scenario

After our recent fix, `SendResolveAlerts` is called inside the backfill branch (`!isChainSynced`). During backfill, `BackfillParallel` writes blocks in parallel — not in strict chronological order. It is possible that:

1. Backfill has written blocks 1000–1150 (including 1032 with participated=1)
2. `SendResolveAlerts` runs and finds recovery at block 1032 → RESOLVED sent
3. Backfill continues and writes blocks 1033–1150 with participated=0
4. `WatchValidatorAlerts` (once synced) detects a new incident 1033–1150 and fires a new CRITICAL

The RESOLVED was technically accurate (the DB did have 1032=1 at that moment) but the user receives a RESOLVED immediately followed by a new CRITICAL, both from historical data, appearing to be real-time events.

### Fix direction

Consider not calling `SendResolveAlerts` during backfill **if the pending alert's `end_height` is beyond a threshold age** (e.g. only resolve alerts where the incident happened in the last N hours). This prevents historical spam while still allowing recent RESOLVEDs during transient backfill gaps.

---

## Bug 7 — `skipped` column semantics inverted (MEDIUM)

**File:** `backend/internal/database/db_init.go` line 124

```go
Skipped bool `gorm:"column:skipped;not null" json:"skipped"`
```

| Insert call | `skipped` value | Actual meaning |
|---|---|---|
| WARNING/CRITICAL dispatched | `true` | Alert **was sent** |
| RESOLVED dispatched | `false` | Alert **was sent** |

`skipped=true` means "this alert was dispatched" — the opposite of what the name implies. A reader would expect `skipped=true` to mean the alert was suppressed by dedup.

No runtime bug today since all queries are internally consistent. However, every new developer touching this code has a 50% chance of querying `skipped=0` when they mean "dispatched alerts", introducing silent bugs.

### Fix direction

Rename `skipped` to `dispatched` (or `sent`) and invert the boolean in a DB migration:
```sql
ALTER TABLE alert_logs RENAME COLUMN skipped TO dispatched;
UPDATE alert_logs SET dispatched = NOT dispatched;
```

---

## Bug 8 — Duplicate goroutine risk on chain restart (LOW)

**File:** `backend/main.go` + `backend/internal/gnovalidator/gnovalidator_realtime.go`

`StartValidatorMonitoring` launches three goroutines (`CollectParticipation`, `WatchValidatorAlerts`, `WatchNewValidators`) and stores a cancel function via `chainmanager.Register`. If a future admin API or error handler calls `startChainMonitoring` for an already-running chain **without first cancelling the existing context**, two instances of `WatchValidatorAlerts` run simultaneously for the same chain. This would cause double-alerts and double-dedup inserts.

Currently this path does not exist. `startChainMonitoring` is only called once at startup per chain. But the risk exists for any future "restart chain" feature.

### Fix direction

Before starting monitoring for a chain, always cancel the existing context if one exists:
```go
if cancel, ok := chainmanager.Get(chainID); ok {
    cancel()
}
```

---

## Full validator state machine

```
NORMAL
  └─ participated=1 each block
  └─ CTE returns 0 sequences
  └─ No alerts

DEGRADING (1-4 missed blocks)
  └─ CTE sequences below WarningThreshold
  └─ Filtered out — no alerts

WARNING (5-29 consecutive missed)
  └─ CTE returns sequence (start, end, missed=5-29)
  └─ Dedup: no recent WARNING → passes
  └─ Dead-validator silence: recently active → passes
  └─ SendAllValidatorAlerts → Discord + Slack + Telegram
  └─ InsertAlertlog(WARNING, skipped=true, start, end)
  └─ Next cycles (up to 6h): dedup blocks re-alert

CRITICAL (30+ consecutive missed)
  └─ CTE returns sequence with missed >= 30
  └─ Level reclassified to CRITICAL
  └─ Dedup: no recent CRITICAL → passes
  └─ SendAllValidatorAlerts → Discord + Slack (WARNING branch broken) + Telegram
  └─ InsertAlertlog(CRITICAL, skipped=true, start, end)
  └─ Next cycles (up to 24h): dedup blocks re-alert

RECOVERY
  └─ participated=1 at block R
  └─ SendResolveAlerts: finds first R in (end, end+10]
     ├─ Found → RESOLVED sent, InsertAlertlog(RESOLVED, skipped=false, start, end)
     │   └─ Dedup unlocked for future incidents
     └─ Not found (R > end+10) → pending alert stays → dedup stays locked

RE-INCIDENT (after clean RESOLVED)
  └─ New sequence at start_height S
  └─ Dedup: finds prior CRITICAL/WARNING within resend window
     └─ NOT EXISTS: S > resolved.end_height?
        ├─ YES → RESOLVED clears dedup → new alert fires ✓
        └─ NO  → dedup stays locked → new alert silenced ✗ (Bug 1)
```

---

## Files referenced

| File | Issues |
|------|--------|
| `backend/internal/gnovalidator/gnovalidator_realtime.go` | Bug 1 (dedup), Bug 3 (RESOLVED window), Bug 5 (CTE window), Bug 6 (backfill), Bug 8 |
| `backend/internal/fonction.go` | Bug 2 (Slack empty message) |
| `backend/internal/database/db_init.go` | Bug 7 (skipped naming) |
| `backend/main.go` | Bug 8 (goroutine lifecycle) |
