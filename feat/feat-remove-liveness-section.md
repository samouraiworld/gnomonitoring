# feat: remove "Validator status at last block" section

## Objective

Remove the per-validator liveness section from both the `/status` Telegram command and
the stuck-chain daily report. Keep only the chain-level info (block height, consensus
round) and the "Alerts last 24h" section.

The liveness section shows a point-in-time snapshot of a single block's precommits — not
very actionable. The alerts section (last 24h) already covers what operators need to know.

---

## Current output (to simplify)

### `/status` — healthy chain

```
🟢 [betanet] Chain status — block #1142239 (3s ago)
Consensus: round 0 — Normal

Validator status at last block #1142239:        ← REMOVE THIS SECTION
  🟢 gnoops         (g1abc...)
  🟢 samourai-crew  (g1def...)
  🔴 ProValidator   (g1xyz...)

⚠️ Alerts last 24h (1 validator(s)):            ← KEEP
  🚨 CRITICAL  ProValidator ...
```

### `/status` — target output

```
🟢 [betanet] Chain status — block #1142239 (3s ago)
Consensus: round 0 — Normal

⚠️ Alerts last 24h (1 validator(s)):
  🚨 CRITICAL  ProValidator ...
```

### Stuck-chain daily report — current

```
🔴 [betanet] Chain status — block #1139000 (2h ago)
Consensus: round 42 — Degraded since 2026-04-07 13:00 UTC

Validator status at last block #1139000:        ← REMOVE
  🟢 gnoops ...
  🔴 ProValidator ...

⚠️ Alerts last 24h (1 validator(s)):            ← already there
```

---

## Impacted files

| File | Impact |
| --- | --- |
| `backend/internal/gnovalidator/health.go` | Remove liveness fetch from `FetchChainHealthSnapshot`; remove `ValidatorLiveness`/`Monikers` from `ChainHealthSnapshot`; remove liveness block from `FormatStuckReport` |
| `backend/internal/telegram/validator.go` | Remove liveness block from `formatChainHealthMessage`; remove `ValidatorLiveness`/`Monikers` from mirror `ChainHealthSnapshot` |
| `backend/main.go` | Remove `ValidatorLiveness`/`Monikers` from snapshot bridge closure |

---

## What stays untouched

- `FormatHealthyReport` — healthy-chain daily report already shows participation rates,
  not liveness. No change needed.
- `FormatDisabledReport` — does not use liveness. No change needed.
- `formatValidatorLivenessHTML` — can be deleted (no more callers after this change).
- `GetMonikerMap` / `SetMoniker` — still used elsewhere (alert dispatch, backfill).

---

## Implementation plan

### Step 1 — Remove `ValidatorLiveness` and `Monikers` from `ChainHealthSnapshot`

**File:** `backend/internal/gnovalidator/health.go`

Remove these two fields from the struct:
```go
// DELETE:
ValidatorLiveness map[string]bool
Monikers          map[string]string
```

`ValidatorRates`, `MinBlock`, `MaxBlock`, `AlertsLast24h` stay (used by daily report).

### Step 2 — Simplify `FetchChainHealthSnapshot`

**File:** `backend/internal/gnovalidator/health.go`

The current function spins up two goroutines (Status + ConsensusState) and then fetches
the block and validator set to build the liveness map. Remove the block/validator fetch
entirely (the sequential block at lines ~112-160):

```go
// DELETE: the entire block starting with:
if snap.RPCReachable && snap.LatestBlockHeight > 0 {
    blockResult, err := rpcClient.Block(...)
    ...
    snap.ValidatorLiveness = liveness
    // and the monikers block below
    snap.Monikers = monikers
}
```

Keep the two goroutines (Status → LatestBlockHeight/LatestBlockTime/IsStuck,
ConsensusState → ConsensusRound). They are still used by `formatChainHealthMessage`.

Side benefit: this also resolves review finding **W13** (no timeout on RPC calls) — the
two remaining RPC calls (Status + ConsensusState) are lightweight and fast compared to
the full Block+Validators fetch that is being removed.

