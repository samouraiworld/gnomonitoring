# feat: add last-24h alert summary to daily report

## Context

The daily report (`SendDailyStatsForUser`) already shows participation rates for yesterday.
This feature adds a section at the bottom of the report listing validators that triggered
WARNING or CRITICAL alerts in the last 24 hours, so operators see both historical rates
and recent incident events in a single message.

---

## Impacted files

| File | Impact |
| --- | --- |
| `backend/internal/database/db_metrics.go` | New query `GetAlertLogsLast24h` |
| `backend/internal/gnovalidator/health.go` | New field on `ChainHealthSnapshot`; update `FormatHealthyReport` (and optionally `FormatStuckReport`) |
| `backend/internal/gnovalidator/health.go` | `FetchChainHealthSnapshot` populates new field |

No change to `gnovalidator_report.go`, `telegram/validator.go`, `main.go`, or Discord/Slack
dispatch — the message is a single plain-text string that flows through all channels unchanged.

---

## Existing infrastructure (do NOT reinvent)

| Component | Location | Role |
| --- | --- | --- |
| `AlertLog` struct | `db_init.go:116` | `chain_id`, `addr`, `moniker`, `level`, `start_height`, `end_height`, `skipped`, `sent_at` |
| `AlertSummary` struct | `db_init.go:136` | Read-only view: `Moniker`, `Addr`, `Level`, `StartHeight`, `EndHeight`, `Msg`, `SentAt` |
| `GetAlertLog` | `db_metrics.go:29` | Queries alert_logs by period — model to follow for the new query |
| `ChainHealthSnapshot` | `health.go` | Already populated by `FetchChainHealthSnapshot` — just add one field |
| `FormatHealthyReport` | `health.go:410` | Main format function for the healthy-chain daily report |
| `FormatStuckReport` | `health.go:386` | Format function for stuck chain — optionally add alerts section here too |

### `alert_logs` levels in practice

| Level | `skipped` | Meaning |
| --- | --- | --- |
| `WARNING` | `true` | Alert was sent, missed ≥ 5 blocks |
| `CRITICAL` | `true` | Alert was sent, missed ≥ 30 blocks |
| `RESOLVED` | `false` | Validator came back online |

`skipped = true` = alert was actually dispatched (field name is counter-intuitive but correct).
`RESOLVED` rows have `skipped = false` — include them in the summary for completeness.

---

## Goal

Add to the healthy (and optionally stuck) daily report:

```
⚠️ Alerts last 24h (4 events):
  🚨 CRITICAL  ProValidator   (g1xyz...) missed 30 blocks (#1124127→#1124156)
  🚨 CRITICAL  ProValidator   (g1xyz...) missed 30 blocks (#1124500→#1124529)
  ⚠️  WARNING   SlowVal        (g1abc...) missed 5 blocks  (#1124272→#1124276)
  ✅ RESOLVED  ProValidator   (g1xyz...) at block #1124530
```

If no alerts: omit the section entirely (do not print "Alerts last 24h: none").

### Formatting constraints

The daily report is **plain text** — no HTML tags. Same string goes to Telegram, Discord,
and Slack. Use emoji + spaces only (same style as the existing participation rate section).

**Deduplication by validator (Option B):** a validator that was down all day could produce
multiple WARNING and CRITICAL rows. The format helper deduplicates per `addr`:
- Keep the **worst level** per validator (`CRITICAL` > `WARNING`)
- Show the **alert count** for that validator (`— 4 alerts`)
- Show the **last sent_at** timestamp
- Add a separate `RESOLVED` line if a RESOLVED row exists for that validator

Max rendered lines: 10 validators + their RESOLVED lines. If more, append `… and N more.`

---

## Implementation plan

### ✅ Step 1 — `GetAlertLogsLast24h` in `db_metrics.go`

**File:** `backend/internal/database/db_metrics.go`

New function following the same pattern as `GetAlertLog`:

