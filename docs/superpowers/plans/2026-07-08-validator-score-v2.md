# Validator Score v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the alert-only validator health score with a layered model — availability base (sign%), proposer reliability, incident penalties, and voting-power severity — surfaced through the API and the panel Reports view.

**Architecture:** Keep `score.Compute` a pure, DB-free function taking a new `Inputs` struct. The base sign% is read from the durable `daily_participation_agregas` rollup (partitioned against current-day raw rows to avoid double counting). Voting power and proposer counts are captured at collection time ("au fil de l'eau") — VP in the existing 5-minute `InitMonikerMap` refresh, proposer in the realtime block loop — never by reparsing historical RPC.

**Tech Stack:** Go 1.x, GORM + raw SQL over PostgreSQL, Fiber-style `net/http` handlers, React + TypeScript panel.

## Global Constraints

- All code comments, commit messages, and docs in **English** only.
- Every query against `daily_participations`, `daily_participation_agregas`, `alert_logs`, `addr_monikers` MUST include `WHERE chain_id = ?`.
- `score` package stays DB-free (pure), unit-testable in isolation.
- DB tests use `testoutils.NewTestDB(t)` (isolated Postgres schema per test).
- Migrations idempotent, in the `ApplyMultiChainMigrations` style (check `information_schema.columns` before `ALTER`).
- Tier thresholds unchanged: Excellent ≥85, Good ≥60, Watch ≥30, Critical <30.
- All new scoring weights read from `admin_config` via `WeightsFromConfig`, defaulted in code (no central seeding needed — missing keys fall back to defaults).
- Branch: `feat/validator-score-v2` (already created from `main`). All three phases land here. Commit after every task.

---

## File Structure

- `backend/internal/score/score.go` — `Inputs`, `Result`, extended `Weights`, refactored `Compute`, new config keys/helpers.
- `backend/internal/score/score_test.go`, `weights_test.go` — pure unit tests.
- `backend/internal/database/db_score.go` — `ParticipationRaw`, `GetValidatorParticipation`, `GetValidatorVP`, `GetChainTotalBlocks`; extended `ValidatorScoreRaw` unchanged (alerts stay separate).
- `backend/internal/database/db_init.go` — `voting_power` on `AddrMoniker`, `proposed` on `DailyParticipation`, `proposed_count` on `DailyParticipationAgrega`, migrations.
- `backend/internal/database/db.go` — `UpsertAddrMonikerVP`.
- `backend/internal/gnovalidator/valoper.go` — capture `voting_power` from `/validators`, persist.
- `backend/internal/gnovalidator/sync.go` — `proposed` in `dpRow` + flush.
- `backend/internal/gnovalidator/gnovalidator_realtime.go` — `proposed` in realtime row build + insert.
- `backend/internal/gnovalidator/aggregator.go` — `proposed_count` in rollup SQL.
- `backend/internal/api/api_report.go` — assemble `Inputs`, extended `periodScore`.
- `panel/src/types/report.ts`, `panel/src/pages/Reports.tsx` — new columns + CSV.

---

# PHASE 1 — Availability base (sign%)

No schema change. Introduces the full `Compute` interface (proposer/VP fields present but neutral when zero) and wires the base + the currently-unused `warning_count`.

## Task 1: Score package — layered `Compute`

**Files:**
- Modify: `backend/internal/score/score.go`
- Test: `backend/internal/score/score_test.go`, `backend/internal/score/weights_test.go`

**Interfaces:**
- Produces:
  - `type Inputs struct { SignedBlocks, TotalBlocks, ProposedBlocks, ChainBlocks, VotingPower, MaxVotingPower, SumVotingPower, DowntimeBlocks int64; CriticalCount, WarningCount int }`
  - `type Result struct { Score int; Tier Tier; SignRate float64; ProposerReliability float64; ProposerScored bool }`
  - `func Compute(in Inputs, w Weights) Result`
  - `Weights` extended: `WarningWeight, WarningCap, ProposerMinExpected int; SignWeight, ProposerWeight, VpSeverityFactor float64`
  - New config keys: `KeyWarningWeight`, `KeyWarningCap`, `KeySignWeight`, `KeyProposerWeight`, `KeyProposerMinExpected`, `KeyVpSeverityFactor`

- [ ] **Step 1: Write failing tests**

Replace the body of `score_test.go` scenarios (keep existing package + imports) and add these cases:

```go
func TestCompute_FlakyNoAlerts_BaseBelow100(t *testing.T) {
	// 90/100 signed, no alerts, no VP/proposer → base 90, no penalty.
	r := Compute(Inputs{SignedBlocks: 90, TotalBlocks: 100}, DefaultWeights())
	if r.Score != 90 {
		t.Fatalf("score = %d, want 90", r.Score)
	}
	if r.SignRate != 90 {
		t.Fatalf("signRate = %v, want 90", r.SignRate)
	}
	if r.ProposerScored {
		t.Fatalf("proposer should be dropped when no VP data")
	}
	if r.Tier != TierExcellent {
		t.Fatalf("tier = %s, want Excellent", r.Tier)
	}
}

func TestCompute_PerfectNoData(t *testing.T) {
	// No participation data at all → sign rate 0 guarded to base 0.
	r := Compute(Inputs{SignedBlocks: 0, TotalBlocks: 0}, DefaultWeights())
	if r.Score != 0 {
		t.Fatalf("score = %d, want 0 (no blocks → base 0)", r.Score)
	}
}

func TestCompute_WarningPenaltyApplied(t *testing.T) {
	// 100/100 signed, 3 warnings @ weight 2 = 6 penalty.
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, WarningCount: 3}, DefaultWeights())
	if r.Score != 94 {
		t.Fatalf("score = %d, want 94", r.Score)
	}
}

func TestCompute_CriticalAndDowntime(t *testing.T) {
	// 100/100 signed, 2 criticals (@6 = 12), 1000 downtime (@500/pt = 2).
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, CriticalCount: 2, DowntimeBlocks: 1000}, DefaultWeights())
	if r.Score != 86 {
		t.Fatalf("score = %d, want 86", r.Score)
	}
}

func TestCompute_VpSeverityRampsPenalty(t *testing.T) {
	// 100/100 signed, 5 criticals (@6=30 capped at 60 → 30), VP == max so severity = 1+0.5 = 1.5.
	// totalPen = 30 * 1.5 = 45 → score 55.
	r := Compute(Inputs{
		SignedBlocks: 100, TotalBlocks: 100, CriticalCount: 5,
		VotingPower: 1000, MaxVotingPower: 1000, SumVotingPower: 4000,
	}, DefaultWeights())
	if r.Score != 55 {
		t.Fatalf("score = %d, want 55", r.Score)
	}
}

func TestCompute_ProposerBlendWhenExpectedMet(t *testing.T) {
	// vpShare = 1000/4000 = 0.25; chainBlocks 1000 → expected 250 (>= min 5).
	// proposed 125 → ratio 0.5 → propRel 50. base = 100.
	// presence = (0.8*100 + 0.2*50)/1.0 = 90. No alerts. severity: VP==max → 1.5 but no penalty.
	r := Compute(Inputs{
		SignedBlocks: 100, TotalBlocks: 100,
		ProposedBlocks: 125, ChainBlocks: 1000,
		VotingPower: 1000, MaxVotingPower: 1000, SumVotingPower: 4000,
	}, DefaultWeights())
	if !r.ProposerScored {
		t.Fatalf("proposer should be scored when expected >= min")
	}
	if r.ProposerReliability != 50 {
		t.Fatalf("propRel = %v, want 50", r.ProposerReliability)
	}
	if r.Score != 90 {
		t.Fatalf("score = %d, want 90", r.Score)
	}
}

func TestCompute_ProposerDroppedWhenExpectedBelowMin(t *testing.T) {
	// Tiny VP: vpShare 1/4000, chainBlocks 1000 → expected 0.25 < min 5 → dropped.
	r := Compute(Inputs{
		SignedBlocks: 100, TotalBlocks: 100,
		ProposedBlocks: 0, ChainBlocks: 1000,
		VotingPower: 1, MaxVotingPower: 1000, SumVotingPower: 4000,
	}, DefaultWeights())
	if r.ProposerScored {
		t.Fatalf("proposer should be dropped for tiny VP")
	}
	if r.Score != 100 {
		t.Fatalf("score = %d, want 100 (presence == base)", r.Score)
	}
}
```

