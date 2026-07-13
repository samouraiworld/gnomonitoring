# Alert Frequency (`freq`) — Score & Display Design

**Status:** Approved (design). Ready for implementation plan.
**Branch:** decided at plan time (new feature branch off `main`, once `fix/valset-membership-integrity` is merged).
**Goal:** Add a distinct-incident frequency signal to the validator score, alongside the existing raw `critical_count`/`warning_count`, without changing their current meaning.

## Context

The validator score already penalizes `critical_count`/`warning_count` — but those are raw counts of dispatched `alert_logs` rows, inflated by the resend mechanism (`alert_warning_resend_hours`/`alert_critical_resend_hours`, default 6h/24h): a single long outage generates multiple CRITICAL rows purely from re-notification, not from separate failures. A validator that flaps in and out repeatedly (several short, distinct outages) and a validator down continuously for days can end up with a similar raw count, even though the first is a worse reliability signal.

`freq` is a new signal — the count of **distinct incidents** per validator per period — computed by collapsing consecutive WARNING/CRITICAL alert_logs rows that aren't separated by a RESOLVED into a single incident. It is additive: `critical_count`/`warning_count` keep their current raw-count meaning in the API/panel/CSV unchanged.

## Decisions (locked)

- **Additive, not a replacement** — `critical_count`/`warning_count` are untouched (same query, same meaning). `freq` (JSON: `incident_count`) is a new field, new penalty term, new panel column.
- **Incident boundary** — a WARNING that escalates to CRITICAL with no RESOLVED in between is **one** incident (counts the failure, not the level transitions). A RESOLVED followed by a new WARNING/CRITICAL starts a **new** incident.
- **Scope** — WARNING + CRITICAL only, `addr <> 'all'` (chain-wide stagnation stays excluded, same as the rest of the score). Valset departure / address-change events are **out of scope** — they aren't persisted per-validator in `alert_logs` today (`WatchNewValidators` only calls `SendInfoValidator`, no `InsertAlertlog`); wiring them in is a separate, later change to the collection path, not required for this feature.
- **Penalty shape** — same model as `critical_count`/`warning_count`: `-FreqWeight` per distinct incident, capped at `FreqCap`. Not a rate (no normalization by period length) — keeps the formula consistent and easy to explain alongside the existing terms.
- **No schema change, no write-path change** — computed read-time in SQL from existing `alert_logs` rows. `WatchValidatorAlerts`/`SendResolveAlerts` (the real-time dedup/resend logic) are not touched.
- **Bounded scan only — no full-history scan** — see "Performance constraint" below. This is a hard requirement, not an optimization nice-to-have.

## Performance constraint (hard requirement)