```go
// GetAlertLogsLast24h returns all alert_logs rows sent in the last 24 hours
// for the given chain, ordered by sent_at DESC. Includes WARNING, CRITICAL,
// and RESOLVED rows. Limits to 50 rows to cap message size.
func GetAlertLogsLast24h(db *gorm.DB, chainID string) ([]AlertSummary, error) {
    var results []AlertSummary
    err := db.Raw(`
        SELECT moniker, addr, level, start_height, end_height, msg, sent_at
        FROM alert_logs
        WHERE chain_id = ?
          AND sent_at >= datetime('now', '-24 hours')
          AND level IN ('WARNING', 'CRITICAL', 'RESOLVED')
        ORDER BY sent_at DESC
        LIMIT 50
    `, chainID).Scan(&results).Error
    return results, err
}
```

`LIMIT 50` gives the format helper enough rows to group/deduplicate per validator
(up to ~10 validators × 4 WARNING + 1 CRITICAL + 1 RESOLVED each = ~60 rows max in practice).

### ✅ Step 2 — Add `AlertsLast24h` field to `ChainHealthSnapshot`

**File:** `backend/internal/gnovalidator/health.go`

```go
type ChainHealthSnapshot struct {
    // existing fields ...
    AlertsLast24h []database.AlertSummary  // new — populated by FetchChainHealthSnapshot
}
```

### ✅ Step 3 — Populate in `FetchChainHealthSnapshot`

**File:** `backend/internal/gnovalidator/health.go`

Inside `FetchChainHealthSnapshot`, add one call (no goroutine needed — single fast query):

```go
alerts, err := database.GetAlertLogsLast24h(db, chainID)
if err != nil {
    log.Printf("[health][%s] GetAlertLogsLast24h error: %v", chainID, err)
    // non-fatal: leave AlertsLast24h nil
} else {
    snap.AlertsLast24h = alerts
}
```

### ✅ Step 4 — Format helper `formatAlertsLast24h`

**File:** `backend/internal/gnovalidator/health.go`

New private helper (plain text, no HTML). Deduplicates per validator:

```go
type validatorAlertSummary struct {
    Moniker     string
    Addr        string
    WorstLevel  string    // "CRITICAL" > "WARNING"
    Count       int       // total WARNING+CRITICAL events
    LastSentAt  time.Time
    Resolved    bool
    ResolvedAt  int64     // end_height of RESOLVED row
}

func formatAlertsLast24h(alerts []database.AlertSummary) string {
    if len(alerts) == 0 {
        return ""
    }

    // Group by addr — alerts are ordered sent_at DESC so first seen = most recent.
    byAddr := map[string]*validatorAlertSummary{}
    var order []string // preserve insertion order for display
    for _, a := range alerts {
        entry, exists := byAddr[a.Addr]
        if !exists {
            entry = &validatorAlertSummary{Moniker: a.Moniker, Addr: a.Addr}
            byAddr[a.Addr] = entry
            order = append(order, a.Addr)
        }
        switch a.Level {
        case "CRITICAL":
            entry.Count++
            entry.WorstLevel = "CRITICAL"
            if a.SentAt.After(entry.LastSentAt) {
                entry.LastSentAt = a.SentAt
            }
        case "WARNING":
            entry.Count++
            if entry.WorstLevel != "CRITICAL" {
                entry.WorstLevel = "WARNING"
            }
            if a.SentAt.After(entry.LastSentAt) {
                entry.LastSentAt = a.SentAt
            }
        case "RESOLVED":
            entry.Resolved = true
            entry.ResolvedAt = a.EndHeight
        }
    }

    var b strings.Builder
    b.WriteString(fmt.Sprintf("\n⚠️ Alerts last 24h (%d validator(s)):\n", len(order)))

    limit := 10
    extra := 0
    if len(order) > limit {
        extra = len(order) - limit
        order = order[:limit]
    }

    for _, addr := range order {
        e := byAddr[addr]
        addrShort := addr
        if len(addrShort) > 12 {
            addrShort = addrShort[:12] + "..."
        }
        var emoji string
        if e.WorstLevel == "CRITICAL" {
            emoji = "🚨"
        } else {
            emoji = "⚠️ "
        }
        b.WriteString(fmt.Sprintf("  %s %-8s  %-14s (%s) — %d alert(s) — last %s\n",
            emoji, e.WorstLevel, e.Moniker, addrShort,
            e.Count, e.LastSentAt.UTC().Format("15:04 UTC")))
        if e.Resolved {
            b.WriteString(fmt.Sprintf("  ✅ RESOLVED  %-14s (%s) at block #%d\n",
                e.Moniker, addrShort, e.ResolvedAt))
        }
    }
    if extra > 0 {
        b.WriteString(fmt.Sprintf("  … and %d more.\n", extra))
    }
    return b.String()
}
```

