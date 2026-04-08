# fix: run SendResolveAlerts during backfill to unblock alert dedup

## Problem

In production with multiple chains (test11 + gnoland1), the service enters a
**perpetual backfill loop** that prevents both new alerts and RESOLVED alerts
from ever being dispatched.

### Root cause — sync gate blocks SendResolveAlerts

`WatchValidatorAlerts` has a sync gate that skips the entire loop body
(including `SendResolveAlerts`) while `chainSynced == false`:

```go
if !isChainSynced(chainID) {
    // select + continue  →  SendResolveAlerts never called
}
// ...
SendResolveAlerts(db, chainID)   // unreachable during backfill
```

`chainSynced` is set to `false` by `CollectParticipation` when the gap between
the last stored block and the chain tip exceeds 500 blocks. In production,
with two chains writing to the same SQLite database simultaneously, DB insert
contention slows the backfill enough that the chain tip advances faster than
the backfill can close the gap — keeping `chainSynced == false` indefinitely.

### Consequence — dedup window permanently blocked

`WatchValidatorAlerts` uses a time-based dedup: it skips sending a WARNING or
CRITICAL if an alert of the same level was dispatched within the last N hours
and no RESOLVED was logged in `alert_logs` since then.

Because `SendResolveAlerts` is never called during backfill:
- RESOLVED entries are never inserted into `alert_logs`
- The dedup query always finds an un-resolved WARNING/CRITICAL within the window
- All subsequent alerts for the same validator are silently dropped

**Example observed in production (test11):**
```
07:25  WARNING sent for MictoNode   (end_height=1153891)
08:39  WARNING sent for Ubik Capital  (no prior alert → dedup clear)
       → MictoNode incident continues but dedup blocks re-alert for 6 h
       → No RESOLVED ever sent (sync gate blocks SendResolveAlerts)
       → No further alert until 07:25 + 6h = 13:25
```

### Why dev/local is not affected

On a fresh local DB, the initial backfill (if any) completes quickly: the DB
is small, only one chain runs, inserts are fast. The gap drops below 500 before
many new blocks accumulate, `chainSynced` becomes `true`, and the system
switches to realtime mode normally.

---

## Fix

Call `SendResolveAlerts` inside the sync-gate branch so RESOLVED alerts are
dispatched even while the chain is catching up:

```go
if !isChainSynced(chainID) {
    SendResolveAlerts(db, chainID)   // clears the dedup window
    select {
    case <-ctx.Done():
        return
    case <-time.After(checkInterval):
    }
    continue
}
```

`SendResolveAlerts` is safe to call during backfill:
- It reads only from `alert_logs` and `daily_participations`, which are always
  consistent (backfill writes atomically per block).
- It only resolves alerts whose `end_height + 1` already has `participated = 1`
  in the DB, so it cannot fire prematurely.
- Once RESOLVED is inserted for a given `end_height`, the dedup window for that
  validator is cleared and new alerts can fire as soon as `chainSynced` returns
  to `true`.

New alert detection (the main loop body with the CTE query) stays behind the
sync gate: its `date >= datetime('now', '-2 hours')` filter prevents historical
spam, but the loop still relies on a consistent view of recent blocks that is
only guaranteed once the chain is synced.

---

## Diagnostic logs added (same branch)

Three log lines were added to help identify the blocking path without attaching
a debugger:

| Log | Meaning |
| --- | --- |
| `[validator][X] alert check: found N window(s)` | CTE found N missed-block sequences above threshold; emitted every cycle when synced |
| `[validator][X] dedup: skipping LEVEL alert for MONIKER …` | Time-based dedup is blocking a new alert; includes heights, missed count, and window duration |
| `[validator][X] silence: skipping LEVEL alert for MONIKER …` | Dead-validator silence check suppressed the alert |
| `[validator][X] SendResolveAlerts: N pending alert(s) without RESOLVED` | Number of WARNING/CRITICAL entries awaiting resolution this cycle |

---

## Files changed

| File | Change |
| --- | --- |
| `backend/internal/gnovalidator/gnovalidator_realtime.go` | Call `SendResolveAlerts` in the sync-gate branch; add diagnostic logs |
