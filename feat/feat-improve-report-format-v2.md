# feat: improve daily report format (v2)

## Observations from actual output (2026-04-07)

After receiving real reports on both test12 (stuck) and gnoland1 (healthy), four issues
were identified.

---

## Issues

### 1. No chain status line in the healthy chain report

`FormatHealthyReport` starts directly with participation rates — no block height, no age,
no consensus round. On gnoland1 (healthy), the report has no live context at all.

**Current output:**
```
📊 [gnoland1] Daily Summary — 2026-04-06

Participation yesterday (Blocks 423756 → 447584):
  🟢 onbloc-val-01  (g10j90...) 100.0%
  ...
```

**Target:**
```
📊 [gnoland1] Daily Summary — 2026-04-06

🟢 Block #447584 (2s ago) — Consensus: round 0 — Normal

Participation yesterday (Blocks 423756 → 447584):
  🟢 onbloc-val-01  (g10j90...) 100.0%
  ...
```

### 2. Stuck chain uses the wrong title and emoji

`FormatStuckReport` starts with `chainStatusEmoji(snap)` (🚨) and "Chain status" instead
of the 📊 Daily Summary format. The user wants a consistent header across all states.

**Current:**
```
🚨 [test12] Chain status — block #234887 (4d 4h ago)
Consensus: round 713 — STUCK since 2026-04-03 17:40 UTC
```

**Target:**
```
📊 [test12] Daily Summary — 2026-04-07

🚨 Block #234887 (4d 4h ago) — Consensus: round 713 — STUCK since 2026-04-03 17:40 UTC
```

### 3. Replace "Alerts last 24h" with "Missed blocks last 24h"

The alerts section lists WARNING/CRITICAL/RESOLVED events. It is noisy and duplicates what
was already received in real-time. A per-validator count of missed blocks in the last 24h
is more actionable and easier to read at a glance.

**Current:**
```
⚠️ Alerts last 24h (4 validator(s)):
  🚨 CRITICAL  unknown        (g1pdg6...) — 3 alert(s) — last 21:38 UTC
  ✅ RESOLVED  unknown        (g1pdg6...) at block #469110
  ⚠️  WARNING   zxq-val-01     (g1u3q5...) — 2 alert(s) — last 21:24 UTC
  ...
```

**Target:**
```
Missed blocks last 24h:
  🔴 unknown        (g1pdg6r8...) 35 missed
  🟡 zxq-val-01     (g1u3q5s5...) 5 missed
```

If no validator missed blocks: omit the section entirely (clean report).

---

## Target output for all three states

The separator line used between sections is `──────────────────────────` (Unicode box-drawing
character, renders cleanly in Discord, Slack, and Telegram).

### Healthy chain

```
📊 [gnoland1] Daily Summary — 2026-04-06
──────────────────────────
🟢 Block #447584 (2s ago) — Consensus: round 0 — Normal
──────────────────────────
Participation yesterday (Blocks 423756 → 447584):
  🟢 onbloc-val-01    (g10j90aqjv...) 100.0%
  🟢 samourai-crew-1  (g1kn7p0wqu...) 100.0%
  🟡 unknown          (g1u4z9tu4q...)  93.8%
──────────────────────────
Missed blocks last 24h:
  🟡 unknown          (g1u4z9tu4q...) 12 missed
```

### Stuck chain

```
📊 [test12] Daily Summary — 2026-04-07
──────────────────────────
🚨 Block #234887 (4d 4h ago) — Consensus: round 713 — STUCK since 2026-04-03 17:40 UTC
──────────────────────────
Missed blocks last 24h:
  🔴 ProValidator  (g1xyz...) 50 missed
```

### Disabled chain

```
📊 [test12] Daily Summary — 2026-04-07
──────────────────────────
⚫ Monitoring OFF — Last known block: #234887 at 2026-04-03 17:40 UTC
```

---

## Implementation plan

### Step 1 — New DB query `GetMissedBlocksLast24h`

**File:** `backend/internal/database/db_metrics.go`

```go
type MissedBlockCount struct {
    Addr    string
    Moniker string
    Missed  int
}

func GetMissedBlocksLast24h(db *gorm.DB, chainID string) ([]MissedBlockCount, error) {
    var result []MissedBlockCount
    err := db.Raw(`
        SELECT addr, MAX(moniker) AS moniker, COUNT(*) AS missed
        FROM daily_participations
        WHERE chain_id = ?
          AND participated = 0
          AND date >= datetime('now', '-24 hours')
        GROUP BY addr
        ORDER BY missed DESC
    `, chainID).Scan(&result).Error
    return result, err
}
```

### Step 2 — New format helper `FormatMissedBlocksLast24h`

**File:** `backend/internal/gnovalidator/health.go`

```go
func FormatMissedBlocksLast24h(rows []database.MissedBlockCount) string {
    if len(rows) == 0 {
        return ""
    }
    var sb strings.Builder
    sb.WriteString("\nMissed blocks last 24h:\n")
    for _, r := range rows {
        moniker := r.Moniker
        if moniker == "" {
            moniker = "unknown"
        }
        addrShort := r.Addr
        if len(addrShort) > 10 {
            addrShort = addrShort[:10] + "..."
        }
        emoji := validatorRateEmoji(100 - float64(r.Missed))  // reuse existing thresholds
        sb.WriteString(fmt.Sprintf("  %s %-14s (%s) %d missed\n",
            emoji, moniker, addrShort, r.Missed))
    }
    return sb.String()
}
```

