# Validator Health Report — Design

**Date:** 2026-07-08
**Branch:** `feat/validator-report` (from `main`)
**Status:** Approved design, pending implementation plan

## Purpose

Produce a per-validator "health score" for each enabled chain, quantifying how
much a validator disrupts the network based on its alert history over several
time windows (last 24h, current week, current month, current year). The score
helps operators identify validators that repeatedly harm chain liveness.

The report is delivered three ways:

1. A **JSON API** exposing the scores (consumed by the external `memba`
   frontend, which renders the public report page, and by this repo's admin
   `panel/`).
2. A **link** to the memba report page appended to the existing daily summary
   sent over Discord / Telegram / Slack (a link works on all three channels;
   file upload does not work with Slack incoming webhooks).
3. A **Reports page** in the admin `panel/` that consumes the same JSON API.

The feature is **enabled per chain** and can be toggled live from the admin
panel.

## Scope boundaries

In scope:
- Backend scoring query + pure scoring function + JSON API routes.
- Per-chain enable toggle stored in `admin_config`.
- Link injection into the daily report.
- Admin panel Reports page + toggle + optional client-side CSV/XLSX export.
- Documentation of the JSON API for the memba colleague.

Out of scope:
- Rendering the public memba report page (owned by a colleague, external repo).
- File attachments to Discord/Telegram (link-only delivery).
- Voting-power weighting of the score (only current power is available, not the
  power at the time of a past outage; deliberately excluded).
- Historical persistence of computed scores (scores are computed on demand from
  `alert_logs`).

## Scoring model

Score is computed **per validator, per period**, as a penalty-based value from a
base of 100. Periods reuse the exact bounds already implemented in
`GetAlertLog(db, chainID, period)` and `GetAlertLogsLast24h`:

- `last_24h` — rolling 24 hours
- `current_week` — Monday 00:00 local → +7 days
- `current_month` — 1st of month 00:00 UTC → +1 month
- `current_year` — Jan 1 00:00 UTC → +1 year

### Components

Source data: `alert_logs` rows (`level`, `addr`, `start_height`, `end_height`,
`sent_at`) plus block-count context for the period.

Two components only. Severity is the primary driver; downtime is a capped
secondary signal.

| Component | Raw metric | Penalty (starting weights, tunable) |
|---|---|---|
| Severity (primary) | `critical_count` = number of `CRITICAL` alert **rows** in period, resends included | `−6` per alert, capped at `−60` |
| Downtime (secondary) | `downtime_ratio` = Σ(`end_height − start_height`) / blocks in period | `−(downtime_ratio × k)`, capped at `−20` |

`score = max(0, round(100 − total_penalty))`

**No grouping into "episodes" and no separate flapping component** — this is
intentional. A validator down for a long time re-triggers a `CRITICAL` alert
every `alert_critical_resend_hours` (~24h), so a longer outage produces more
`CRITICAL` rows and a lower score. This is the desired incentive: maintenance
should be planned ahead and kept short. A separate flapping metric would be
redundant with the raw critical count.

### Tiers

| Score | Tier |
|---|---|
| 85–100 | Excellent |
| 60–84 | Good |
| 30–59 | Watch |
| 0–29 | Critical (network risk) |

### Notes / edge cases

- An ongoing outage has `end_height = 0`; treat the missing end as the current
  chain height when computing `downtime_blocks`.
- Only **dispatched** alerts should count. Suppressed (deduped) alerts are not
  stored as rows, so counting `alert_logs` rows already excludes them; `RESOLVED`
  rows are not penalties and must be excluded from `critical_count`.
- Starting weights (`6`, `k`, the caps) are placeholders to calibrate. They are
  stored in `admin_config` so they can be tuned without recompiling.

## Backend components

### `db_score.go` (package `database`)

```go
type ValidatorScoreRaw struct {
    Addr              string
    Moniker           string
    CriticalCount     int   // raw CRITICAL rows in period, resends included
    DowntimeBlocks    int64
    ParticipationRate float64
}

func GetValidatorScores(db *gorm.DB, chainID, period string) ([]ValidatorScoreRaw, error)
```