Add to `weights_test.go`:

```go
func TestWeightsFromConfig_NewKeys(t *testing.T) {
	cfg := map[string]string{
		KeyWarningWeight:       "3",
		KeyWarningCap:          "15",
		KeySignWeight:          "0.7",
		KeyProposerWeight:      "0.3",
		KeyProposerMinExpected: "10",
		KeyVpSeverityFactor:    "0.25",
	}
	w := WeightsFromConfig(cfg)
	if w.WarningWeight != 3 || w.WarningCap != 15 || w.ProposerMinExpected != 10 {
		t.Fatalf("int keys not parsed: %+v", w)
	}
	if w.SignWeight != 0.7 || w.ProposerWeight != 0.3 || w.VpSeverityFactor != 0.25 {
		t.Fatalf("float keys not parsed: %+v", w)
	}
}

func TestWeightsFromConfig_DefaultsWhenMissing(t *testing.T) {
	w := WeightsFromConfig(map[string]string{})
	d := DefaultWeights()
	if w != d {
		t.Fatalf("empty config should equal DefaultWeights: got %+v want %+v", w, d)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/score/... -run TestCompute -v`
Expected: FAIL — `Compute` signature/`Inputs`/`Result` undefined (compile error).

- [ ] **Step 3: Rewrite `score.go`**

Replace `Weights`, `DefaultWeights`, `Compute`, keys, and `WeightsFromConfig` with:

```go
package score

import (
	"math"
	"strconv"
)

type Tier string

const (
	TierExcellent Tier = "Excellent"
	TierGood      Tier = "Good"
	TierWatch     Tier = "Watch"
	TierCritical  Tier = "Critical"
)

// Weights holds the tunable scoring parameters (loaded from admin_config in
// production, defaulted here).
type Weights struct {
	CriticalWeight         int
	CriticalCap            int
	DowntimeBlocksPerPoint int
	DowntimeCap            int
	WarningWeight          int
	WarningCap             int
	ProposerMinExpected    int
	SignWeight             float64
	ProposerWeight         float64
	VpSeverityFactor       float64
}

func DefaultWeights() Weights {
	return Weights{
		CriticalWeight:         6,
		CriticalCap:            60,
		DowntimeBlocksPerPoint: 500,
		DowntimeCap:            20,
		WarningWeight:          2,
		WarningCap:             20,
		ProposerMinExpected:    5,
		SignWeight:             0.8,
		ProposerWeight:         0.2,
		VpSeverityFactor:       0.5,
	}
}

// Inputs carries one validator's raw metrics for one period. All fields are
// non-negative. Proposer/VP fields are 0 until those collectors are deployed,
// in which case their components degrade to neutral (proposer dropped,
// severity = 1).
type Inputs struct {
	SignedBlocks   int64 // participated_count over the period
	TotalBlocks    int64 // total blocks this validator was expected to sign
	ProposedBlocks int64 // proposed_count over the period
	ChainBlocks    int64 // total blocks on the chain in the period
	VotingPower    int64 // current voting power
	MaxVotingPower int64 // max VP across the chain (severity normalization)
	SumVotingPower int64 // sum of VP across the chain (vp share)
	DowntimeBlocks int64
	CriticalCount  int
	WarningCount   int
}

// Result is the computed score plus the surfaced sub-metrics.
type Result struct {
	Score               int
	Tier                Tier
	SignRate            float64 // 0..100
	ProposerReliability float64 // 0..100 (meaningful only when ProposerScored)
	ProposerScored      bool
}

func Compute(in Inputs, w Weights) Result {
	signRate := 0.0
	if in.TotalBlocks > 0 {
		signRate = float64(in.SignedBlocks) / float64(in.TotalBlocks) * 100
	}
	base := signRate

	// Proposer reliability, dropped when the validator's expected proposal
	// count is too small to be a reliable signal.
	propScored := false
	propRel := 0.0
	if in.SumVotingPower > 0 && in.ChainBlocks > 0 && in.VotingPower > 0 {
		vpShare := float64(in.VotingPower) / float64(in.SumVotingPower)
		expected := vpShare * float64(in.ChainBlocks)
		if expected >= float64(w.ProposerMinExpected) {
			ratio := float64(in.ProposedBlocks) / expected
			if ratio > 1 {
				ratio = 1
			}
			if ratio < 0 {
				ratio = 0
			}
			propRel = ratio * 100
			propScored = true
		}
	}

	presence := base
	if propScored && (w.SignWeight+w.ProposerWeight) > 0 {
		presence = (w.SignWeight*base + w.ProposerWeight*propRel) / (w.SignWeight + w.ProposerWeight)
	}

	critPenalty := in.CriticalCount * w.CriticalWeight
	if critPenalty > w.CriticalCap {
		critPenalty = w.CriticalCap
	}
	warnPenalty := in.WarningCount * w.WarningWeight
	if warnPenalty > w.WarningCap {
		warnPenalty = w.WarningCap
	}
	downPenalty := 0
	if w.DowntimeBlocksPerPoint > 0 {
		downPenalty = int(in.DowntimeBlocks / int64(w.DowntimeBlocksPerPoint))
	}
	if downPenalty > w.DowntimeCap {
		downPenalty = w.DowntimeCap
	}

	severity := 1.0
	if in.MaxVotingPower > 0 && in.VotingPower > 0 {
		severity = 1 + w.VpSeverityFactor*(float64(in.VotingPower)/float64(in.MaxVotingPower))
	}
	totalPenalty := float64(critPenalty+warnPenalty+downPenalty) * severity

	s := int(math.Round(presence - totalPenalty))
	if s < 0 {
		s = 0
	}
	if s > 100 {
		s = 100
	}
	return Result{
		Score:               s,
		Tier:                tierFor(s),
		SignRate:            signRate,
		ProposerReliability: propRel,
		ProposerScored:      propScored,
	}
}

func tierFor(s int) Tier {
	switch {
	case s >= 85:
		return TierExcellent
	case s >= 60:
		return TierGood
	case s >= 30:
		return TierWatch
	default:
		return TierCritical
	}
}

const (
	KeyCriticalWeight         = "report_score_critical_weight"
	KeyCriticalCap            = "report_score_critical_cap"
	KeyDowntimeBlocksPerPoint = "report_score_downtime_blocks_per_point"
	KeyDowntimeCap            = "report_score_downtime_cap"
	KeyWarningWeight          = "report_score_warning_weight"
	KeyWarningCap             = "report_score_warning_cap"
	KeyProposerMinExpected    = "report_score_proposer_min_expected"
	KeySignWeight             = "report_score_sign_weight"
	KeyProposerWeight         = "report_score_proposer_weight"
	KeyVpSeverityFactor       = "report_score_vp_severity_factor"
)

func WeightsFromConfig(cfg map[string]string) Weights {
	w := DefaultWeights()
	w.CriticalWeight = intOr(cfg, KeyCriticalWeight, w.CriticalWeight)
	w.CriticalCap = intOr(cfg, KeyCriticalCap, w.CriticalCap)
	w.DowntimeBlocksPerPoint = intOr(cfg, KeyDowntimeBlocksPerPoint, w.DowntimeBlocksPerPoint)
	w.DowntimeCap = intOr(cfg, KeyDowntimeCap, w.DowntimeCap)
	w.WarningWeight = intOr(cfg, KeyWarningWeight, w.WarningWeight)
	w.WarningCap = intOr(cfg, KeyWarningCap, w.WarningCap)
	w.ProposerMinExpected = intOr(cfg, KeyProposerMinExpected, w.ProposerMinExpected)
	w.SignWeight = floatOr(cfg, KeySignWeight, w.SignWeight)
	w.ProposerWeight = floatOr(cfg, KeyProposerWeight, w.ProposerWeight)
	w.VpSeverityFactor = floatOr(cfg, KeyVpSeverityFactor, w.VpSeverityFactor)
	return w
}

func intOr(cfg map[string]string, key string, fallback int) int {
	v, ok := cfg[key]
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func floatOr(cfg map[string]string, key string, fallback float64) float64 {
	v, ok := cfg[key]
	if !ok {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/score/... -v`
Expected: PASS (all `TestCompute_*` and `TestWeightsFromConfig_*`).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/score/
git commit -m "feat(score): layered Compute with sign base, proposer, warning, VP severity"
```

## Task 2: DB — per-validator participation totals

**Files:**
- Modify: `backend/internal/database/db_score.go`
- Test: `backend/internal/database/db_score_test.go` (create if absent)

**Interfaces:**
- Consumes: `periodBounds(period, now)` (existing in db_score.go).
- Produces:
  - `type ParticipationRaw struct { Addr string; SignedBlocks int64; TotalBlocks int64; ProposedBlocks int64 }`
  - `func GetValidatorParticipation(db *gorm.DB, chainID, period string) ([]ParticipationRaw, error)`

Partition rule (avoids double counting): the aggregator rolls up complete days
`< today UTC` into `daily_participation_agregas`; today's rows live only in raw
`daily_participations`. So sum agrega for `block_date < todayUTC`, raw for
`date >= todayUTC 00:00`. For `last_24h`, read entirely from raw (raw retains
~7 days, so it covers the trailing window at block granularity) and skip agrega.

- [ ] **Step 1: Write failing test**

```go
package database

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestGetValidatorParticipation_UnionAgregaAndToday(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"

	// Aggregated complete day (yesterday): 80 signed / 100 total for addrA.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if err := db.Exec(`INSERT INTO daily_participation_agregas
		(chain_id, addr, block_date, moniker, participated_count, missed_count,
		 tx_contribution_count, total_blocks, first_block_height, last_block_height)
		VALUES (?, 'addrA', ?, 'A', 80, 20, 0, 100, 1, 100)`,
		chain, yesterday).Error; err != nil {
		t.Fatal(err)
	}

	// Today's raw rows for addrA: 2 blocks, 1 signed.
	now := time.Now().UTC()
	today0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 5, 0, 0, time.UTC)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution)
		VALUES (?, ?, 201, 'A', 'addrA', true, false),
		       (?, ?, 202, 'A', 'addrA', false, false)`,
		chain, today0, chain, today0).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := GetValidatorParticipation(db, chain, "current_month")
	if err != nil {
		t.Fatal(err)
	}
	var got *ParticipationRaw
	for i := range rows {
		if rows[i].Addr == "addrA" {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("addrA missing from results")
	}
	// 80 + 1 signed, 100 + 2 total.
	if got.SignedBlocks != 81 || got.TotalBlocks != 102 {
		t.Fatalf("signed/total = %d/%d, want 81/102", got.SignedBlocks, got.TotalBlocks)
	}
}