### Step 3 — Update `FormatStuckReport`

**File:** `backend/internal/gnovalidator/health.go`

Remove the liveness/rates block:
```go
// DELETE:
if snap.ValidatorLiveness != nil {
    sb.WriteString(fmt.Sprintf("\nValidator status at last block #%d:\n", snap.LatestBlockHeight))
    sb.WriteString(formatValidatorLiveness(snap.ValidatorLiveness, snap.Monikers))
} else if len(snap.ValidatorRates) > 0 {
    sb.WriteString(...)
    sb.WriteString(formatValidatorRates(snap.ValidatorRates))
}
```

Add the alerts section at the end (same as `FormatHealthyReport`):
```go
sb.WriteString(FormatAlertsLast24h(snap.AlertsLast24h))
```

### Step 4 — Update `formatChainHealthMessage` in telegram

**File:** `backend/internal/telegram/validator.go`

Remove the liveness block:
```go
// DELETE:
if snap.ValidatorLiveness != nil {
    b.WriteString(fmt.Sprintf("\nValidator status at last block <code>#%d</code>:\n", snap.LatestBlockHeight))
    b.WriteString(formatValidatorLivenessHTML(snap.ValidatorLiveness, snap.Monikers))
} else {
    b.WriteString("\nParticipation (last 50 blocks — RPC unreachable):\n")
    b.WriteString(formatValidatorRates(snap.ValidatorRates))
}
```

The `AlertsFormatter` block stays. Nothing else changes in the function.

Delete `formatValidatorLivenessHTML` (no more callers).

### Step 5 — Update mirror `ChainHealthSnapshot` in telegram

**File:** `backend/internal/telegram/validator.go`

Remove `ValidatorLiveness` and `Monikers` from the local mirror struct.

### Step 6 — Update bridge in `main.go`

**File:** `backend/main.go`

Remove `ValidatorLiveness: snap.ValidatorLiveness` and `Monikers: snap.Monikers`
from the `ChainHealthSnapshot` conversion closure.

Also remove `convertBackValidatorRates` calls from `ChainStuckFormatter` and
`ChainDisabledFormatter` closures if `ValidatorRates` is no longer needed there
(check if FormatStuckReport/FormatDisabledReport still use it after Step 3).

---

## Expected final output

### `/status` — healthy chain, no alerts

```
🟢 [betanet] Chain status — block #1142239 (3s ago)
Consensus: round 0 — Normal
```

### `/status` — healthy chain, with alerts

```
🟢 [betanet] Chain status — block #1142239 (3s ago)
Consensus: round 0 — Normal

⚠️ Alerts last 24h (2 validator(s)):
  🚨 CRITICAL  ProValidator   (g1xyz...) — 3 alert(s) — last 18:32 UTC
  ✅ RESOLVED  ProValidator   (g1xyz...) at block #1141530
  ⚠️  WARNING   samourai-crew  (g1def...) — 1 alert(s) — last 14:10 UTC
```

### Stuck-chain daily report

```
🔴 [betanet] Chain status — block #1139000 (2h ago)
Consensus: round 42 — Degraded since 2026-04-07 13:00 UTC

⚠️ Alerts last 24h (1 validator(s)):
  🚨 CRITICAL  ProValidator   (g1xyz...) — 2 alert(s) — last 13:05 UTC
```

---

## Files changed

| File | Change |
| --- | --- |
| `backend/internal/gnovalidator/health.go` | Remove `ValidatorLiveness`/`Monikers` fields; remove block+validator RPC fetch; simplify `FormatStuckReport` |
| `backend/internal/telegram/validator.go` | Remove liveness block from `formatChainHealthMessage`; remove `ValidatorLiveness`/`Monikers` from mirror struct; delete `formatValidatorLivenessHTML` |
| `backend/main.go` | Remove liveness fields from bridge closure |

No DB migration. No new dependency. No change to alert dispatch or daily scheduler.
