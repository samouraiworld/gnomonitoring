# Feat: Improve daily summary and validator status commands

## Problem

Two related issues:

1. **Daily report is not representative** — it shows yesterday's average participation rate. When the chain is stuck, reports were silenced entirely (previous fix). A report saying "chain stuck, here is last known validator state" is more useful than silence.
2. **`/status` means the wrong thing** — it currently shows historical participation rates (monthly by default). "Status" should mean *current state*: is the chain alive? How healthy is consensus? Who is voting right now?

---

## RPC-based chain health signals (new)

Exploration of the live test12 RPC (`/status`, `/consensus_state`, `/dump_consensus_state`) revealed three actionable signals not currently tracked:

### Signal 1 — Time since last block (from `/status`)

```
client.RPCClient.Status() → ResultStatus.SyncInfo.LatestBlockTime (time.Time)
```

Age of the last committed block: `time.Since(LatestBlockTime)`. On test12 right now: **3 days**. This is a continuous metric, much more informative than the current boolean "stuck flag". Displayed as "last block 3d 4h ago" in both the daily report and `/status`.

### Signal 2 — Consensus round number (from `/consensus_state`)

```
client.RPCClient.ConsensusState() → ResultConsensusState.RoundState (RoundStateSimple)
// RoundStateSimple has a "height/round/step" string, e.g. "234889/713/7"
```

The consensus round counter tells us *how stuck* the chain is, not just whether it is stuck:

| Round | Meaning |
|---|---|
| 0–2 | Normal — block produced promptly |
| 3–10 | Slightly slow |
| >10 | Degraded — some validators missing |
| >50 | Stuck — no 2/3 majority reachable |

On test12 right now: **round 713**. This number is the clearest single-signal health indicator.

### Signal 3 — Live validator vote count (from `/dump_consensus_state`)

```
client.RPCClient.DumpConsensusState() → peers[i].peer_state (base64 JSON)
// decoded peer_state contains:
// "prevotes":  {"bits": "7", "elems": ["121"]}
// "precommits":{"bits": "7", "elems": ["121"]}
```

Each peer reports a BitArray of who has voted in the current consensus round. The `elems` field is a uint64 bitmask; `bits` is the total validator count.

`popcount(121)` = `popcount(0b01111001)` = **5 votes out of 7**.

On test12 right now: **5/7 validators are actively voting** even though the chain is blocked. This is the real "validator liveness" signal that works even when no new blocks are produced. It tells us the chain is stuck due to a BFT agreement issue, not because validators are offline.

**Note on parsing complexity:** `bits` and `elems` form a compact bit array. To count votes: `sum(popcount(e) for e in elems)`, capped at `bits`. Per-validator breakdown (which specific validators) requires mapping bit indices to the `/validators` endpoint result — possible but adds complexity. A total count ("5/7 voting") is simpler and equally useful for the report.

---

## Proposed command restructuring

| Command | Before | After |
|---|---|---|
| `/status` | Participation rate (historical, period-based) | **Chain health snapshot** (RPC-live metrics + recent validator liveness) |
| `/rate` *(new)* | — | Participation rate — what `/status` did before |
| `/uptime` | Uptime % (last 500 blocks) | Unchanged |
| `/report` | Trigger/configure daily report | Unchanged — report content changes (see below) |

Rationale: "status" should answer "what is happening *right now*". Historical rates move to `/rate`.

---

## Chain health snapshot — what it contains

Gathered from two sources:

**From RPC (live, at query time):**
- `LatestBlockTime` → age of last block
- `ConsensusRound` → current round number + interpretation (Normal / Degraded / Stuck)
- `VotingCount / TotalValidators` → from dump_consensus_state bitmask popcount

**From DB (historical, last N blocks):**
- Per-validator participation rate (last 50 blocks) — who was contributing before/during the stuck period

### Output when chain is healthy (round 0–2)

```
🟢 [test12] Chain status — block #234900 (3s ago)
Consensus: round 0 — Normal
Validators voting: 7/7

Participation (last 50 blocks):
  🟢 gnoops    (g1abc...) 100%
  🟢 samourai  (g1def...)  98%
  🟡 slowval   (g1ghi...)  76%
```

### Output when chain is degraded (round 3–50)

```
🟡 [test12] Chain status — block #234900 (45s ago)
Consensus: round 8 — Degraded (some validators lagging)
Validators voting: 5/7

Participation (last 50 blocks):
  🟢 gnoops    (g1abc...) 100%
  🔴 offline   (g1xyz...)   0%
  ...
```

### Output when chain is stuck (round > 50)

```
🚨 [test12] Chain status — block #234888 (3d 4h ago)
Consensus: round 713 — STUCK (no 2/3 majority since 2026-04-03 17:40 UTC)
Validators voting: 5/7

Last known participation (50 blocks before freeze):
  🟢 gnoops    (g1abc...) 100%
  🟢 samourai  (g1def...)  98%
  🔴 downval   (g1xyz...)   0%
```

### Output when chain is disabled

