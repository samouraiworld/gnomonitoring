# Validator Score v2 — Design Spec

Date: 2026-07-08
Branch: all three phases land on a single feature branch off `main`.

## Goal

Replace the purely punitive validator health score (start at 100, subtract
alert penalties) with a layered model that reflects the *real* quality of a
validator: actual block-signing availability, leader (proposer) reliability,
incident history, and network impact (voting power). No historical RPC is
reparsed — the base reads existing daily aggregates, and the new signals
(proposer, VP) are captured at collection time "au fil de l'eau".

## Current state (what we replace)

- `score.Compute(criticalCount, downtimeBlocks, weights)` — `score = 100 −
  critPenalty − downPenalty`. `WarningCount` is fetched in
  `db_score.GetValidatorScores` but **never used** in the computation.
- Data source is `alert_logs` only. Sub-threshold missed blocks (a validator
  chronically missing 4 blocks, never tripping the ≥5 WARNING threshold) are
  invisible: it scores a perfect 100.
- `daily_participation_agregas` already holds durable per (chain, addr, day)
  rollups: `participated_count`, `missed_count`, `total_blocks`,
  `first/last_block_height`. Raw `daily_participations` rows are pruned after
  ~7 days (retention). VP and proposer are stored nowhere.

## Scoring model v2

Four layers combined into a single 0–100 score per validator per period.

### ① Availability base (0–100)

```
base = 100 × (participated_count / total_blocks)   // over the period
```

Source: `daily_participation_agregas` summed over the period, UNION with
current-day raw `daily_participations` rows not yet aggregated (same pattern
already used by `GetChainValidators`). This is the core fix: chronic
sub-threshold unreliability now lowers the score even with zero alerts.

### ② Proposer reliability (0–100)

```
expected  = vp_share × total_blocks_chain          // vp_share = vp / Σ vp
proposer_reliability = clamp(proposed / expected, 0, 1) × 100
```

Guard: if `expected < ProposerMinExpected` (tiny-VP validators rarely
propose → noisy), the proposer component is dropped (its weight folds into
sign). Tendermint proposer selection is deterministic and VP-proportional, so
proposing materially below one's VP share is a genuine leader-liveness failure.

**Presence score** = weighted blend:

```
presence = w_sign × base + w_prop × proposer_reliability   // default 0.8 / 0.2
```

When the proposer component is dropped, `w_sign = 1` (presence = base).

### ③ Incident penalties (modifier)

The existing alert model becomes a modifier, and `warning_count` (already
collected, currently unused) is finally wired in:

```
critPenalty = min(critical_count × CriticalWeight, CriticalCap)
warnPenalty = min(warning_count  × WarningWeight,  WarningCap)   // NEW
downPenalty = min(downtime_blocks / DowntimeBlocksPerPoint, DowntimeCap)
```

### ④ Voting-power severity (network impact)

```
severity     = 1 + VpSeverityFactor × (vp_share / max_vp_share)   // bounded, e.g. 1 → 1.5
totalPenalty = (critPenalty + warnPenalty + downPenalty) × severity
```

The same incident costs more for a high-VP validator because its failures
weigh more on consensus. Bounded so a whale cannot diverge to −∞.

### Final

```
score = clamp(presence − totalPenalty, 0, 100)
tier  = tierFor(score)      // thresholds unchanged: 85 / 60 / 30
```

### Reliability properties

- **Graceful degradation.** Before the first VP snapshot, `vp = 0` →
  `severity = 1`, proposer dropped → `score = 100×sign_rate −
  alert_penalties`. Nothing breaks during rollout.
- **Backward compatible.** `w_prop = 0`, `VpSeverityFactor = 0`,
  `WarningWeight = 0` reproduces (close to) today's behavior.
- **Pure & testable.** `score.Compute` stays DB-free; its signature takes a
  new `Inputs` struct.
- **Division guards.** `total_blocks = 0`, `Σ vp = 0`, `expected ≈ 0` are all
  guarded (component dropped / neutral).

## Tunable weights (admin_config)

Extends the existing `report_score_*` key pattern. New keys with defaults:

| Key | Default | Meaning |
|---|---|---|
| `report_score_sign_weight` | 0.8 | `w_sign` in presence blend |
| `report_score_proposer_weight` | 0.2 | `w_prop` in presence blend |
| `report_score_proposer_min_expected` | 5 | drop proposer component below this expected count |
| `report_score_warning_weight` | 2 | points per WARNING alert |
| `report_score_warning_cap` | 20 | max points lost from warnings |
| `report_score_vp_severity_factor` | 0.5 | severity ramp (→ ×1.5 for top-VP validator) |

Existing keys retained: `report_score_critical_weight`,
`report_score_critical_cap`, `report_score_downtime_blocks_per_point`,
`report_score_downtime_cap`.

