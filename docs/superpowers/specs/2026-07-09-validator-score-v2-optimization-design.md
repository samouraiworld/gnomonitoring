# Validator Score v2 — Optimization Design

**Status:** Approved (design). Ready for implementation plan.
**Branch:** `feat/validator-score-v2` (continues on the existing branch).
**Goal:** Resolve the seven code-review findings on the score-v2 work as **behavior-preserving** refactors — remove duplication, cut hot-path query fan-out, batch the voting-power upsert, and drop redundant migrations — without changing any score value, JSON field, or CSV cell.

## Guiding Constraints

- **Behavior-preserving.** No observable change to scores, the `/api/reports/validators` payload, the panel, or the CSV export. Existing test suites are the safety net and must stay green: `score_test`, `weights_test`, `db_score_test`, `api_report_test`, `aggregator_test`, `periodbounds_internal_test`, `db_vp_test`.
- **English only** for all comments/docs/commit messages (repo CLAUDE.md rule).
- **`chain_id` scoping** preserved on every query touching `daily_participations`, `daily_participation_agregas`, `alert_logs`, `addr_monikers` (repo CLAUDE.md rule).
- **`score` package stays DB-free / pure.**
- New tests added only where a boundary is worth pinning; refactors otherwise ride the existing suites.
- Commit after each phase.

## Findings → Changes Map

| # | Finding | Change |
|---|---------|--------|
| 1 | Partition logic duplicated across two DB functions | A1 — `computePartition` helper |
| 2 | Report handler ~13 DB round-trips; redundant re-scans | A2 — fold chain-blocks into participation round-trip |
| 3 | Per-validator VP upsert loop every 5 min | A3 + B2 — batch upsert |
| 4 | `addColumnIfMissing` redundant with AutoMigrate | A4 — delete, trust AutoMigrate |
| 5 | Parallel `monikers` map + `ensure` closure in handler | C1 — single `merged` map via helper |
| 6 | `proposerAddr` `.String()` duplicated in 3 collectors | B1 — compute once, derive `txProposer` |
| 7 | `floatOr`/`intOr` clone; missing indexes | A5 — generic `numOr`; indexes: no change (documented) |

---

## Phase A — DB layer

Files: `backend/internal/database/db_score.go`, `db.go`, `db_init.go`; `backend/internal/score/score.go`.

### A1 — Single partition helper (#1)

Extract the `todayStart / rawStart / agregaStart / agregaEnd / includeAgrega` computation — currently duplicated verbatim in `GetValidatorParticipation` and `GetChainTotalBlocks` — into one unexported helper in `db_score.go`:

```go
// periodPartition describes how a report period splits across the durable
// daily aggregate (complete past days) and the raw current-day rows, with the
// seam fixed at today 00:00 UTC to avoid double counting.
type periodPartition struct {
	rawStart, end          time.Time // raw window [rawStart, end)
	agregaStart, agregaEnd string    // aggregate window [agregaStart, agregaEnd) as YYYY-MM-DD
	includeAgrega          bool
}

func computePartition(period string, now time.Time) (periodPartition, error) {
	start, end, err := periodBounds(period, now)
	if err != nil {
		return periodPartition{}, err
	}
	nowUTC := now.UTC()
	todayStart := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	rawStart := todayStart
	if period == "last_24h" {
		rawStart = start
	}
	if rawStart.Before(start) {
		rawStart = start
	}
	return periodPartition{
		rawStart:      rawStart,
		end:           end,
		agregaStart:   start.Format("2006-01-02"),
		agregaEnd:     todayStart.Format("2006-01-02"),
		includeAgrega: period != "last_24h" && todayStart.After(start),
	}, nil
}
```

Both callers use it. The double-count seam now lives in exactly one place.

**Test:** internal `TestComputePartition` in `periodbounds_internal_test.go` (or a new internal test file) asserting `rawStart`, `includeAgrega`, and the day-string bounds for `last_24h`, `current_week`, `current_month`, `current_year` against a fixed non-UTC `now`.

### A2 — Fold chain-blocks into the participation round-trip (#2)

`GetChainTotalBlocks` is deleted. Its value is returned from `GetValidatorParticipation`, which becomes:

```go
func GetValidatorParticipation(db *gorm.DB, chainID, period string) ([]ParticipationRaw, int64, error)
```

The chain-block count keeps its **exact current SQL semantics** — `COUNT(DISTINCT block_height)` over the raw window plus, **only when `includeAgrega`**, `SUM(MAX(total_blocks))` per day over the aggregate window — attached to each participation row via a `CROSS JOIN` to that scalar. This gating matches the deleted `GetChainTotalBlocks`, which added the aggregate term only for `period != "last_24h" && todayStart.After(start)`; without it, `last_24h` would wrongly fold in aggregate rows. The caller reads the scalar from any returned row (0 when there are no rows). One DB round-trip per period instead of two; identical value.

Query shape — **both** the participation aggregate arm and the chain-blocks aggregate sub-term are included only when `includeAgrega`:

```sql
SELECT combined.addr AS addr,
       SUM(combined.signed)   AS signed_blocks,
       SUM(combined.total)    AS total_blocks,
       SUM(combined.proposed) AS proposed_blocks,
       cb.chain_blocks        AS chain_blocks
FROM ( <raw arm>  UNION ALL  <agrega arm> ) combined
CROSS JOIN (
    SELECT (SELECT COUNT(DISTINCT block_height) FROM daily_participations
            WHERE chain_id = ? AND date >= ? AND date < ?)
         + COALESCE((SELECT SUM(day_blocks) FROM (
              SELECT MAX(total_blocks) AS day_blocks
              FROM daily_participation_agregas
              WHERE chain_id = ? AND block_date >= ? AND block_date < ?
              GROUP BY block_date) t), 0) AS chain_blocks
) cb
GROUP BY combined.addr, cb.chain_blocks
ORDER BY combined.addr
```

The empty-result case (no participation rows in a period) still yields `chainBlocks = 0`; the handler already treats missing participation as score 0 / Critical, so behavior is unchanged. `ParticipationRaw` gains no exported chain-block field — the scalar is read into a local and returned as the second value.

`GetValidatorParticipation`'s raw/agrega arms and the chain-blocks subqueries all derive their bounds from `computePartition` (A1).

**Tests:** existing `TestGetValidatorParticipation_*` updated for the new signature; existing `TestGetChainTotalBlocks` retargeted to assert the chain-blocks value now returned by `GetValidatorParticipation` (same expected values — 2 distinct heights, 81/102 union case).

### A3 — Batch VP upsert (#3)

Add to `db.go`:

```go
type AddrVP struct {
	Addr        string
	VotingPower int64
}

// UpsertAddrMonikerVPBatch writes voting power for many validators in chunked
// multi-row upserts, inserting a row (empty moniker) when none exists. Scoped
// to chain_id. Same per-row semantics as UpsertAddrMonikerVP.
func UpsertAddrMonikerVPBatch(db *gorm.DB, chainID string, rows []AddrVP) error
```