```
⚫ [test12] Chain status — MONITORING OFF
Last known block: #234888 at 2026-04-03 17:40 UTC
```

---

## Daily report changes

The daily report reuses the same `ChainHealthSnapshot` struct and format helpers as `/status`. Instead of yesterday's participation rates, it sends the health snapshot.

**Remove the `IsReportsEnabled` gate** — reports always fire; content adapts to chain state. The `IsReportsEnabled` flag and all its call sites are deleted.

### Report format (healthy chain — adds date context)

```
📊 [test12] Daily Summary — 2026-04-05

🟢 Chain HEALTHY — block #234900 (2s ago) — Consensus round 0
Validators voting: 7/7

Participation yesterday (Blocks 4000→4521):
  🟢 gnoops    (g1abc...) 99.5%
  🟡 samourai  (g1def...)  92%
```

The healthy report keeps yesterday's participation rates (useful for trend watching) AND adds the live health header.

### Report format (stuck/disabled) — same as `/status` output above

---

## Implementation plan

### Step 1 — New `ChainHealthSnapshot` struct and RPC query

**New file:** `backend/internal/gnovalidator/health.go`

```go
type ChainHealthSnapshot struct {
    // From RPC
    LatestBlockHeight int64
    LatestBlockTime   time.Time
    ConsensusRound    int
    VotingCount       int  // validators currently casting votes
    TotalValidators   int  // total validator set size
    RPCReachable      bool // false if RPC call failed

    // From in-memory flags
    IsStuck    bool
    IsDisabled bool

    // From DB
    ValidatorRates map[string]ValidatorRate  // last N blocks
    MinBlock, MaxBlock int64
}

func FetchChainHealthSnapshot(db *gorm.DB, chainID string, rpcClient RPCClient) ChainHealthSnapshot
```

`FetchChainHealthSnapshot` calls in parallel (goroutines + WaitGroup):
1. `rpcClient.Status()` → LatestBlockHeight, LatestBlockTime
2. `rpcClient.ConsensusState()` → parse "h/r/s" string → ConsensusRound
3. `rpcClient.DumpConsensusState()` → parse peer bitmasks → VotingCount, TotalValidators
4. `CalculateRecentValidatorStatus(db, chainID, N)` → ValidatorRates

If any RPC call fails, `RPCReachable = false` and those fields are zero — the report falls back to DB-only mode gracefully.

### Step 2 — New DB query: `CalculateRecentValidatorStatus`

**File:** `backend/internal/database/db_metrics.go`

```go
func CalculateRecentValidatorStatus(db *gorm.DB, chainID string, lastNBlocks int) (map[string]ValidatorRate, int64, int64, error)
```

```sql
SELECT addr, MAX(moniker) AS moniker,
    COUNT(*) AS total_blocks,
    SUM(CASE WHEN participated THEN 1 ELSE 0 END) AS participated_count,
    MIN(block_height) AS first_block,
    MAX(block_height) AS last_block
FROM daily_participations
WHERE chain_id = ?
  AND block_height >= (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - ?
GROUP BY addr
```

Reuses the existing `ValidatorRate` struct (no new type).

### Step 3 — Bitmask vote counter helper

**File:** `backend/internal/gnovalidator/health.go`

```go
// parseBitArrayVoteCount parses a Tendermint BitArray JSON object
// {"bits": "7", "elems": ["121"]} and returns (votedCount, totalBits).
func parseBitArrayVoteCount(bits int, elems []uint64) (int, int) {
    count := 0
    for _, e := range elems {
        count += bits.OnesCount64(e)
    }
    if count > bits {
        count = bits // cap at total (last elem may have padding bits)
    }
    return count, bits
}
```

Simple popcount — no reflection of which specific validators voted.

### Step 4 — `GetLastProgressTime` accessor

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

```go
func GetLastProgressTime(chainID string) (time.Time, bool) {
    timeMu.Lock()
    defer timeMu.Unlock()
    t, ok := lastProgressTime[chainID]
    return t, ok
}
```

Used to determine `IsStuck` in `FetchChainHealthSnapshot`.

### Step 5 — Rewrite `SendDailyStatsForUser`

**File:** `backend/internal/gnovalidator/gnovalidator_report.go`

Replace current single-path function. The RPC client must be passed in (already available in `CollectParticipation` scope — pass it through the scheduler or store it per-chain in a registry map similar to `chainmanager`).

```go
func SendDailyStatsForUser(db *gorm.DB, chainID string, rpcClient RPCClient,
    userID *string, chatID *int64, loc *time.Location)
```