`GetValidatorScores` is called **once per period** (`last_24h`, `current_week`, `current_month`, `current_year` — [api_report.go:147-148](backend/internal/api/api_report.go#L147-L148)), i.e. 4 times per `/api/reports/validators` request. On 2026-07-13, a sustained Postgres CPU incident was caused by six `daily_participations`/`daily_participation_agregas` queries each scanning all the way back to the period start (or unbounded) on every call, run in parallel every ~5 min (fixed in `659982c`, bounding each scan to the aggregation watermark instead). A naive `freq` implementation — computing `LAG() OVER (PARTITION BY addr ORDER BY sent_at)` over the **entire** `alert_logs` history for the chain, 4 times per report request — reproduces the exact same anti-pattern on a different table, and is strictly worse because it re-scans full history on every single page load/refresh rather than every 5 minutes.

**Required approach:** never scan more than (a) one row per validator before the period start, and (b) rows within the period. A plain `DISTINCT ON (addr) ... WHERE sent_at < period_start` does **not** satisfy (a): for a short period like `last_24h`, `sent_at < period_start` matches nearly the *entire* chain history, and Postgres has no native "skip scan" to jump straight to each addr's latest row — the planner sorts/scans the whole qualifying set. The bounded shape uses a `LATERAL` join instead, one `LIMIT 1` backward-index probe per validator that actually alerted this period:

```sql
WITH in_period AS (
    -- Same period bound already used today for critical_count/warning_count —
    -- no new cost here, this scan already exists.
    SELECT addr, level, sent_at, id
    FROM alert_logs
    WHERE chain_id = ? AND addr <> 'all'
      AND level IN ('WARNING','CRITICAL')
      AND sent_at >= ? AND sent_at < ?  -- period bounds
),
addrs AS (
    SELECT DISTINCT addr FROM in_period
),
prior AS (
    -- Per addr that alerted this period, one LIMIT-1 backward index probe for
    -- the closest preceding row — O(#addrs) index lookups, NOT a scan
    -- proportional to total alert_logs history.
    SELECT a.addr, p.level, p.sent_at, p.id
    FROM addrs a
    LEFT JOIN LATERAL (
        SELECT level, sent_at, id
        FROM alert_logs
        WHERE chain_id = ? AND addr = a.addr AND sent_at < ?  -- period start
        ORDER BY sent_at DESC, id DESC
        LIMIT 1
    ) p ON true
),
tagged AS (
    SELECT addr, level, sent_at, id,
           LAG(level) OVER (PARTITION BY addr ORDER BY sent_at, id) AS prev_level
    FROM (
        SELECT addr, level, sent_at, id FROM prior WHERE level IS NOT NULL
        UNION ALL
        SELECT addr, level, sent_at, id FROM in_period
    ) combined
)
SELECT addr, COUNT(*) AS incident_count
FROM tagged
WHERE sent_at >= ? AND sent_at < ?  -- exclude the prior-context row itself
  AND level IN ('WARNING','CRITICAL')
  AND (prev_level IS NULL OR prev_level = 'RESOLVED')
GROUP BY addr
```

`in_period` is the same bound the existing `critical_count`/`warning_count` query already pays. `addrs` is derived from it (only validators that alerted this period — a small set). `prior`'s `LATERAL` probe is index-backed by `idx_al_chain_addr_sentat` (`chain_id, addr, sent_at`) with `LIMIT 1`, so each probe is O(log n) regardless of chain age — total added cost is O(#addrs that alerted) index lookups, never a scan proportional to total historical `alert_logs` size. The `id` tiebreaker (autoincrement primary key) makes ordering deterministic when two rows share the same `sent_at` (seed data and real resends can collide at second granularity).

**Verification before merge:** `EXPLAIN ANALYZE` the final query against the populated production-shaped test data and confirm it index-scans (`idx_al_chain_addr_sentat`) rather than seq-scanning `alert_logs`, for both a fresh chain and one with a full year of history.

## Global Constraints

- English only for all comments, docs, commit messages.
- Every query on `alert_logs` keeps its `WHERE chain_id = ?` scoping and `addr <> 'all'` exclusion.
- Behavior-preserving for all existing fields: `critical_count`, `warning_count`, `score` for validators with `IncidentCount` unused (e.g. before this ships, or when `FreqWeight=0`) do not change. `freq` is purely additive.
- Backend tests use `testoutils.NewTestDB(t)`; DB tests require `TEST_DATABASE_DSN` (test Postgres on port 5433).

---

## Data layer — `backend/internal/database/db_score.go`

**`ValidatorScoreRaw`** gains `IncidentCount int` (json tag `incident_count` where the struct is reused for JSON, otherwise threaded manually like the other raw fields).

**`GetValidatorScores`** — extend the existing query with the **bounded** `in_period` + `addrs` + `prior` (LATERAL) CTE chain from the "Performance constraint" section above (not a full-history scan, not a `DISTINCT ON` over the whole pre-period range — see that section for the exact SQL and why). The `prior` CTE resolves, per validator that alerted this period, whether the first in-period WARNING/CRITICAL is a continuation of an already-open incident or the start of a new one, via one `LIMIT 1` index probe each — without scanning anything proportional to total history.

This is merged into the existing `GetValidatorScores` result set (same `addr`-keyed row), not a second round-trip if avoidable — join or merge in Go, whichever keeps the existing query readable.

---

## Score layer — `backend/internal/score/score.go`

- `Inputs.IncidentCount int` — new field, mirrors `CriticalCount`/`WarningCount` (non-negative, 0 until the collector/query feeds it).
- `Weights.FreqWeight int` (default `3`), `Weights.FreqCap int` (default `30`) — same scale as `WarningWeight`/`WarningCap` (2/20), slightly higher since a distinct incident is a stronger signal than a raw resend.
- `Compute()`: `freqPenalty := in.IncidentCount * w.FreqWeight`, capped at `w.FreqCap`, added into `totalPenalty` alongside `critPenalty`/`warnPenalty`/`downPenalty` (same severity multiplier applies to the sum, unchanged structure).
- New admin_config keys: `KeyFreqWeight = "report_score_freq_weight"`, `KeyFreqCap = "report_score_freq_cap"`, wired into `WeightsFromConfig` with `numOr` fallback to defaults, same pattern as the existing keys.

---

## API layer — `backend/internal/api/api_report.go`

- New field on the per-period response struct: `IncidentCount int json:"incident_count"`.
- Threaded from `ValidatorScoreRaw.IncidentCount` into `score.Inputs.IncidentCount` (mirrors how `CriticalCount`/`WarningCount` are threaded today), and surfaced as-is in the JSON output.

---

## Panel — `panel/src/types/report.ts`, `panel/src/pages/Reports.tsx`, `panel/src/components/ScoreLegend.tsx`

- `PeriodScore` type gains `incident_count: number`.
- New sortable column **"Freq"**, placed after **Warning count** (mirrors the existing `critical_count`/`warning_count` column pair), included in the per-period CSV export.
- `ScoreLegend.tsx`: add `report_score_freq_weight`/`report_score_freq_cap` to the mirrored defaults and the formula text: `− ${freqWeight} per distinct incident (max ${freqCap})`.

---

## Files Touched

- **Backend**
  - `backend/internal/database/db_score.go` — extend `GetValidatorScores` query + `ValidatorScoreRaw`.
  - `backend/internal/score/score.go` — `Inputs.IncidentCount`, `Weights.FreqWeight/FreqCap`, `Compute()` penalty term, admin_config keys + `WeightsFromConfig`.
  - `backend/internal/api/api_report.go` — new JSON field, threading.
- **Panel**
  - `panel/src/types/report.ts` — `incident_count: number` on `PeriodScore`.
  - `panel/src/pages/Reports.tsx` — new column (header, cell, sort case, CSV).
  - `panel/src/components/ScoreLegend.tsx` — new defaults + formula line.
- **Docs**
  - `CLAUDE.md` — "Validator Health Report" section: add `incident_count` to the surfaced fields list, `report_score_freq_weight`/`_cap` to the weights list (defaults 3/30).

## Testing

- **`db_score_test.go`** — seed scenarios and assert `incident_count`:
  - WARNING → CRITICAL (no RESOLVED in between) → **1** incident.
  - WARNING → RESOLVED → WARNING → **2** incidents.
  - An incident starting before the period, still resending inside it → **0** new incidents counted in that period (its start row is outside the period filter, and the in-period resend rows all have a non-RESOLVED `prev_level`).
  - `addr='all'` rows never counted, chain-scoping respected (a second chain's rows don't leak).
- **`score_test.go`** — nominal penalty case, cap-reached case, `FreqWeight=0` (no-op) case.
- **`weights_test.go`** — `WeightsFromConfig` parses `report_score_freq_weight`/`_cap`, falls back to defaults (3/30) when absent or non-numeric.
- **Panel** — `npm run build` (typecheck) for the new column/type; no assertion tests for the table itself (consistent with existing panel test coverage).
- **Performance verification** — `EXPLAIN ANALYZE` the bounded `prior`/`in_period` query against seeded data with a full year of history for a chain with several validators; confirm an index scan on `idx_al_chain_addr_sentat`, not a sequential scan on `alert_logs`. This is a merge-blocking check per the Performance constraint section, not optional.

## Out of Scope

- Valset departure / address-change events are not counted — they are not persisted per-validator in `alert_logs` today. Adding them would require modifying `WatchNewValidators` to call `InsertAlertlog` with a new level, which is a separate collection-path change.
- No rate normalization (incidents/day) — `freq` stays a capped raw count, same shape as `critical_count`/`warning_count`.
- `critical_count`/`warning_count` are not redefined — their existing raw-count meaning in the API/panel/CSV is unchanged.
