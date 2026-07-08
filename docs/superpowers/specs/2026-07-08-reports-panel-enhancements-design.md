# Reports Panel & Alert History Enhancements — Design

**Date:** 2026-07-08
**Scope:** Admin panel front-end only (`panel/`). No backend changes, no new dependencies.

## Goal

Improve two admin-panel pages:

1. **Reports** (`panel/src/pages/Reports.tsx`) — sortable columns, tier filter, CSV export.
2. **Alert History** (`panel/src/pages/AlertHistory.tsx`) — validator selection filter.

All work is client-side. Report data (`ValidatorReport[]`, all four periods per validator)
is already fully loaded on the client; alert selection filters the already-fetched
`AlertLog[]` list. Styling follows existing inline-style conventions (`form-input`, `badge`,
`card`, `table-container`). React 19, no data-grid library.

## Context

- `Reports.tsx` already holds `reports: ValidatorReport[]`, a text `filter`, a `chain`
  select, and a `period` select (`last_24h | current_week | current_month | current_year`).
  The table renders one row per validator for the selected period. Tier badge classes exist
  (`TIER_BADGE_CLASS`).
- `AlertHistory.tsx` holds `alerts: AlertLog[]` fetched from `GET /admin/alerts` with
  `chain`, `level`, `limit` params. The backend handler (`handleGetAlerts` in
  `internal/api/api-admin.go`) does **not** support an `addr` param — confirmed. Alerts use
  `addr === 'all'` / `moniker === 'all'` for system-level alerts.

## Feature 1 — Sortable columns (Reports)

- Add `sortKey: string | null` and `sortDir: 'asc' | 'desc'` state.
- Each sortable `<th>` becomes clickable:
  - 1st click → ascending; 2nd click → descending; 3rd click → clear (back to default
    sort: **score descending**).
  - Show a ▲ / ▼ indicator on the active column header.
- Sortable columns and their comparators:
  - `moniker` — case-insensitive string compare.
  - `addr` — string compare.
  - `score`, `critical`, `warning`, `downtime` — numeric compare on the **selected period's**
    `PeriodScore` fields.
  - `tier` — ordinal by tier rank `Excellent(3) > Good(2) > Watch(1) > Critical(0)`
    (NOT alphabetical).
- Sorting runs **after** filtering (text + tier) and operates on the selected period.
- Validators with no `PeriodScore` for the selected period (`p` undefined) sort to the
  **end** of the list regardless of direction.

## Feature 2 — Tier filter (Reports)

- Add a `tierFilter: string` state (`''` = all).
- New `<select>` in the filter bar: `All Tiers`, `Excellent`, `Good`, `Watch`, `Critical`.
- Filters on the validator's tier for the **selected period**, combined with the existing
  text filter using AND.
- A validator with no `PeriodScore` for the selected period is excluded when a specific tier
  is selected.
- The `N validators shown` subtitle reflects the combined (text + tier) result.

## Feature 3 — CSV export (Reports) — all four periods

- `Export CSV` button in the page header (next to the Reports-enabled toggle).
- Pure client-side generation: build a CSV string → `Blob` → `URL.createObjectURL` →
  temporary `<a download>` → click → revoke URL. No library.
- Row set: the **current filtered set** (respects text + tier filters), in the **currently
  displayed sort order**.
- Columns (22 total): `moniker`, `address`, then for each period in fixed order
  `last_24h, current_week, current_month, current_year`:
  `<period>_score, <period>_tier, <period>_critical, <period>_warning, <period>_downtime`.
- Missing `PeriodScore` cells are written as empty strings.
- CSV escaping: wrap any field containing `"`, `,`, or newline in double quotes and double
  internal quotes (handles monikers with commas/quotes).
- Filename: `validator-report-<chain>-<YYYY-MM-DD>.csv`.

## Feature 4 — Validator selection (Alert History)

- Add `validatorFilter: string` state (`''` = all), stores the selected `addr`.
- New `<select>` in the filter bar: `All Validators` plus one option per **distinct
  validator present in the loaded `alerts`**:
  - Dedupe by `addr`; sort options by moniker (case-insensitive).
  - `addr === 'all'` is labelled `System`.
  - Option label: moniker (or `System`); option value: `addr`.
- Filtering is client-side on the already-fetched `alerts` array (no network call).
- When `chain` / `level` / `limit` changes and the selected `addr` no longer appears in the
  new `alerts` set, reset `validatorFilter` to `''` (avoid a silently empty table).
- The `N alerts shown` subtitle reflects the filter.

## Assumed limitations (accepted)

- The Alert History validator dropdown only lists validators appearing within the current
  `limit` window. Raising `limit` widens the list. This is intentional (no backend `addr`
  param added).
- The repo has no front-end test harness. Verification is `npm run build` (type-check +
  bundle) from `panel/` plus manual review. No JS unit tests are added.

## Files touched

- `panel/src/pages/Reports.tsx` — sort state/handlers, tier filter select, CSV export button
  + helper.
- `panel/src/pages/AlertHistory.tsx` — validator select, derive-distinct-validators helper,
  filter + reset logic.
- (Optional) a small CSV-escape helper may live inline in `Reports.tsx` or in
  `panel/src/lib/` if a shared spot fits existing conventions — decided at implementation
  time; default is inline to avoid over-abstraction.

No `panel/src/types/` changes required (existing `ValidatorReport`, `PeriodScore`,
`AlertLog` cover everything).
