# Validator Report — Two New Columns Design

**Status:** Approved (design). Ready for implementation plan.
**Branch:** to be created from the current work (`feat/validator-score-v2`) or a new feature branch — decided at plan time.
**Goal:** Add two display columns to the validator report — a per-period **Missed block** count and a global **Days since last alert** — without changing the score model.

## Context

The validator report (`GET /api/reports/validators`) currently surfaces, per period, `score/tier/sign_rate/proposer_reliability/voting_power/critical_count/warning_count/downtime_blocks`. Investigation showed that **missed blocks already drive the score** through the sign-rate base (`base = 100 × signed/total`); the `downtime_blocks` penalty is CRITICAL-only, which is why it reads 0 for validators whose outages never reached the 30-consecutive-missed CRITICAL threshold. Both additions here are **display-only** — they make existing signals visible, they do not alter `score.Compute`.

## Decisions (locked)

- **Missed block** — display-only column, **per period**, `missed = TotalBlocks − SignedBlocks`. No new query (both values already returned by `GetValidatorParticipation`). Score unchanged.
- **Days since last alert** — display-only column, **global per validator** (one value regardless of selected period). Based on the most recent `WARNING` or `CRITICAL` alert. `addr='all'` "blockchain stuck" rows are **excluded** (they don't reflect an individual validator's health). `nil` when the validator has never alerted → rendered as `—`.

## Global Constraints

- English only for all comments, docs, commit messages.
- Every query on `alert_logs` (and the participation tables) keeps its `WHERE chain_id = ?` scoping.
- `score` package stays untouched — no scoring change.
- Behavior-preserving for all existing fields: no existing JSON field, score, tier, or CSV cell changes value; the two features are purely additive.
- Backend tests use `testoutils.NewTestDB(t)`; DB tests require `TEST_DATABASE_DSN` (test Postgres on port 5433).

---

## Feature 1 — Missed block (per period)

**Data.** `missed = in.TotalBlocks − in.SignedBlocks`, computed in the handler from the already-merged `score.Inputs`. No DB change.

**API shape.** Add to `periodScore` (api_report.go):
```go
MissedBlocks int64 `json:"missed_blocks"`
```
Set it in the per-period loop: `ps.MissedBlocks = in.TotalBlocks - in.SignedBlocks`. For the absent-period fill (`score.Inputs{}`), it is naturally 0.

**Panel.** New per-period column **"Missed"**, placed after **Downtime Blocks**. Sortable (numeric). Added to the per-period CSV fields. Cell shows `p.missed_blocks` when the period entry exists, `—` otherwise (same `p ? … : '—'` guard as the other period cells).

**Edge case.** A period with no participation rows → `total = 0` → `missed = 0`. When the whole period entry is absent, the panel already renders `—`.

---

## Feature 2 — Days since last alert (global)

**Data.** New DB function in `db_score.go`:
```go
// GetLastAlertTimes returns, per validator, the timestamp of its most recent
// WARNING or CRITICAL alert on the chain. Chain-scoped. The chain-wide
// "blockchain stuck" rows (addr = 'all') are excluded — they don't reflect an
// individual validator's health. Validators with no such alert are absent from
// the map.
func GetLastAlertTimes(db *gorm.DB, chainID string) (map[string]time.Time, error)
```
Query:
```sql
SELECT addr, MAX(sent_at) AS last_alert
FROM alert_logs
WHERE chain_id = ?
  AND level IN ('WARNING','CRITICAL')
  AND addr <> 'all'
GROUP BY addr
```
Called **once** in the handler, before the period loop (like `GetValidatorVP`).

**Computation.** In the handler, per validator: if a timestamp exists, `days := int(time.Now().UTC().Sub(t).Hours() / 24)` (full days elapsed, UTC — timezone-safe, consistent with the report's UTC period bounds). Absent → leave `nil`.

**API shape.** Add to `validatorReport` (top level, one value per validator, alongside `Moniker`):
```go
DaysSinceLastAlert *int `json:"days_since_last_alert"`
```
Set when the validator is first inserted into `byAddr` (and it is global, so it does not belong in `periodScore`). Because the roster seed happens before the period loop, set it there and on any addr discovered later.

**Panel.** New **global** column **"Last alert (d)"**, placed after **Address** (with the other period-independent columns). Sortable, with `nil` sorted last (mirror the existing `proposer_reliability ?? -1` pattern). Added to the global CSV fields (`['moniker','address','last_alert', …]`). Cell shows the integer or `—` when `nil`.

**Edge cases.** Never alerted → absent from the map → `nil` → `—`. Only `addr='all'` alerts → excluded → `nil`.

---

## Files Touched

- **Backend**
  - `backend/internal/database/db_score.go` — add `GetLastAlertTimes`.
  - `backend/internal/api/api_report.go` — add `periodScore.MissedBlocks` + compute; add `validatorReport.DaysSinceLastAlert`; call `GetLastAlertTimes` once and inject days into each report.
- **Panel**
  - `panel/src/types/report.ts` — `missed_blocks: number` on `PeriodScore`; `days_since_last_alert: number | null` on the validator report type.
  - `panel/src/pages/Reports.tsx` — two columns (headers, cells, sort cases), CSV headers/rows, `colSpan` bump (current 10 → 12).
- **Docs**
  - `docs/validator-report-api.md` — document the two new fields.
  - `CLAUDE.md` — one-line note in the Validator Health Report section.

## Testing

- **`GetLastAlertTimes`** (db_score_test.go): seed WARNING + CRITICAL + RESOLVED + `addr='all'` rows across two validators; assert the map returns the latest WARNING/CRITICAL per real addr, excludes `addr='all'`, ignores RESOLVED, and is chain-scoped (a second chain's rows don't leak).
- **Handler** (api_report_test.go): assert `missed_blocks == total − signed` for a period with known participation; assert `days_since_last_alert` is a plausible integer for a validator with a seeded alert and `null` for one with none.
- **Panel**: `npm run build` (typecheck) — no assertion tests for the table.

## Out of Scope

- No change to `score.Compute` or any weight. Missed blocks remain counted once (via sign rate); the CRITICAL-only nature of `downtime_blocks` is unchanged.
- No new alerting or collection logic; both features read existing data.