func TestGetValidatorParticipation_Last24hRawOnly(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution)
		VALUES (?, ?, 301, 'B', 'addrB', true, false),
		       (?, ?, 302, 'B', 'addrB', true, false),
		       (?, ?, 303, 'B', 'addrB', false, false)`,
		chain, recent, chain, recent, chain, recent).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := GetValidatorParticipation(db, chain, "last_24h")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SignedBlocks != 2 || rows[0].TotalBlocks != 3 {
		t.Fatalf("got %+v, want one row 2/3", rows)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/database/... -run TestGetValidatorParticipation -v`
Expected: FAIL — `GetValidatorParticipation`/`ParticipationRaw` undefined.

- [ ] **Step 3: Implement `GetValidatorParticipation`**

Add to `db_score.go` (note: `proposed_count` referenced here is added in Phase 3; until the column exists this query would error, so Phase 1 selects a literal `0`. Phase 3 Task 12 swaps the literal for the real column — see that task):

```go
// ParticipationRaw holds summed signing (and proposer) activity for one
// validator over one period. Scoped to chain_id.
type ParticipationRaw struct {
	Addr           string `json:"addr"`
	SignedBlocks   int64  `json:"signed_blocks"`
	TotalBlocks    int64  `json:"total_blocks"`
	ProposedBlocks int64  `json:"proposed_blocks"`
}

// GetValidatorParticipation returns per-validator signed/total (and proposed)
// block counts for the period. It reads durable daily aggregates for complete
// past days and raw rows for the current day, partitioned at today 00:00 UTC to
// avoid double counting. last_24h reads only raw rows (block granularity).
func GetValidatorParticipation(db *gorm.DB, chainID, period string) ([]ParticipationRaw, error) {
	start, end, err := periodBounds(period, time.Now())
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Raw window: for last_24h use the whole period; otherwise only today.
	rawStart := todayStart
	if period == "last_24h" {
		rawStart = start
	}
	if rawStart.Before(start) {
		rawStart = start
	}

	// Aggregate window (day strings, exclusive upper bound at today).
	agregaStart := start.Format("2006-01-02")
	agregaEnd := todayStart.Format("2006-01-02") // exclusive: block_date < today
	includeAgrega := period != "last_24h" && todayStart.After(start)

	var rows []ParticipationRaw
	q := `
		SELECT combined.addr AS addr,
		       SUM(combined.signed) AS signed_blocks,
		       SUM(combined.total)  AS total_blocks,
		       SUM(combined.proposed) AS proposed_blocks
		FROM (
			SELECT addr,
			       CASE WHEN participated THEN 1 ELSE 0 END AS signed,
			       1 AS total,
			       0 AS proposed
			FROM daily_participations
			WHERE chain_id = ? AND date >= ? AND date < ?
	`
	args := []any{chainID, rawStart, end}
	if includeAgrega {
		q += `
			UNION ALL
			SELECT addr,
			       participated_count AS signed,
			       total_blocks       AS total,
			       0                  AS proposed
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ? AND block_date < ?
		`
		args = append(args, chainID, agregaStart, agregaEnd)
	}
	q += `
		) combined
		GROUP BY combined.addr
		ORDER BY combined.addr
	`
	if err := db.Raw(q, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("GetValidatorParticipation(%s,%s): %w", chainID, period, err)
	}
	return rows, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/database/... -run TestGetValidatorParticipation -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/database/db_score.go backend/internal/database/db_score_test.go
git commit -m "feat(db): GetValidatorParticipation summing agrega + current-day raw"
```

## Task 3: API — assemble Inputs, surface sign%

**Files:**
- Modify: `backend/internal/api/api_report.go`

**Interfaces:**
- Consumes: `score.Compute`, `score.Inputs`, `database.GetValidatorParticipation`, existing `database.GetValidatorScores`.

- [ ] **Step 1: Extend `periodScore` and merge participation**

Replace the `periodScore` struct:

```go
type periodScore struct {
	Score               int      `json:"score"`
	Tier                string   `json:"tier"`
	SignRate            float64  `json:"sign_rate"`
	ProposerReliability *float64 `json:"proposer_reliability"`
	VotingPower         int64    `json:"voting_power"`
	CriticalCount       int      `json:"critical_count"`
	WarningCount        int      `json:"warning_count"`
	DowntimeBlocks      int64    `json:"downtime_blocks"`
}
```

Replace the per-period loop body (lines ~79-105) so it merges alert rows with participation rows by addr:

```go
	for _, period := range reportPeriods {
		alertRows, err := database.GetValidatorScores(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		partRows, err := database.GetValidatorParticipation(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Merge both sources into one Inputs per addr.
		inputs := map[string]*score.Inputs{}
		monikers := map[string]string{}
		ensure := func(addr string) *score.Inputs {
			in, ok := inputs[addr]
			if !ok {
				in = &score.Inputs{}
				inputs[addr] = in
			}
			return in
		}
		for _, p := range partRows {
			if addrFilter != "" && p.Addr != addrFilter {
				continue
			}
			in := ensure(p.Addr)
			in.SignedBlocks = p.SignedBlocks
			in.TotalBlocks = p.TotalBlocks
			in.ProposedBlocks = p.ProposedBlocks
		}
		for _, a := range alertRows {
			if addrFilter != "" && a.Addr != addrFilter {
				continue
			}
			in := ensure(a.Addr)
			in.CriticalCount = a.CriticalCount
			in.WarningCount = a.WarningCount
			in.DowntimeBlocks = a.DowntimeBlocks
			if a.Moniker != "" {
				monikers[a.Addr] = a.Moniker
			}
		}

		for addr, in := range inputs {
			rep, ok := byAddr[addr]
			if !ok {
				rep = &validatorReport{Addr: addr, Moniker: monikers[addr], Periods: map[string]periodScore{}}
				byAddr[addr] = rep
				order = append(order, addr)
			}
			if rep.Moniker == "" {
				rep.Moniker = monikers[addr]
			}
			res := score.Compute(*in, weights)
			ps := periodScore{
				Score: res.Score, Tier: string(res.Tier),
				SignRate:       res.SignRate,
				VotingPower:    in.VotingPower,
				CriticalCount:  in.CriticalCount,
				WarningCount:   in.WarningCount,
				DowntimeBlocks: in.DowntimeBlocks,
			}
			if res.ProposerScored {
				pr := res.ProposerReliability
				ps.ProposerReliability = &pr
			}
			rep.Periods[period] = ps
		}
	}
```

Update the absent-period fill (lines ~110-116) to use the new signature:

```go
		for _, period := range reportPeriods {
			if _, ok := rep.Periods[period]; !ok {
				res := score.Compute(score.Inputs{}, weights)
				rep.Periods[period] = periodScore{Score: res.Score, Tier: string(res.Tier)}
			}
		}
```