One `INSERT INTO addr_monikers (chain_id, addr, moniker, voting_power) VALUES (?,?, '', ?), … ON CONFLICT(chain_id, addr) DO UPDATE SET voting_power = excluded.voting_power`, chunked at the `flushChunk` convention (4 bind cols → conservative ~247 rows/chunk, well under Postgres' 65535). `UpsertAddrMonikerVP` (single-row) is retained for existing callers/tests.

**Test:** `TestUpsertAddrMonikerVPBatch` — upsert a batch, assert per-addr `voting_power`, and cross a chunk boundary (e.g. 300 rows) to prove chunking.

### A4 — Remove redundant migrations (#4)

`AutoMigrate` at `db_init.go:418` already runs over `DailyParticipation`, `DailyParticipationAgrega`, and `AddrMoniker`, which carry the `proposed`, `proposed_count`, and `voting_power` GORM tags (with `not null default`). GORM AutoMigrate adds missing columns with their defaults, so the three `addColumnIfMissing` blocks never fire.

- Delete the three `addColumnIfMissing(...)` blocks in `InitDB`.
- Delete the `addColumnIfMissing` helper.
- Remove the now-unused `database/sql` import if nothing else uses it.

**Verification (manual, in the plan):** on a schema that predates these columns, run `InitDB` and confirm all three columns exist afterward (AutoMigrate created them). The existing DB-backed tests already exercise a fresh migration path.

### A5 — Generic `numOr` (#7a)

Replace `intOr` and `floatOr` in `score.go` with one generic helper:

```go
func numOr[T any](cfg map[string]string, key string, fallback T, parse func(string) (T, error)) T {
	v, ok := cfg[key]
	if !ok {
		return fallback
	}
	n, err := parse(v)
	if err != nil {
		return fallback
	}
	return n
}
```

`WeightsFromConfig` calls `numOr(cfg, key, w.X, strconv.Atoi)` / `numOr(cfg, key, w.X, func(s string) (float64, error) { return strconv.ParseFloat(s, 64) })`. Identical fallback semantics; `weights_test.go` is unchanged and remains the safety net.

---

## Phase B — Collectors

Files: `backend/internal/gnovalidator/gnovalidator_realtime.go`, `sync.go`, `valoper.go`.

### B1 — De-dupe proposer derivation (#6)

In each of the three collectors (`CollectParticipation`, `BackfillRange`, `BackfillParallel`), the block proposer is currently resolved with `.String()` twice — once into `txProposer`/`txProp` inside the `if hasTx` branch and again into `proposerAddr`. Compute it once and derive the tx-proposer from it:

```go
proposerAddr := block.Block.Header.ProposerAddress.String() // (b.… in BackfillParallel)
txProposer := ""
if len(block.Block.Data.Txs) > 0 { // hasTx
	txProposer = proposerAddr
}
```

`TxContribution` and `Proposed` then read these locals as they do today. No cross-function shared helper — the surrounding variable names/types differ and a one-liner helper adds little. Semantics unchanged.

### B2 — Batch VP upsert call site (#3)

In `InitMonikerMap`, replace the per-validator `for … UpsertAddrMonikerVP` loop with a single collect-then-`UpsertAddrMonikerVPBatch` call. Same parse/skip rules (skip empty or non-integer `voting_power`); one DB round-trip per refresh instead of N. Keep the best-effort error log on batch failure.

---

## Phase C — API

File: `backend/internal/api/api_report.go`.

### C1 — Collapse the merge (#5)

Replace the parallel `inputs map[string]*score.Inputs` + `monikers map[string]string` + `ensure` closure with one map over a small wrapper that carries the moniker alongside the score inputs:

```go
type merged struct {
	in      score.Inputs
	moniker string
}
```

A helper `mergeParticipationAndAlerts(partRows []database.ParticipationRaw, alertRows []database.ValidatorScoreRaw, addrFilter string) map[string]*merged` builds it (moniker still sourced from alert rows; roster still seeds `byAddr` upstream). The per-period loop wires the `chainBlocks` returned by A2 (`GetValidatorParticipation`) and drops the removed `GetChainTotalBlocks` call. Score computation, VP injection, `ProposerScored` handling, and the absent-period fill are unchanged.

**Test:** existing `api_report_test.go` cases (including `…AlertOnlyNoParticipation`) are the safety net; no new assertions needed unless the helper is unit-tested directly.

---

## Explicitly Out of Scope (#7b — indexes)

The `GetValidatorVP` filter (`addr_monikers WHERE chain_id = ? AND voting_power > 0`) hits a table with one row per validator; the `proposed` / `proposed_count` reads ride the existing `(chain_id, date)` / `(chain_id, block_date)` indexes. Adding indexes here is premature. **Decision: no index changes** — recorded here so it's a deliberate call, not an oversight. Revisit only if `addr_monikers` accumulates stale historical rows.

Also unchanged and **accepted** (documented in the score-v2 plan, not defects): the current-VP snapshot applied to `current_year`/`current_month` severity and proposer-expected, and the sub-1h aggregator-lag seam. Not touched by this work.

---

## Sequencing & Verification

1. **Phase A** (DB + score): A1 → A2 → A3 → A4 → A5. Commit per change or per phase.
2. **Phase B** (collectors): B1, B2.
3. **Phase C** (API): C1.
4. **Final gate:** `cd backend && go build ./... && go vet ./... && go test ./...` (needs a reachable Postgres per CLAUDE.md) and `cd panel && npm run build`.

Each phase must leave the full suite green before the next begins, since every change is behavior-preserving.