3-branch format:
- `IsDisabled` → `formatDisabledReport`
- `IsStuck` → `formatStuckReport` (shows round number, time stuck, vote count, last-N-blocks rates)
- default → `formatHealthyReport` (shows live header + yesterday's rates)

**Remove** the `IsReportsEnabled` guard from `SheduleUserReport` and `SheduleTelegramReport`.

### Step 6 — Rewrite `/status` Telegram handler

**File:** `backend/internal/telegram/validator.go`

New `/status` handler: calls `FetchChainHealthSnapshot`, formats and sends the health snapshot. No pagination. No period parameter.

### Step 7 — Add `/rate` Telegram command

Same as old `/status` — calls `buildPaginatedResponse` with `"status"` key (no data change). Add `"rate"` case to `buildPaginatedResponse` that delegates to the same `formatParticipationRate` function.

Keep the old `"status"` callback key working to avoid breaking existing inline keyboards.

### Step 8 — Store RPC client per chain for access from report schedulers

**File:** `backend/internal/gnovalidator/gnovalidator_realtime.go`

Add a per-chain RPC client registry (similar to `chainmanager`):

```go
var chainRPCClients   = make(map[string]RPCClient)
var chainRPCClientsMu sync.RWMutex

func SetChainRPCClient(chainID string, client RPCClient)
func GetChainRPCClient(chainID string) (RPCClient, bool)
```

Populated in `StartValidatorMonitoring`. Read from `SendDailyStatsForUser` and the `/status` handler.

### Step 9 — `RecentBlocksWindow` constant

**File:** `backend/internal/gnovalidator/thresholds.go`

```go
RecentBlocksWindow int = 50
```

---

## New `/rate` Telegram command — backward compat

`/rate` handler added to `BuildTelegramHandlers`. `buildPaginatedResponse` gets a `"rate"` case that calls `formatParticipationRate` — same function as before. Old inline keyboard callbacks encoded with `"status"` key continue working.

Update `formatHelp()`:
```
/status          — chain health: last block age, consensus round, validator vote count
/rate            — historical participation rate (period-based)
/uptime          — uptime % (last 500 blocks)
/operation_time  — days since last downtime
/tx_contrib      — transaction contribution %
/missing         — missing blocks count
/report          — configure daily report schedule
/subscribe       — subscribe to validator alerts
/chain           — show current chain
/setchain        — switch chain
```

---

## Summary of all changes

| File | Change |
|---|---|
| `gnovalidator/health.go` *(new)* | `ChainHealthSnapshot`, `FetchChainHealthSnapshot`, `parseBitArrayVoteCount`, format helpers |
| `gnovalidator/gnovalidator_realtime.go` | Add `GetLastProgressTime`, `SetChainRPCClient`, `GetChainRPCClient`; delete `IsReportsEnabled` + helpers |
| `gnovalidator/gnovalidator_report.go` | Rewrite `SendDailyStatsForUser` (accepts rpcClient); remove `IsReportsEnabled` guard from schedulers |
| `gnovalidator/thresholds.go` | Add `RecentBlocksWindow = 50` |
| `database/db_metrics.go` | Add `CalculateRecentValidatorStatus` |
| `telegram/validator.go` | Rewrite `/status` handler; add `/rate` handler + `"rate"` case in `buildPaginatedResponse`; update `formatHelp` |
| `api/api-admin.go` | Remove `SetReportsEnabled` calls |
| `main.go` | Remove `SetReportsEnabled(chainID, true)` initialization calls |

No DB migration. No new external dependency.

---

## Discord and Slack impact

The daily report dispatch path is shared across all channels:

```
SendDailyStatsForUser
  └─ buildMsg (single string)
       ├─ userID path → SendUserReportInChunks → SendUserReportAlert (fonction.go:463)
       │                                              ├─ Discord webhook (SendDiscordAlert)
       │                                              └─ Slack webhook (SendSlackAlert)
       └─ chatID path → telegram.SendMessageTelegram
```

The message is built once and dispatched everywhere. The new report format (chain health header + validator liveness) will therefore appear on Discord and Slack automatically with no extra work.

**Formatting constraint:** Telegram uses HTML tags (`<b>`, `<code>`). `SendUserReportAlert` sends the same string to Discord and Slack without conversion. The new format helpers must therefore use plain-text-only formatting (emoji + spaces) for the shared message body, as is already the case today.

**Known pre-existing bug (out of scope):** `SendUserReportAlert` queries `webhook_validators` with no `chain_id` filter (`WHERE user_id = ?` only). A user with webhooks registered for multiple chains will receive the same report on all their webhooks regardless of chain. This is documented in CLAUDE.md Known Limitations and is not addressed in this PR.

---

## Open questions

1. **`/rate` backward compat** — keep both `"status"` and `"rate"` keys in `buildPaginatedResponse` forever, or only during a transition window?
2. **Liveness window size** — 50 blocks. On a 1 block/s chain that is 50s of history; on a slower chain it could be minutes. Should this be per-chain configurable?
3. **Per-validator bitmask** — currently we show a total vote count (5/7). If we also want "which specific validators are voting right now", we need to map bit indices to the `/validators` address list. Adds complexity; deferred.
4. **`/status` is a breaking UX change** — existing users used `/status` for historical rates. A transition message ("moved to /rate") for one release could soften it.
5. **RPC client registry pattern** — storing per-chain RPC clients adds a new global map. Alternative: pass the client through the scheduler goroutine closure instead of a registry. Less global state, but requires refactoring scheduler signatures.