- [ ] **Step 2: Build and vet**

Run: `cd backend && go build ./... && go vet ./internal/api/...`
Expected: no errors.

- [ ] **Step 3: Run the API package tests**

Run: `cd backend && go test ./internal/api/... -v`
Expected: PASS (existing report tests still green; a validator with only participation and no alerts now yields sign-rate-based score).

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/api_report.go
git commit -m "feat(api): score from sign base + alerts, surface sign_rate"
```

## Task 4: Panel — Sign% column

**Files:**
- Modify: `panel/src/types/report.ts`, `panel/src/pages/Reports.tsx`

- [ ] **Step 1: Extend the TS interface**

In `report.ts`, replace `PeriodScore`:

```ts
export interface PeriodScore {
  score: number
  tier: 'Excellent' | 'Good' | 'Watch' | 'Critical'
  sign_rate: number
  proposer_reliability: number | null
  voting_power: number
  critical_count: number
  warning_count: number
  downtime_blocks: number
}
```

- [ ] **Step 2: Add the Sign% column to the table**

In `Reports.tsx`, add a sortable header after the Tier `<th>` (line ~240):

```tsx
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('sign')}>Sign %{sortIndicator('sign')}</th>
```

Add the cell after the Tier `<td>` (line ~256):

```tsx
                    <td>{p ? `${p.sign_rate.toFixed(1)}%` : '—'}</td>
```

Add a sort case alongside the others (near line ~176):

```tsx
      case 'sign':
        cmp = pa.sign_rate - pb.sign_rate
        break
```

Update the empty-state `colSpan` (line ~247) from `7` to `8`.

Add to CSV headers/rows (lines ~93 and ~100):

```tsx
      headers.push(`${per}_score`, `${per}_tier`, `${per}_sign`, `${per}_critical`, `${per}_warning`, `${per}_downtime`)
```
```tsx
          cells.push(String(p.score), p.tier, p.sign_rate.toFixed(1), String(p.critical_count), String(p.warning_count), String(p.downtime_blocks))
```

- [ ] **Step 3: Typecheck / build the panel**

Run: `cd panel && npm run build`
Expected: build succeeds, no TS errors.

- [ ] **Step 4: Commit**

```bash
git add panel/src/types/report.ts panel/src/pages/Reports.tsx
git commit -m "feat(panel): show sign% column in validator report"
```

---

# PHASE 2 — Voting power severity

Captures current VP in the 5-minute `InitMonikerMap` refresh, persists it on `addr_monikers`, and wires it into `Compute` severity. Surfaces VP in API + panel.

## Task 5: DB — `voting_power` column + upsert

**Files:**
- Modify: `backend/internal/database/db_init.go`, `backend/internal/database/db.go`
- Test: `backend/internal/database/db_vp_test.go` (create)

**Interfaces:**
- Produces: `func UpsertAddrMonikerVP(db *gorm.DB, chainID, addr string, votingPower int64) error`

- [ ] **Step 1: Add the column to the model + migration**

In `db_init.go`, add the field to `AddrMoniker`:

```go
	VotingPower      int64  `gorm:"column:voting_power;not null;default:0"                                 json:"voting_power"`
```

In `ApplyMultiChainMigrations` (append to the `alterations` slice), add an idempotent guard. Since that function early-returns when `chain_id` already exists, add a separate idempotent block instead — a standalone `AddColumnIfMissing` call in the existing migration path. Locate where migrations run after `ApplyMultiChainMigrations` in `db_init.go` and add:

```go
	// Idempotent: voting_power on addr_monikers (score v2).
	if err := addColumnIfMissing(sqlDB, "addr_monikers", "voting_power",
		"ALTER TABLE addr_monikers ADD COLUMN voting_power BIGINT NOT NULL DEFAULT 0"); err != nil {
		return err
	}
```

Add the helper near the other migration helpers in `db_init.go`:

```go
// addColumnIfMissing runs alterSQL only when table.column does not yet exist.
func addColumnIfMissing(sqlDB *sql.DB, table, column, alterSQL string) error {
	var count int
	row := sqlDB.QueryRow(`
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = ? AND column_name = ?`, table, column)
	if err := row.Scan(&count); err != nil {
		return fmt.Errorf("addColumnIfMissing(%s.%s): check: %w", table, column, err)
	}
	if count > 0 {
		return nil
	}
	if _, err := sqlDB.Exec(alterSQL); err != nil {
		return fmt.Errorf("addColumnIfMissing(%s.%s): alter: %w", table, column, err)
	}
	return nil
}
```

(Ensure `database/sql` is imported in db_init.go — it already uses `sqlDB`.)

- [ ] **Step 2: Write the upsert test**

```go
package database