### ✅ Step 5 — Update `FormatHealthyReport`

**File:** `backend/internal/gnovalidator/health.go:410`

`FormatHealthyReport` currently receives `rates`, `minBlock`, `maxBlock` but NOT the full
`ChainHealthSnapshot`. Two options:

**Option A (preferred — minimal change):** Add `alerts []database.AlertSummary` as a new
parameter, call `formatAlertsLast24h(alerts)` at the end:

```go
func FormatHealthyReport(chainID, date string, rates map[string]ValidatorRate,
    minBlock, maxBlock int64, alerts []database.AlertSummary) string {
    // existing body unchanged ...
    b.WriteString(formatAlertsLast24h(alerts))
    return b.String()
}
```

Update the single call site in `gnovalidator_report.go`:
```go
msg = FormatHealthyReport(chainID, yesterday, rates, minBlock, maxBlock, snap.AlertsLast24h)
```

**Option B:** Pass the full `snap ChainHealthSnapshot` to `FormatHealthyReport`. More future-proof
but larger signature change. Prefer Option A for now.

### Step 6 — (Optional, skipped) Update `FormatStuckReport`

`FormatStuckReport` already shows liveness and rates. Appending `formatAlertsLast24h` there
is low-effort and gives operators context on what triggered alerts before the chain froze.
Same `snap.AlertsLast24h` field — just call `formatAlertsLast24h(snap.AlertsLast24h)` at
the end of `FormatStuckReport`. No signature change needed since it already receives `snap`.

---

## Expected output

### Healthy report (with alerts)

```
📊 [betanet] Daily Summary — 2026-04-07

Participation yesterday (Blocks 1124000 → 1124999):
  🟢 gnoops        (g1abc12345...) 99.5%
  🟡 samourai-crew (g1def67890...)  87%
  🔴 ProValidator  (g1xyz11111...)   0%

⚠️ Alerts last 24h (2 validator(s)):
  🚨 CRITICAL  ProValidator   (g1xyz11111...) — 3 alert(s) — last 18:32 UTC
  ✅ RESOLVED  ProValidator   (g1xyz11111...) at block #1124530
  ⚠️  WARNING   samourai-crew  (g1def67890...) — 2 alert(s) — last 14:10 UTC
```

### Healthy report (no alerts in last 24h)

```
📊 [betanet] Daily Summary — 2026-04-07

Participation yesterday (Blocks 1124000 → 1124999):
  🟢 gnoops        (g1abc12345...) 99.5%
  🟢 samourai-crew (g1def67890...)  98%
```

(No alerts section — omitted entirely.)

---

## Edge cases

- **Dead validator silenced by `DeadValidatorSilenceDays`**: a validator that stopped getting
  alerts due to the 7-day silence still has older alert rows in `alert_logs`. If those rows
  have `sent_at >= now - 24h`, they appear in the summary. This is correct behaviour — the
  report shows what was sent, not what the silence suppressed.
- **`skipped = false` for RESOLVED**: the query does not filter on `skipped` so both
  `skipped=true` (WARNING/CRITICAL sent) and `skipped=false` (RESOLVED sent) are included.
- **Discord/Slack 1500-char chunk boundary**: `SendUserReportInChunks` splits on `\n` at
  1500 chars. A 10-line alert section is ~400 chars — well within one chunk.
- **Telegram 4096-char message limit**: same analysis — 10 lines ≈ 400 chars.

---

## Files changed

| File | Change |
| --- | --- |
| `backend/internal/database/db_metrics.go` | Add `GetAlertLogsLast24h` |
| `backend/internal/gnovalidator/health.go` | Add `AlertsLast24h` field to `ChainHealthSnapshot`; populate in `FetchChainHealthSnapshot`; add `formatAlertsLast24h`; update `FormatHealthyReport` signature + body; optionally update `FormatStuckReport` |
| `backend/internal/gnovalidator/gnovalidator_report.go` | Update single call to `FormatHealthyReport` to pass `snap.AlertsLast24h` |

No DB migration. No new table. No new dependency. No change to scheduler, Telegram handlers,
Discord/Slack dispatch, or `main.go`.