## Data collection changes

### Proposer (new)

- Realtime loop: for each block, mark the row whose `addr` equals
  `block.Block.Header.ProposerAddress` with a new `Proposed bool` column on
  `daily_participations` (mirrors `TxContribution`).
- Aggregator: sum `proposed_count` per day into
  `daily_participation_agregas`.
- Migration: `ALTER TABLE ... ADD COLUMN` (default false / 0), idempotent, in
  the `ApplyMultiChainMigrations` style.

### Voting power (new)

- The 5-minute MonikerMap refresh already queries the validator set /
  valopers. Extend it to capture VP per addr.
- Store latest VP in a new `voting_power int64` column on `addr_monikers`
  (already keyed `chain_id, addr`). Current snapshot only, no history.

No existing data is replayed: the base reads aggregates; proposer/VP are added
at the source going forward.

## Surfaces to update

### API — `internal/api/api_report.go`

`periodScore` gains fields; `Compute` is called with the new `Inputs`:

```go
type periodScore struct {
    Score               int     `json:"score"`
    Tier                string  `json:"tier"`
    SignRate            float64 `json:"sign_rate"`             // NEW 0..100
    ProposerReliability float64 `json:"proposer_reliability"`  // NEW 0..100 (or null when dropped)
    VotingPower         int64   `json:"voting_power"`          // NEW
    CriticalCount       int     `json:"critical_count"`
    WarningCount        int     `json:"warning_count"`
    DowntimeBlocks      int64   `json:"downtime_blocks"`
}
```

### DB — `internal/database/db_score.go`

`GetValidatorScores` (and/or a companion query) enriched to also return, per
validator per period: participation totals (`participated_count`,
`total_blocks`) from the aggregate + current-day raw union, `proposed_count`,
chain `total_blocks_chain`, and `voting_power`. Merge sources by `addr` in Go
for testability. `ValidatorScoreRaw` extended accordingly.

### Panel — `panel/src/pages/Reports.tsx` + `panel/src/types/report.ts`

- `PeriodScore` interface: add `sign_rate`, `proposer_reliability`,
  `voting_power`.
- Reports table: add columns Sign % / Proposer % / VP (sortable, matching the
  existing sort machinery).
- CSV export: add the new fields to headers and rows.

## Phasing (all on one branch, shippable/testable in sequence)

**Phase 1 — Base (fast, no schema change).** Read participation totals from
the aggregate (+ current-day raw union), set `base = 100×sign_rate`, wire in
`warning_count`. Immediately fixes the sub-threshold blind spot.

**Phase 2 — Score refactor.** Restructure `Compute` into the presence/penalty
model; all new weights in admin_config. API + panel surface `sign_rate`.

**Phase 3 — Option (new collection).**
- 3a VP: `voting_power` column on `addr_monikers`, capture in the 5-minute
  refresh, `severity` weighting, surface VP in API + panel.
- 3b Proposer: `proposed` column, realtime marking, aggregate `proposed_count`,
  proposer-reliability component, surface in API + panel.

## Files touched

- `internal/score/score.go` — `Inputs` struct, `Compute` refactor, new weight keys
- `internal/database/db_score.go` — enriched query + `ValidatorScoreRaw`
- `internal/database/db_init.go` — 2 columns + migrations
- `internal/gnovalidator/sync.go` / `gnovalidator_realtime.go` — proposer marking
- `internal/gnovalidator/aggregator.go` — `proposed_count` in rollup
- `internal/gnovalidator/valoper.go` — VP capture in moniker refresh
- `internal/api/api_report.go` — period assembly + response shape
- `panel/src/pages/Reports.tsx`, `panel/src/types/report.ts` — columns + CSV
- Tests: `score_test.go`, `weights_test.go`, `db_score` (NewTestDB),
  `aggregator_test.go`, `gnovalidator_realtime_test.go`

## Testing strategy

- **score (pure unit):** flaky-no-alerts (base < 100), whale-downtime
  (severity ramps penalty), tiny-VP (proposer dropped), all-perfect (100),
  backward-compat defaults. Extend `score_test.go` / `weights_test.go`.
- **db_score (integration, NewTestDB):** seed `daily_participation_agregas` +
  current-day raw + `alert_logs` + `addr_monikers.voting_power`; assert merged
  raw metrics and current-day union.
- **aggregator:** `proposed_count` summed correctly.
- **realtime:** exactly one row per block marked `Proposed = true`.

## Out of scope / accepted limitations

- VP is a current snapshot: the `current_year` period applies today's VP to
  older incidents where VP may have differed. Accepted for simplicity.
- Recency weighting inside a period is a flat average; period granularity
  (24h/week/month/year) already provides coarse recency.
- No historical VP table, no proposer expected-value recomputation over past
  VP.