- Reuses the period-bound computation from `GetAlertLog`.
- Every query is scoped `WHERE chain_id = ?`.
- Resolves moniker via `COALESCE(am.moniker, al.moniker, '')` like the other
  moniker-bearing queries.

### `score` package (pure, no DB — TDD target)

```go
type Tier string // "Excellent" | "Good" | "Watch" | "Critical"

type Weights struct { /* per-component weights + caps, loaded from admin_config */ }

func Compute(raw database.ValidatorScoreRaw, periodBlocks int64, w Weights) (int, Tier)
```

- Pure function, unit-tested independently of the database.
- Deterministic; covers the caps and the tier boundaries.

### API routes (`api.go`)

- `GET /api/reports/validators?chain=X` → array; each element carries the
  validator's score + tier + raw components for **all four periods** in one
  response (convenient for both memba and the panel).
- `GET /api/reports/validators?chain=X&addr=Z` → same shape filtered to one
  validator (selection).
- Reject unknown chains with 400 via `Config.ValidateChainID`.
- **The API is always available**, regardless of the per-chain toggle. The toggle
  gates only the daily-report link injection (see below). The panel Reports page
  is also always available (admin-only surface).

Response shape (per validator):

```json
{
  "addr": "g1...",
  "moniker": "example",
  "periods": {
    "last_24h":      { "score": 92, "tier": "Excellent", "critical_count": 0, "downtime_blocks": 0, "participation_rate": 99.8 },
    "current_week":  { "score": 76, "tier": "Good", "critical_count": 1, "downtime_blocks": 120, "participation_rate": 98.1 },
    "current_month": { "...": "..." },
    "current_year":  { "...": "..." }
  }
}
```

## Configuration & toggle

- Per-chain enable flag stored in `admin_config` under key
  `validator_report_enabled.{chainID}` (bool, default `false`). Read via
  `GetAdminConfig` / written via `SetAdminConfig`, exposed through the existing
  admin config API so the panel can toggle it live.
- Global key `report_base_url` (memba base URL) used to build the report link.
- Scoring weights stored as `admin_config` keys with sane defaults.

## Daily report integration

In `SendDailyStatsForUser` (`gnovalidator_report.go`), after building `msg`, if
`validator_report_enabled.{chainID}` is true and `report_base_url` is set,
append a line:

```
📊 Validator report: {report_base_url}/reports/{chainID}
```

Applies to all delivery targets (web webhooks → Discord/Slack, Telegram) since
it is just a link.

## Admin panel (`panel/`)

- New sidebar entry under the **Monitoring** section: "Reports"
  (`panel/src/components/Sidebar.tsx`).
- New page `panel/src/pages/Reports.tsx`:
  - Chain selector.
  - Table of validators: moniker, address, score + tier per period.
  - Filter / select a single validator.
  - Per-chain enable toggle (writes the `admin_config` key).
  - Optional client-side CSV/XLSX export of the table.
- Consumes the same `/api/reports/validators` JSON API.

## Documentation for the memba colleague

A doc under `docs/` describing:
- The JSON API routes, query params, and response schema.
- The four period definitions and their bounds.
- The score/tier semantics.
- Example requests/responses.

## Implementation lots

1. **Backend** — `GetValidatorScores` query + `score` package + API routes +
   unit/integration tests.
2. **Integration** — per-chain `admin_config` toggle + weights, `report_base_url`,
   daily-report link injection, memba API doc.
3. **Panel** — Reports page + toggle UI + optional export.

## Resolved decisions

- **API gating:** the JSON API is always available. The `validator_report_enabled`
  toggle gates only the daily-report link injection. The panel Reports page is
  always available (admin-only).
- **Scoring source:** raw `CRITICAL` row count (resends included), no grouping
  into episodes, no separate flapping component. Severity primary, downtime a
  capped secondary. Rationale: incentivize planned, short maintenance.
- **Weights:** stored in `admin_config` (tunable without recompiling); starting
  values `−6` per critical (cap `−60`) and downtime cap `−20` are placeholders to
  calibrate. Exact `admin_config` key names to be finalized in the plan.