Also add HTML-safe variant `FormatMissedBlocksLast24hHTML` (with `html.EscapeString` on
moniker and addr) for the Telegram `/status` path.

### Step 3 — Update `ChainHealthSnapshot`

**File:** `backend/internal/gnovalidator/health.go`

Replace `AlertsLast24h []database.AlertSummary` with:
```go
MissedLast24h []database.MissedBlockCount
```

Update `FetchChainHealthSnapshot` to call `GetMissedBlocksLast24h` instead of
`GetAlertLogsLast24h`.

### Step 4 — Update `FormatHealthyReport`

**File:** `backend/internal/gnovalidator/health.go`

Add a chain status line after the title:

```go
func FormatHealthyReport(chainID, date string, snap ChainHealthSnapshot,
    rates map[string]ValidatorRate, minBlock, maxBlock int64) string {

    var sb strings.Builder
    sb.WriteString(fmt.Sprintf("📊 [%s] Daily Summary — %s\n\n", chainID, date))

    // Chain status line (block height + consensus)
    if snap.RPCReachable {
        emoji := chainStatusEmoji(snap)
        blockAge := formatBlockAge(snap.LatestBlockTime)
        sb.WriteString(fmt.Sprintf("%s Block #%d (%s) — Consensus: round %d — %s\n\n",
            emoji, snap.LatestBlockHeight, blockAge,
            snap.ConsensusRound, consensusLabel(snap.ConsensusRound)))
    }

    sb.WriteString(fmt.Sprintf("Participation yesterday (Blocks %d → %d):\n", minBlock, maxBlock))
    sb.WriteString(formatValidatorRates(rates))
    sb.WriteString(FormatMissedBlocksLast24h(snap.MissedLast24h))
    return sb.String()
}
```

Update call site in `gnovalidator_report.go` to pass `snap`.

### Step 5 — Update `FormatStuckReport`

**File:** `backend/internal/gnovalidator/health.go`

```go
func FormatStuckReport(chainID, date string, snap ChainHealthSnapshot) string {
    var sb strings.Builder
    sb.WriteString(fmt.Sprintf("📊 [%s] Daily Summary — %s\n\n", chainID, date))

    emoji := chainStatusEmoji(snap)
    blockAge := formatBlockAge(snap.LatestBlockTime)
    stuckSince := ""
    if !snap.LatestBlockTime.IsZero() {
        stuckSince = fmt.Sprintf(" since %s UTC", snap.LatestBlockTime.UTC().Format("2006-01-02 15:04"))
    }
    sb.WriteString(fmt.Sprintf("%s Block #%d (%s) — Consensus: round %d — %s%s\n",
        emoji, snap.LatestBlockHeight, blockAge,
        snap.ConsensusRound, consensusLabel(snap.ConsensusRound), stuckSince))

    sb.WriteString(FormatMissedBlocksLast24h(snap.MissedLast24h))
    return sb.String()
}
```

Signature gains `date string` parameter. Update call site in `gnovalidator_report.go`.

### Step 6 — Update `FormatDisabledReport`

**File:** `backend/internal/gnovalidator/health.go`

```go
func FormatDisabledReport(chainID, date string, snap ChainHealthSnapshot) string {
    var sb strings.Builder
    sb.WriteString(fmt.Sprintf("📊 [%s] Daily Summary — %s\n\n", chainID, date))
    sb.WriteString("⚫ Monitoring OFF")
    if snap.LatestBlockHeight > 0 {
        sb.WriteString(fmt.Sprintf(" — Last known block: #%d at %s UTC",
            snap.LatestBlockHeight,
            snap.LatestBlockTime.UTC().Format("2006-01-02 15:04")))
    }
    sb.WriteString("\n")
    return sb.String()
}
```

### Step 7 — Update `SendDailyStatsForUser` call sites

**File:** `backend/internal/gnovalidator/gnovalidator_report.go`

Pass `date` to `FormatStuckReport` and `FormatDisabledReport`. Pass `snap` to
`FormatHealthyReport`.

### Step 8 — Update mirror struct and bridge in `telegram/validator.go` and `main.go`

Replace `AlertsLast24h []database.AlertSummary` with
`MissedLast24h []database.MissedBlockCount` in:
- `telegram.ChainHealthSnapshot` mirror struct
- the bridge closure in `main.go`

Update `formatChainHealthMessage` in `telegram/validator.go` to use
`FormatMissedBlocksLast24hHTML` instead of `AlertsFormatter`.

---

## Files changed

| File | Change |
|---|---|
| `database/db_metrics.go` | Add `MissedBlockCount` type + `GetMissedBlocksLast24h` |
| `gnovalidator/health.go` | Add `FormatMissedBlocksLast24h` + HTML variant; update `ChainHealthSnapshot`; update all three `Format*Report` functions |
| `gnovalidator/gnovalidator_report.go` | Pass `date` and `snap` to format functions |
| `telegram/validator.go` | Update mirror struct; use new missed-blocks formatter |
| `main.go` | Update bridge closure |

No DB migration. No new external dependency.