import (
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestUpsertAddrMonikerVP(t *testing.T) {
	db := testoutils.NewTestDB(t)
	if err := UpsertAddrMoniker(db, "test13", "addrX", "X"); err != nil {
		t.Fatal(err)
	}
	if err := UpsertAddrMonikerVP(db, "test13", "addrX", 500); err != nil {
		t.Fatal(err)
	}
	var vp int64
	if err := db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "addrX").Scan(&vp).Error; err != nil {
		t.Fatal(err)
	}
	if vp != 500 {
		t.Fatalf("voting_power = %d, want 500", vp)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test ./internal/database/... -run TestUpsertAddrMonikerVP -v`
Expected: FAIL — `UpsertAddrMonikerVP` undefined.

- [ ] **Step 4: Implement the upsert**

In `db.go`, next to `UpsertAddrMoniker`:

```go
// UpsertAddrMonikerVP writes the latest voting power for a validator, inserting
// a row (with empty moniker) if none exists yet. Scoped to chain_id.
func UpsertAddrMonikerVP(db *gorm.DB, chainID, addr string, votingPower int64) error {
	return db.Exec(`
		INSERT INTO addr_monikers (chain_id, addr, moniker, voting_power)
		VALUES (?, ?, '', ?)
		ON CONFLICT(chain_id, addr) DO UPDATE SET voting_power = excluded.voting_power
	`, chainID, addr, votingPower).Error
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test ./internal/database/... -run TestUpsertAddrMonikerVP -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/database/db_init.go backend/internal/database/db.go backend/internal/database/db_vp_test.go
git commit -m "feat(db): voting_power column on addr_monikers + upsert"
```

## Task 6: Capture VP in the moniker refresh

**Files:**
- Modify: `backend/internal/gnovalidator/valoper.go`

- [ ] **Step 1: Decode voting_power from /validators and persist it**

In `InitMonikerMap`, extend the local `Validator` struct (line ~331) to decode voting power:

```go
	type Validator struct {
		Address     string `json:"address"`
		VotingPower string `json:"voting_power"`
	}
```

After the `for addr, moniker := range tempMonikers { SetMoniker(...) }` loop (line ~419-421), persist VP:

```go
	// Persist current voting power for score severity weighting (best-effort).
	for _, val := range validatorsResp.Result.Validators {
		if val.VotingPower == "" {
			continue
		}
		vp, err := strconv.ParseInt(val.VotingPower, 10, 64)
		if err != nil {
			continue
		}
		if err := database.UpsertAddrMonikerVP(db, chainID, val.Address, vp); err != nil {
			log.Printf("[valoper][%s] failed to persist voting power for %s: %v", chainID, val.Address, err)
		}
	}
```

Ensure `strconv` is imported in valoper.go.

- [ ] **Step 2: Build and vet**

Run: `cd backend && go build ./... && go vet ./internal/gnovalidator/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/gnovalidator/valoper.go
git commit -m "feat(valoper): capture and persist validator voting power each refresh"
```

## Task 7: DB VP fetch + API severity wiring

**Files:**
- Modify: `backend/internal/database/db_score.go`, `backend/internal/api/api_report.go`
- Test: `backend/internal/database/db_score_test.go`

**Interfaces:**
- Produces:
  - `type ValidatorVP struct { Addr string; VotingPower int64 }`
  - `func GetValidatorVP(db *gorm.DB, chainID string) (perAddr map[string]int64, sum int64, max int64, err error)`

- [ ] **Step 1: Write failing test**

```go
func TestGetValidatorVP(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"
	for _, r := range []struct {
		addr string
		vp   int64
	}{{"a", 100}, {"b", 300}, {"c", 0}} {
		if err := UpsertAddrMoniker(db, chain, r.addr, r.addr); err != nil {
			t.Fatal(err)
		}
		if err := UpsertAddrMonikerVP(db, chain, r.addr, r.vp); err != nil {
			t.Fatal(err)
		}
	}
	perAddr, sum, max, err := GetValidatorVP(db, chain)
	if err != nil {
		t.Fatal(err)
	}
	if perAddr["b"] != 300 || sum != 400 || max != 300 {
		t.Fatalf("perAddr=%v sum=%d max=%d, want b=300 sum=400 max=300", perAddr, sum, max)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/database/... -run TestGetValidatorVP -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `GetValidatorVP`**

Add to `db_score.go`:

```go
// GetValidatorVP returns the current voting power per validator plus the sum
// and max across the chain, used for score severity and proposer-share. Scoped
// to chain_id.
func GetValidatorVP(db *gorm.DB, chainID string) (map[string]int64, int64, int64, error) {
	type row struct {
		Addr        string
		VotingPower int64
	}
	var rows []row
	if err := db.Raw(`
		SELECT addr, voting_power FROM addr_monikers
		WHERE chain_id = ? AND voting_power > 0
	`, chainID).Scan(&rows).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("GetValidatorVP(%s): %w", chainID, err)
	}
	perAddr := make(map[string]int64, len(rows))
	var sum, max int64
	for _, r := range rows {
		perAddr[r.Addr] = r.VotingPower
		sum += r.VotingPower
		if r.VotingPower > max {
			max = r.VotingPower
		}
	}
	return perAddr, sum, max, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/database/... -run TestGetValidatorVP -v`
Expected: PASS.

- [ ] **Step 5: Wire VP into the API handler**

In `api_report.go`, after computing `weights` (line ~56), load VP once:

```go
	vpByAddr, vpSum, vpMax, err := database.GetValidatorVP(db, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
```

Inside the per-period `for addr, in := range inputs` loop, before `score.Compute`, set the VP fields:

```go
			in.VotingPower = vpByAddr[addr]
			in.SumVotingPower = vpSum
			in.MaxVotingPower = vpMax
```

(`in.VotingPower` is then already copied into `ps.VotingPower` by the existing line.)

- [ ] **Step 6: Build, vet, test**

Run: `cd backend && go build ./... && go test ./internal/api/... -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/database/db_score.go backend/internal/database/db_score_test.go backend/internal/api/api_report.go
git commit -m "feat(score): apply voting-power severity to report scores"
```

## Task 8: Panel — VP column

**Files:**
- Modify: `panel/src/pages/Reports.tsx`

- [ ] **Step 1: Add the VP column**

Header after Sign% (`sign`):

```tsx
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('vp')}>VP{sortIndicator('vp')}</th>
```

Cell after the sign-rate cell:

```tsx
                    <td>{p ? p.voting_power.toLocaleString() : '—'}</td>
```

Sort case:

```tsx
      case 'vp':
        cmp = pa.voting_power - pb.voting_power
        break
```

Bump empty-state `colSpan` from `8` to `9`. Add `${per}_vp` to CSV headers and `String(p.voting_power)` to CSV rows, positioned right after the sign field.

- [ ] **Step 2: Build the panel**

Run: `cd panel && npm run build`
Expected: build succeeds.

- [ ] **Step 3: Commit**

```bash
git add panel/src/pages/Reports.tsx
git commit -m "feat(panel): show voting power column in validator report"
```

---

# PHASE 3 — Proposer reliability

Collects the proposer of each block (already computed inline as `txProposer`), aggregates `proposed_count`, and feeds proposer reliability into the score. Surfaces it in API + panel.

## Task 9: DB — `proposed` / `proposed_count` columns

**Files:**
- Modify: `backend/internal/database/db_init.go`

- [ ] **Step 1: Add fields + migrations**

Add to `DailyParticipation` struct:

```go
	Proposed       bool      `gorm:"column:proposed;not null;default:false"`
```

Add to `DailyParticipationAgrega` struct:

```go
	ProposedCount       int    `gorm:"column:proposed_count;not null;default:0"`
```

In the idempotent migration path (next to the `voting_power` block from Task 5), add:

```go
	if err := addColumnIfMissing(sqlDB, "daily_participations", "proposed",
		"ALTER TABLE daily_participations ADD COLUMN proposed BOOLEAN NOT NULL DEFAULT false"); err != nil {
		return err
	}
	if err := addColumnIfMissing(sqlDB, "daily_participation_agregas", "proposed_count",
		"ALTER TABLE daily_participation_agregas ADD COLUMN proposed_count INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
```

- [ ] **Step 2: Build**

Run: `cd backend && go build ./...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/database/db_init.go
git commit -m "feat(db): proposed column + proposed_count aggregate for proposer score"
```

## Task 10: Collect proposer in realtime + backfill

**Files:**
- Modify: `backend/internal/gnovalidator/sync.go`, `backend/internal/gnovalidator/gnovalidator_realtime.go`

- [ ] **Step 1: Add `Proposed` to `dpRow` and the flush**

In `sync.go`, add the field to `dpRow` (after `TxContribution`):

```go
	Proposed       bool
```

Update `flushBatch`/`flushChunk`: change `const cols = 7` → `const cols = 8`, extend the INSERT column list and placeholder and args:

```go
	q := `
      INSERT INTO daily_participations
        (chain_id, date, block_height, moniker, addr, participated, tx_contribution, proposed)
      VALUES `
```
```go
		q += "(?, ?, ?, ?, ?, ?, ?, ?)"
		args = append(args, r.ChainID, r.Date, r.BlockHeight, r.Moniker, r.Addr, r.Participated, r.TxContribution, r.Proposed)
```

Add to the `ON CONFLICT ... DO UPDATE SET`:

```go
		    tx_contribution = excluded.tx_contribution,
		    proposed = excluded.proposed
```

In the two backfill row builders (`BackfillRange` ~line 126 and `BackfillParallel` ~line 231) set `Proposed` where each `dpRow` is constructed for a participating validator. The proposer address is `txProposer`/`txProp`; set:

```go
		Proposed:       addr == txProp, // BackfillParallel; use precommit.ValidatorAddress.String() == txProposer in BackfillRange
```

- [ ] **Step 2: Set `Proposed` in the realtime insert**

In `gnovalidator_realtime.go`, add `Proposed bool` to the realtime row struct (line ~124, next to `TxContribution`). In the row build loop (line ~303-316), the proposer is `txProposer`; set:

```go
			Proposed:       precommit.ValidatorAddress.String() == txProposer,
```

Update the raw insert at line ~611 to include the column. Change the INSERT statement string to add `proposed` and a placeholder, and pass `participated.Proposed`:

```go
		if err := tx.Exec(stmt, chainID, timeStp, blockHeight, moniker, valAddr,
			participated.Participated, participated.TxContribution, participated.Proposed).Error; err != nil {
```

(Update the `stmt` column list + `VALUES` placeholders + `ON CONFLICT DO UPDATE SET` to include `proposed = excluded.proposed`.)

- [ ] **Step 3: Build and vet**

Run: `cd backend && go build ./... && go vet ./internal/gnovalidator/...`
Expected: no errors.

- [ ] **Step 4: Run gnovalidator tests**

Run: `cd backend && go test ./internal/gnovalidator/... -run 'Realtime|Backfill|Sync' -v`
Expected: PASS (existing insert/flush tests still green with the extra column).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/gnovalidator/sync.go backend/internal/gnovalidator/gnovalidator_realtime.go
git commit -m "feat(collect): mark block proposer in daily_participations rows"
```

## Task 11: Aggregate `proposed_count`

**Files:**
- Modify: `backend/internal/gnovalidator/aggregator.go`
- Test: `backend/internal/gnovalidator/aggregator_test.go`

- [ ] **Step 1: Write failing test**

Add a test that seeds two raw rows (one proposed) for one complete past day, runs `AggregateChain`, and asserts `proposed_count`:

```go
func TestAggregateChain_ProposedCount(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"
	day := time.Now().UTC().AddDate(0, 0, -1)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution, proposed)
		VALUES (?, ?, 10, 'A', 'addrA', true, false, true),
		       (?, ?, 11, 'A', 'addrA', true, false, false)`,
		chain, day, chain, day).Error; err != nil {
		t.Fatal(err)
	}
	if err := AggregateChain(db, chain); err != nil {
		t.Fatal(err)
	}
	var pc int
	if err := db.Raw(`SELECT proposed_count FROM daily_participation_agregas
		WHERE chain_id=? AND addr='addrA'`, chain).Scan(&pc).Error; err != nil {
		t.Fatal(err)
	}
	if pc != 1 {
		t.Fatalf("proposed_count = %d, want 1", pc)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/gnovalidator/... -run TestAggregateChain_ProposedCount -v`
Expected: FAIL — `proposed_count` not populated (column summed as 0 or SQL error on missing select).

- [ ] **Step 3: Add `proposed_count` to the rollup SQL**

In `aggregator.go` `AggregateChain`, update the INSERT column list, the SELECT, and the `ON CONFLICT DO UPDATE SET`:

```sql
INSERT INTO daily_participation_agregas
  (chain_id, addr, block_date, moniker,
   participated_count, missed_count, tx_contribution_count, proposed_count,
   total_blocks, first_block_height, last_block_height)
SELECT
  chain_id, addr, date::date AS block_date, MAX(moniker) AS moniker,
  SUM(CASE WHEN participated     THEN 1 ELSE 0 END) AS participated_count,
  SUM(CASE WHEN NOT participated THEN 1 ELSE 0 END) AS missed_count,
  SUM(CASE WHEN tx_contribution  THEN 1 ELSE 0 END) AS tx_contribution_count,
  SUM(CASE WHEN proposed         THEN 1 ELSE 0 END) AS proposed_count,
  COUNT(*) AS total_blocks,
  MIN(block_height) AS first_block_height,
  MAX(block_height) AS last_block_height
FROM daily_participations
WHERE chain_id = ? AND date::date = ?
GROUP BY chain_id, addr, date::date
ON CONFLICT(chain_id, addr, block_date) DO UPDATE SET
  moniker               = excluded.moniker,
  participated_count    = excluded.participated_count,
  missed_count          = excluded.missed_count,
  tx_contribution_count = excluded.tx_contribution_count,
  proposed_count        = excluded.proposed_count,
  total_blocks          = excluded.total_blocks,
  first_block_height    = excluded.first_block_height,
  last_block_height     = excluded.last_block_height
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/gnovalidator/... -run TestAggregateChain_ProposedCount -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/gnovalidator/aggregator.go backend/internal/gnovalidator/aggregator_test.go
git commit -m "feat(aggregator): roll up proposed_count per day"
```

## Task 12: DB + API — proposer reliability into score

**Files:**
- Modify: `backend/internal/database/db_score.go`, `backend/internal/api/api_report.go`
- Test: `backend/internal/database/db_score_test.go`

**Interfaces:**
- Produces: `func GetChainTotalBlocks(db *gorm.DB, chainID, period string) (int64, error)`
- Modifies: `GetValidatorParticipation` now selects the real `proposed` / `proposed_count` (replacing the `0` literals from Task 2).

- [ ] **Step 1: Update `GetValidatorParticipation` to read real proposed counts**

In the raw subquery replace `0 AS proposed` with `CASE WHEN proposed THEN 1 ELSE 0 END AS proposed`; in the agrega subquery replace `0 AS proposed` with `proposed_count AS proposed`.

- [ ] **Step 2: Write failing test for chain totals**

```go
func TestGetChainTotalBlocks(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"
	now := time.Now().UTC()
	rec := now.Add(-30 * time.Minute)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution, proposed)
		VALUES (?, ?, 1, 'A', 'a', true, false, true),
		       (?, ?, 1, 'B', 'b', true, false, false),
		       (?, ?, 2, 'A', 'a', true, false, false)`,
		chain, rec, chain, rec, chain, rec).Error; err != nil {
		t.Fatal(err)
	}
	// Two distinct block heights in the window.
	n, err := GetChainTotalBlocks(db, chain, "last_24h")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("chain blocks = %d, want 2 (distinct heights)", n)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test ./internal/database/... -run TestGetChainTotalBlocks -v`
Expected: FAIL — undefined.

- [ ] **Step 4: Implement `GetChainTotalBlocks`**

Counts distinct block heights across the period (raw for today / last_24h, plus aggregate `total_blocks` max per day for past days). Since a validator's `total_blocks` per day already equals the chain's block count for that day (every active validator has one row per block), sum the per-day chain block counts:

```go
// GetChainTotalBlocks returns the number of blocks produced on the chain during
// the period, used to size expected proposal counts. Scoped to chain_id.
func GetChainTotalBlocks(db *gorm.DB, chainID, period string) (int64, error) {
	start, end, err := periodBounds(period, time.Now())
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	rawStart := todayStart
	if period == "last_24h" {
		rawStart = start
	}
	if rawStart.Before(start) {
		rawStart = start
	}

	var rawCount int64
	if err := db.Raw(`
		SELECT COUNT(DISTINCT block_height) FROM daily_participations
		WHERE chain_id = ? AND date >= ? AND date < ?
	`, chainID, rawStart, end).Scan(&rawCount).Error; err != nil {
		return 0, fmt.Errorf("GetChainTotalBlocks raw(%s,%s): %w", chainID, period, err)
	}

	var agregaCount int64
	if period != "last_24h" && todayStart.After(start) {
		// Per day, the chain's block count = MAX(total_blocks) over validators.
		if err := db.Raw(`
			SELECT COALESCE(SUM(day_blocks), 0) FROM (
				SELECT MAX(total_blocks) AS day_blocks
				FROM daily_participation_agregas
				WHERE chain_id = ? AND block_date >= ? AND block_date < ?
				GROUP BY block_date
			) t
		`, chainID, start.Format("2006-01-02"), todayStart.Format("2006-01-02")).Scan(&agregaCount).Error; err != nil {
			return 0, fmt.Errorf("GetChainTotalBlocks agrega(%s,%s): %w", chainID, period, err)
		}
	}
	return rawCount + agregaCount, nil
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test ./internal/database/... -run 'TestGetChainTotalBlocks|TestGetValidatorParticipation' -v`
Expected: PASS.

- [ ] **Step 6: Wire proposer inputs into the API handler**

In `api_report.go`, inside the per-period loop after loading `partRows`, load chain blocks once per period:

```go
		chainBlocks, err := database.GetChainTotalBlocks(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
```

In the `for addr, in := range inputs` loop (where VP is already set), add:

```go
			in.ChainBlocks = chainBlocks
```

`ProposedBlocks` is already populated from `partRows` (Task 3). `res.ProposerReliability` is already surfaced when `res.ProposerScored`. No further change.

- [ ] **Step 7: Build, vet, test**

Run: `cd backend && go build ./... && go test ./internal/api/... ./internal/database/... -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/database/db_score.go backend/internal/database/db_score_test.go backend/internal/api/api_report.go
git commit -m "feat(score): proposer reliability from proposed_count and VP share"
```

## Task 13: Panel — Proposer% column

**Files:**
- Modify: `panel/src/pages/Reports.tsx`

- [ ] **Step 1: Add the Proposer% column**

Header after VP:

```tsx
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('proposer')}>Proposer %{sortIndicator('proposer')}</th>
```

Cell after the VP cell (null → em dash when the proposer component was dropped):

```tsx
                    <td>{p && p.proposer_reliability != null ? `${p.proposer_reliability.toFixed(1)}%` : '—'}</td>
```

Sort case (treat null as -1 so unscored sort last):

```tsx
      case 'proposer':
        cmp = (pa.proposer_reliability ?? -1) - (pb.proposer_reliability ?? -1)
        break
```

Bump empty-state `colSpan` from `9` to `10`. Add `${per}_proposer` to CSV headers and `p.proposer_reliability != null ? p.proposer_reliability.toFixed(1) : ''` to CSV rows, right after the VP field.

- [ ] **Step 2: Build the panel**

Run: `cd panel && npm run build`
Expected: build succeeds.

- [ ] **Step 3: Commit**

```bash
git add panel/src/pages/Reports.tsx
git commit -m "feat(panel): show proposer reliability column in validator report"
```

---

## Final verification

- [ ] **Full backend suite**

Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: all PASS (requires a reachable Postgres per CLAUDE.md).

- [ ] **Panel build**

Run: `cd panel && npm run build`
Expected: success.

- [ ] **Update docs**

Update `backend/CLAUDE.md` Validator Health Report section and `docs/validator-report-api.md` to document: new `periodScore` fields (`sign_rate`, `proposer_reliability`, `voting_power`), the four new-plus-existing admin_config weight keys, and the layered score model. Commit:

```bash
git add backend/CLAUDE.md docs/validator-report-api.md
git commit -m "docs: document validator score v2 model and new report fields"
```

---

## Notes / accepted limitations (from the spec)

- VP is a current snapshot; the `current_year` period applies today's VP to older incidents. Accepted.
- Recency weighting inside a period is a flat average; period granularity provides coarse recency.
- Base for complete-but-not-yet-aggregated days (aggregator lag, <1h) may briefly omit the most recent complete day: it is excluded from the raw today-filter and not yet in the aggregate. Self-heals on the next hourly aggregation.
