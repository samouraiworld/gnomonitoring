# Alert Frequency (`freq`) Score Signal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a distinct-incident "frequency" penalty to the validator health score — additive to the existing `critical_count`/`warning_count`, computed from `alert_logs` with a query bounded to O(#validators that alerted) regardless of chain age.

**Architecture:** `GetValidatorScores` (db_score.go) gains an `IncidentCount` per validator via a bounded SQL CTE chain (period-scoped rows + a per-addr `LATERAL LIMIT 1` probe for pre-period context, no full-history scan). `score.Compute` gains a capped `FreqWeight`-per-incident penalty. `api_report.go` threads the new field into the JSON response. The panel gets a new "Freq" column and an updated score-formula legend.

**Tech Stack:** Go 1.x, GORM raw SQL (`db.Raw`/`db.Exec`), PostgreSQL 16, `testify`-free stdlib `testing`, `testoutils.NewTestDB` (isolated per-test Postgres schema), React/TypeScript panel.

**Spec:** `docs/superpowers/specs/2026-07-13-alert-frequency-score-design.md` — read it first for the full rationale (why additive, why this incident-boundary definition, why the bounded-query requirement exists).

## Global Constraints

- English only for all comments, commit messages, docs.
- Every query on `alert_logs` keeps `WHERE chain_id = ?` scoping and excludes `addr = 'all'`.
- `critical_count`/`warning_count` are untouched — same query shape, same values, same meaning. `freq`/`incident_count` is purely additive.
- **No full-history scan.** Any query touching pre-period `alert_logs` rows must be bounded per-addr (a `LATERAL ... LIMIT 1` probe), never a scan whose cost grows with total chain history. This is the exact anti-pattern that caused the 2026-07-13 production CPU incident (fixed in commit `659982c`) — re-verify this before merging (Task 1, Step 8).
- Backend tests use `testoutils.NewTestDB(t)`; requires `TEST_DATABASE_DSN` (test Postgres on port 5433) or the default `localhost:5432` per `CLAUDE.md`.
- `go build ./... && go vet ./... && go test ./...` must pass from `backend/` after each task.
- Branch: create a new feature branch off `main` (this work should not start until `fix/valset-membership-integrity` is merged — confirm with the user before branching if that merge hasn't happened yet).

---

### Task 1: Bounded incident-count query in the database layer

**Files:**
- Modify: `backend/internal/database/db_score.go:11-16` (`ValidatorScoreRaw`), `db_score.go:249-281` (`GetValidatorScores`)
- Test: `backend/internal/database/db_score_test.go`

**Interfaces:**
- Consumes: existing `alert_logs` table (`chain_id, addr, level, start_height, end_height, sent_at, id, moniker` columns), existing `addr_monikers` table, existing `idx_al_chain_addr_sentat` index on `(chain_id, addr, sent_at)`.
- Produces: `ValidatorScoreRaw.IncidentCount int` (json tag `incident_count`), returned by `GetValidatorScores(db *gorm.DB, chainID, period string) ([]ValidatorScoreRaw, error)` — same signature, one more populated field.

- [ ] **Step 1: Write failing tests for the three incident-boundary scenarios**

Add to `backend/internal/database/db_score_test.go` (uses the existing `seedScoreAlert` helper already in that file):

```go
func TestGetValidatorScoresIncidentCount(t *testing.T) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(now.Year(), now.Month(), 2, 9, 0, 0, 0, time.UTC)

	t.Run("escalation without RESOLVED is one incident", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		seedScoreAlert(t, db, "test20", "g1esc", "WARNING", 10, 15, day2)
		seedScoreAlert(t, db, "test20", "g1esc", "CRITICAL", 10, 40, day2.Add(time.Hour))

		rows, err := database.GetValidatorScores(db, "test20", "current_month")
		if err != nil {
			t.Fatalf("GetValidatorScores: %v", err)
		}
		if len(rows) != 1 || rows[0].IncidentCount != 1 {
			t.Fatalf("want 1 row with IncidentCount=1, got %+v", rows)
		}
	})

	t.Run("RESOLVED then new WARNING is two incidents", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		seedScoreAlert(t, db, "test20", "g1flap", "WARNING", 10, 15, day2)
		seedScoreAlert(t, db, "test20", "g1flap", "RESOLVED", 10, 15, day2.Add(time.Hour))
		seedScoreAlert(t, db, "test20", "g1flap", "WARNING", 20, 25, day2.Add(2*time.Hour))

		rows, err := database.GetValidatorScores(db, "test20", "current_month")
		if err != nil {
			t.Fatalf("GetValidatorScores: %v", err)
		}
		if len(rows) != 1 || rows[0].IncidentCount != 2 {
			t.Fatalf("want 1 row with IncidentCount=2, got %+v", rows)
		}
	})

	t.Run("incident started before the period and still resending counts zero new", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		// Started before this month, no RESOLVED yet, resent inside the period.
		seedScoreAlert(t, db, "test20", "g1cont", "CRITICAL", 10, 40, monthStart.Add(-time.Hour))
		seedScoreAlert(t, db, "test20", "g1cont", "CRITICAL", 10, 400, day2)

		rows, err := database.GetValidatorScores(db, "test20", "current_month")
		if err != nil {
			t.Fatalf("GetValidatorScores: %v", err)
		}
		if len(rows) != 1 || rows[0].IncidentCount != 0 {
			t.Fatalf("want 1 row with IncidentCount=0 (continuation, not new), got %+v", rows)
		}
		// critical_count must still count the resend row itself — unaffected by freq.
		if rows[0].CriticalCount != 1 {
			t.Fatalf("critical_count must stay a raw resend count, got %d", rows[0].CriticalCount)
		}
	})

	t.Run("addr=all and other chains never counted", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		seedScoreAlert(t, db, "test20", "all", "CRITICAL", 1, 999, day2)
		seedScoreAlert(t, db, "other20", "g1esc", "CRITICAL", 1, 999, day2)

		rows, err := database.GetValidatorScores(db, "test20", "current_month")
		if err != nil {
			t.Fatalf("GetValidatorScores: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("want no rows (addr=all excluded, other chain excluded), got %+v", rows)
		}
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/database/... -run TestGetValidatorScoresIncidentCount -v`
Expected: FAIL — `rows[0].IncidentCount` does not compile yet (field doesn't exist).

- [ ] **Step 3: Add `IncidentCount` to `ValidatorScoreRaw`**

In `backend/internal/database/db_score.go`, change:

```go
// ValidatorScoreRaw holds the raw per-validator alert metrics for one period.
type ValidatorScoreRaw struct {
	Addr           string `json:"addr"`
	Moniker        string `json:"moniker"`
	CriticalCount  int    `json:"critical_count"`
	WarningCount   int    `json:"warning_count"`
	DowntimeBlocks int64  `json:"downtime_blocks"`
	IncidentCount  int    `json:"incident_count"`
}
```

- [ ] **Step 4: Run the tests again to verify they now fail on the query, not a compile error**

Run: `go test ./internal/database/... -run TestGetValidatorScoresIncidentCount -v`
Expected: FAIL — `IncidentCount` is always 0 (query doesn't compute it yet).

- [ ] **Step 5: Rewrite `GetValidatorScores` with the bounded incident-count CTE chain**

Replace the body of `GetValidatorScores` in `backend/internal/database/db_score.go:252-281`:

```go
// GetValidatorScores returns per-validator CRITICAL/WARNING counts, summed
// downtime blocks, and distinct-incident frequency for the given chain and
// period. Ongoing outages (end_height = 0) contribute 0 downtime. Scoped to
// chain_id.
//
// IncidentCount collapses consecutive WARNING/CRITICAL rows not separated by a
// RESOLVED into one incident (an escalation from WARNING to CRITICAL is one
// incident, not two). The "prior" CTE below resolves whether the first
// in-period alert continues an already-open incident using one LATERAL
// LIMIT-1 index probe per validator that alerted this period — NOT a scan
// over all pre-period alert_logs history. See
// docs/superpowers/specs/2026-07-13-alert-frequency-score-design.md
// "Performance constraint" for why this shape is required.
func GetValidatorScores(db *gorm.DB, chainID, period string) ([]ValidatorScoreRaw, error) {
	start, end, err := periodBounds(period, time.Now())
	if err != nil {
		return nil, err
	}

	var rows []ValidatorScoreRaw
	err = db.Raw(`
		WITH in_period AS (
			SELECT addr, level, sent_at, id
			FROM alert_logs
			WHERE chain_id = ? AND addr <> 'all'
			  AND level IN ('WARNING','CRITICAL','RESOLVED')
			  AND sent_at >= ? AND sent_at < ?
		),
		addrs AS (
			SELECT DISTINCT addr FROM in_period WHERE level IN ('WARNING','CRITICAL')
		),
		prior AS (
			SELECT a.addr, p.level, p.sent_at, p.id
			FROM addrs a
			LEFT JOIN LATERAL (
				SELECT level, sent_at, id
				FROM alert_logs
				WHERE chain_id = ? AND addr = a.addr AND sent_at < ?
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
		),
		incidents AS (
			SELECT addr, COUNT(*) AS incident_count
			FROM tagged
			WHERE sent_at >= ? AND sent_at < ?
			  AND level IN ('WARNING','CRITICAL')
			  AND (prev_level IS NULL OR prev_level = 'RESOLVED')
			GROUP BY addr
		)
		SELECT al.addr AS addr,
		       COALESCE(MAX(am.moniker), MAX(al.moniker), '') AS moniker,
		       COUNT(*) FILTER (WHERE al.level = 'CRITICAL') AS critical_count,
		       COUNT(*) FILTER (WHERE al.level = 'WARNING')  AS warning_count,
		       COALESCE(SUM(
		           CASE WHEN al.level = 'CRITICAL' AND al.end_height > al.start_height
		                THEN al.end_height - al.start_height ELSE 0 END
		       ), 0) AS downtime_blocks,
		       COALESCE(MAX(incidents.incident_count), 0) AS incident_count
		FROM alert_logs al
		LEFT JOIN addr_monikers am ON am.chain_id = al.chain_id AND am.addr = al.addr
		LEFT JOIN incidents ON incidents.addr = al.addr
		WHERE al.chain_id = ?
		  AND al.level IN ('CRITICAL','WARNING')
		  AND al.addr <> 'all'
		  AND al.sent_at >= ? AND al.sent_at < ?
		GROUP BY al.addr
		ORDER BY al.addr
	`, chainID, start, end, chainID, start, start, end, chainID, start, end).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("GetValidatorScores(%s,%s): %w", chainID, period, err)
	}
	return rows, nil
}
```

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `go test ./internal/database/... -run TestGetValidatorScoresIncidentCount -v`
Expected: PASS (all four subtests).

- [ ] **Step 7: Run the full existing database suite to confirm no regression**

Run: `go test ./internal/database/... -v`
Expected: PASS, including the pre-existing `TestGetValidatorScoresCurrentMonth` (its `CriticalCount`/`WarningCount`/`DowntimeBlocks` assertions are unchanged; it does not assert `IncidentCount` so the new field doesn't affect it).

- [ ] **Step 8: Manual performance verification (required before this branch merges)**

This is not an automated test — Postgres's planner can choose a sequential scan on a tiny test table regardless of query shape, so a unit-test assertion on `EXPLAIN` output would be unreliable. Instead, run this manually against a populated database (a copy of prod, or a locally seeded chain with a realistic row count — thousands of `alert_logs` rows across many months):

```sql
EXPLAIN ANALYZE
-- paste the query from Step 5 with real chain_id/start/end values substituted
```

Confirm: no `Seq Scan on alert_logs` in the plan, and the `prior`/LATERAL portion shows an `Index Scan` (not a full sort of all pre-period rows). Record the result in the task's PR description or commit message. If a seq scan appears, check that `idx_al_chain_addr_sentat` exists (`\d alert_logs` in psql) before changing the query shape.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/database/db_score.go backend/internal/database/db_score_test.go
git commit -m "feat(score): add bounded distinct-incident count to GetValidatorScores"
```

---

### Task 2: Wire the frequency penalty into the score formula

**Files:**
- Modify: `backend/internal/score/score.go` (`Inputs`, `Weights`, `DefaultWeights`, `Compute`, admin_config keys, `WeightsFromConfig`)
- Test: `backend/internal/score/score_test.go`, `backend/internal/score/weights_test.go`

**Interfaces:**
- Consumes: nothing new from other packages — pure in-memory computation, as today.
- Produces: `Inputs.IncidentCount int` (new field, task 3 will populate it from `database.ValidatorScoreRaw.IncidentCount`), `Weights.FreqWeight int` / `Weights.FreqCap int` (defaults `3`/`30`), `KeyFreqWeight = "report_score_freq_weight"`, `KeyFreqCap = "report_score_freq_cap"`.

- [ ] **Step 1: Write failing tests for the frequency penalty**

Add to `backend/internal/score/score_test.go`:

```go
func TestCompute_FreqPenaltyApplied(t *testing.T) {
	// 100/100 signed, 2 distinct incidents @ weight 3 = 6 penalty.
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, IncidentCount: 2}, DefaultWeights())
	if r.Score != 94 {
		t.Fatalf("score = %d, want 94", r.Score)
	}
}

func TestCompute_FreqPenaltyCapped(t *testing.T) {
	// 100/100 signed, 20 incidents @ weight 3 = 60, capped at 30.
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, IncidentCount: 20}, DefaultWeights())
	if r.Score != 70 {
		t.Fatalf("score = %d, want 70 (penalty capped at 30)", r.Score)
	}
}

func TestCompute_FreqWeightZeroIsNoOp(t *testing.T) {
	w := DefaultWeights()
	w.FreqWeight = 0
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, IncidentCount: 5}, w)
	if r.Score != 100 {
		t.Fatalf("score = %d, want 100 (FreqWeight=0 must be a no-op)", r.Score)
	}
}
```

Add to `backend/internal/score/weights_test.go`:

```go
func TestWeightsFromConfig_FreqKeys(t *testing.T) {
	w := WeightsFromConfig(map[string]string{
		KeyFreqWeight: "5",
		KeyFreqCap:    "40",
	})
	if w.FreqWeight != 5 || w.FreqCap != 40 {
		t.Fatalf("freq keys not parsed: %+v", w)
	}
}

func TestWeightsFromConfig_FreqDefaultsWhenMissing(t *testing.T) {
	w := WeightsFromConfig(map[string]string{})
	if w.FreqWeight != 3 || w.FreqCap != 30 {
		t.Fatalf("want default FreqWeight=3 FreqCap=30, got %+v", w)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/score/... -run 'TestCompute_Freq|TestWeightsFromConfig_Freq' -v`
Expected: FAIL — `IncidentCount`, `FreqWeight`, `FreqCap`, `KeyFreqWeight`, `KeyFreqCap` do not exist yet (compile error).

- [ ] **Step 3: Add `IncidentCount` to `Inputs`**

In `backend/internal/score/score.go`, change the `Inputs` struct:

```go
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
	IncidentCount  int // distinct WARNING/CRITICAL incidents (not raw alert rows)
}
```

- [ ] **Step 4: Add `FreqWeight`/`FreqCap` to `Weights` and their defaults**

Change the `Weights` struct:

```go
type Weights struct {
	CriticalWeight         int
	CriticalCap            int
	DowntimeBlocksPerPoint int
	DowntimeCap            int
	WarningWeight          int
	WarningCap             int
	FreqWeight             int
	FreqCap                int
	ProposerMinExpected    int
	SignWeight             float64
	ProposerWeight         float64
	VpSeverityFactor       float64
}
```

Change `DefaultWeights()`:

```go
func DefaultWeights() Weights {
	return Weights{
		CriticalWeight:         6,
		CriticalCap:            60,
		DowntimeBlocksPerPoint: 500,
		DowntimeCap:            20,
		WarningWeight:          2,
		WarningCap:             20,
		FreqWeight:             3,
		FreqCap:                30,
		ProposerMinExpected:    5,
		SignWeight:             0.8,
		ProposerWeight:         0.2,
		VpSeverityFactor:       0.5,
	}
}
```

- [ ] **Step 5: Add the frequency penalty to `Compute`**

In `Compute`, after the `warnPenalty` block, add:

```go
	freqPenalty := in.IncidentCount * w.FreqWeight
	if freqPenalty > w.FreqCap {
		freqPenalty = w.FreqCap
	}
```

Change the `totalPenalty` line to include it:

```go
	totalPenalty := float64(critPenalty+warnPenalty+downPenalty+freqPenalty) * severity
```

- [ ] **Step 6: Add the admin_config keys and wire `WeightsFromConfig`**

Add to the key constants block:

```go
	KeyFreqWeight = "report_score_freq_weight"
	KeyFreqCap    = "report_score_freq_cap"
```

Add to `WeightsFromConfig`, alongside the other `numOr` calls:

```go
	w.FreqWeight = numOr(cfg, KeyFreqWeight, w.FreqWeight, strconv.Atoi)
	w.FreqCap = numOr(cfg, KeyFreqCap, w.FreqCap, strconv.Atoi)
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/score/... -v`
Expected: PASS, including all pre-existing tests (`TestCompute_WarningPenaltyApplied`, `TestCompute_CriticalAndDowntime`, etc. — none of them set `IncidentCount`, so it defaults to 0 and does not change their expected scores).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/score/score.go backend/internal/score/score_test.go backend/internal/score/weights_test.go
git commit -m "feat(score): add capped frequency penalty for distinct incidents"
```

---

### Task 3: Thread `incident_count` through the API

**Files:**
- Modify: `backend/internal/api/api_report.go:16-26` (`periodScore`), `:67-78` (`mergeParticipationAndAlerts`), `:185-194` (response assembly)
- Test: `backend/internal/api/api_report_test.go`

**Interfaces:**
- Consumes: `database.ValidatorScoreRaw.IncidentCount` (Task 1), `score.Inputs.IncidentCount` (Task 2).
- Produces: `periodScore.IncidentCount int` (json tag `incident_count`), populated in the JSON response returned by `GetValidatorReportHandler`.

- [ ] **Step 1: Update the existing handler test's score expectation (write this first — it documents the behavior change)**

In `backend/internal/api/api_report_test.go`, `TestGetValidatorReportHandler`, change:

```go
	p := alerting.Periods["current_month"]
	if p.CriticalCount != 1 || p.DowntimeBlocks != 30 {
		t.Fatalf("current_month wrong: %+v", p)
	}
	if p.Score != 94 || p.Tier != "Excellent" {
		t.Fatalf("score wrong: got (%d,%s), want (94,Excellent)", p.Score, p.Tier)
	}
```

to:

```go
	p := alerting.Periods["current_month"]
	if p.CriticalCount != 1 || p.DowntimeBlocks != 30 {
		t.Fatalf("current_month wrong: %+v", p)
	}
	// Score now also reflects the frequency penalty: 1 distinct incident (the
	// single CRITICAL alert) @ default FreqWeight=3, on top of the existing
	// critical penalty (@6): 100 - 6 - 3 = 91.
	if p.Score != 91 || p.Tier != "Excellent" {
		t.Fatalf("score wrong: got (%d,%s), want (91,Excellent)", p.Score, p.Tier)
	}
	if p.IncidentCount != 1 {
		t.Fatalf("want incident_count 1, got %d", p.IncidentCount)
	}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/api/... -run TestGetValidatorReportHandler -v`
Expected: FAIL — `p.IncidentCount` does not compile (field doesn't exist), and even after adding the field, the score would still read 94 until Task-1/2 wiring lands (it already has by this point in the plan, so this specifically checks the threading in this task).

- [ ] **Step 3: Add `IncidentCount` to `periodScore` and thread it through**

In `backend/internal/api/api_report.go`, change the `periodScore` struct:

```go
type periodScore struct {
	Score               int      `json:"score"`
	Tier                string   `json:"tier"`
	SignRate            float64  `json:"sign_rate"`
	ProposerReliability *float64 `json:"proposer_reliability"`
	VotingPower         int64    `json:"voting_power"`
	CriticalCount       int      `json:"critical_count"`
	WarningCount        int      `json:"warning_count"`
	IncidentCount       int      `json:"incident_count"`
	DowntimeBlocks      int64    `json:"downtime_blocks"`
	MissedBlocks        int64    `json:"missed_blocks"`
}
```

In `mergeParticipationAndAlerts`, add one line to the alert-row loop:

```go
	for _, a := range alertRows {
		if addrFilter != "" && a.Addr != addrFilter {
			continue
		}
		m := ensure(a.Addr)
		m.in.CriticalCount = a.CriticalCount
		m.in.WarningCount = a.WarningCount
		m.in.IncidentCount = a.IncidentCount
		m.in.DowntimeBlocks = a.DowntimeBlocks
		if a.Moniker != "" {
			m.moniker = a.Moniker
		}
	}
```

In the response-assembly loop, add one field to the `periodScore` literal:

```go
			ps := periodScore{
				Score: res.Score, Tier: string(res.Tier),
				SignRate:       res.SignRate,
				VotingPower:    in.VotingPower,
				CriticalCount:  in.CriticalCount,
				WarningCount:   in.WarningCount,
				IncidentCount:  in.IncidentCount,
				DowntimeBlocks: in.DowntimeBlocks,
				MissedBlocks:   in.TotalBlocks - in.SignedBlocks,
			}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/api/... -run TestGetValidatorReportHandler -v`
Expected: PASS.

- [ ] **Step 5: Run the full API suite to confirm no regression**

Run: `go test ./internal/api/... -v`
Expected: PASS — `TestGetValidatorReportHandlerAlertOnlyNoParticipation` (score stays 0, already clamped, unaffected by an added penalty), `TestGetValidatorReportHandlerMissedAndLastAlert` (asserts `missed_blocks`/`days_since_last_alert` only, unaffected), `TestGetValidatorReportHandlerInvalidChain` (unaffected) must all still pass unchanged.

- [ ] **Step 6: Full backend build + vet + test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/api/api_report.go backend/internal/api/api_report_test.go
git commit -m "feat(api): surface incident_count in the validator report response"
```

---

### Task 4: Panel — "Freq" column and score-legend update

**Files:**
- Modify: `panel/src/types/report.ts`, `panel/src/pages/Reports.tsx`, `panel/src/components/ScoreLegend.tsx`

**Interfaces:**
- Consumes: `incident_count` field on the JSON response (Task 3).
- Produces: no new exported interfaces — internal component/type changes only.

- [ ] **Step 1: Add `incident_count` to the `PeriodScore` type**

In `panel/src/types/report.ts`, change:

```ts
export interface PeriodScore {
  score: number
  tier: 'Excellent' | 'Good' | 'Watch' | 'Critical'
  sign_rate: number
  proposer_reliability: number | null
  voting_power: number
  critical_count: number
  warning_count: number
  incident_count: number
  downtime_blocks: number
  missed_blocks: number
}
```

- [ ] **Step 2: Add the "Freq" column to `Reports.tsx`**

In `panel/src/pages/Reports.tsx`, add a new `case` to the `sorted` switch statement, right after `case 'warning':`:

```ts
      case 'warning':
        cmp = pa.warning_count - pb.warning_count
        break
      case 'freq':
        cmp = pa.incident_count - pb.incident_count
        break
```

Add a new header cell right after the Warning header (`onClick={() => handleSort('warning')}` th):

```tsx
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('freq')}><HeaderTip align="right" tip="Distinct incidents this period — WARNING/CRITICAL alerts collapsed across resends, separated only by a RESOLVED. A better flapping signal than the raw Critical/Warning counts.">Freq{sortIndicator('freq')}</HeaderTip></th>
```

Add a new body cell right after the Warning cell (`<td>{p ? p.warning_count : '—'}</td>`):

```tsx
                    <td>{p ? p.incident_count : '—'}</td>
```

Bump the "No data" row's `colSpan` from `12` to `13`:

```tsx
                <tr><td colSpan={13}><div className="empty-state"><div className="empty-state-title">No data</div></div></td></tr>
```

Update `handleExportCsv`'s header list — add `${per}_freq` right after `${per}_warning`:

```ts
      headers.push(`${per}_score`, `${per}_tier`, `${per}_sign`, `${per}_vp`, `${per}_proposer`, `${per}_critical`, `${per}_warning`, `${per}_freq`, `${per}_downtime`, `${per}_missed`)
```

Update the CSV row-building loop — add `p.incident_count` right after `p.warning_count`, in both the populated and empty branches:

```ts
        if (p) {
          cells.push(String(p.score), p.tier, p.sign_rate.toFixed(1), String(p.voting_power), p.proposer_reliability != null ? p.proposer_reliability.toFixed(1) : '', String(p.critical_count), String(p.warning_count), String(p.incident_count), String(p.downtime_blocks), String(p.missed_blocks))
        } else {
          cells.push('', '', '', '', '', '', '', '', '', '')
        }
```

(note: the empty-cells array grows from nine `''` to ten, matching the ten pushed fields above)

- [ ] **Step 3: Update `ScoreLegend.tsx` with the freq defaults and formula line**

In `panel/src/components/ScoreLegend.tsx`, add to `DEFAULT_WEIGHTS`:

```ts
const DEFAULT_WEIGHTS: Record<string, number> = {
  report_score_critical_weight: 6,
  report_score_critical_cap: 60,
  report_score_warning_weight: 2,
  report_score_warning_cap: 20,
  report_score_freq_weight: 3,
  report_score_freq_cap: 30,
  report_score_downtime_blocks_per_point: 500,
  report_score_downtime_cap: 20,
  report_score_proposer_min_expected: 5,
  report_score_sign_weight: 0.8,
  report_score_proposer_weight: 0.2,
  report_score_vp_severity_factor: 0.5,
}
```

Add the two derived constants inside the component, after `warningCap`:

```ts
  const freqWeight = weight(thresholds, 'report_score_freq_weight')
  const freqCap = weight(thresholds, 'report_score_freq_cap')
```

Update the penalties formula block to add a bullet:

```tsx
          <code className="score-legend-formula">{`penalties = (critical + warning + freq + downtime) × severity
  • −${criticalWeight} per CRITICAL alert (max ${criticalCap})
  • −${warningWeight} per WARNING alert (max ${warningCap})
  • −${freqWeight} per distinct incident (max ${freqCap})
  • −1 per ${downtimePerPoint} downtime blocks (max ${downtimeCap})
severity = 1 + ${vpSeverity} × (VP / max VP)`}</code>
```

- [ ] **Step 4: Typecheck the panel**

Run: `cd panel && npm run build`
Expected: build succeeds with no TypeScript errors.

- [ ] **Step 5: Manual smoke check**

Run: `cd panel && npm run dev`, open the Reports page for a chain with existing alert history, confirm the "Freq" column renders, sorts, and the legend shows the new bullet with the right numbers (3/30 unless admin_config overrides them).

- [ ] **Step 6: Commit**

```bash
git add panel/src/types/report.ts panel/src/pages/Reports.tsx panel/src/components/ScoreLegend.tsx
git commit -m "feat(panel): add Freq column and update score legend for incident frequency"
```

---

### Task 5: Documentation

**Files:**
- Modify: `CLAUDE.md` ("Validator Health Report" section)

**Interfaces:** none — documentation only.

- [ ] **Step 1: Update the periods field list and weights list**

In `CLAUDE.md`, under "## Validator Health Report", find the line:

```
**Periods** — Endpoint returns four period scores: `last_24h`, `current_week`, `current_month`, `current_year`. Each period surfaces `score`, `tier`, `sign_rate`, `proposer_reliability` (nullable), `voting_power`, `critical_count`, `warning_count`, `downtime_blocks`, `missed_blocks` (= `total − signed`, display-only, already reflected in sign_rate).
```

Change it to:

```
**Periods** — Endpoint returns four period scores: `last_24h`, `current_week`, `current_month`, `current_year`. Each period surfaces `score`, `tier`, `sign_rate`, `proposer_reliability` (nullable), `voting_power`, `critical_count`, `warning_count`, `incident_count`, `downtime_blocks`, `missed_blocks` (= `total − signed`, display-only, already reflected in sign_rate). `incident_count` is the number of distinct WARNING/CRITICAL incidents (consecutive alert rows not separated by a RESOLVED collapse into one), computed with a query bounded to O(#validators that alerted that period) — never a full `alert_logs` history scan (see `GetValidatorScores` in `db_score.go`).
```

Find the line:

```
Weights via admin_config: `report_score_{critical_weight,critical_cap,warning_weight,warning_cap,downtime_blocks_per_point,downtime_cap,sign_weight,proposer_weight,proposer_min_expected,vp_severity_factor}` (defaults 6/60/2/20/500/20/0.8/0.2/5/0.5). Missing keys fall back to code defaults (no seeding).
```

Change it to:

```
Weights via admin_config: `report_score_{critical_weight,critical_cap,warning_weight,warning_cap,freq_weight,freq_cap,downtime_blocks_per_point,downtime_cap,sign_weight,proposer_weight,proposer_min_expected,vp_severity_factor}` (defaults 6/60/2/20/3/30/500/20/0.8/0.2/5/0.5). Missing keys fall back to code defaults (no seeding).
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: document incident_count and freq weights in Validator Health Report"
```

---

## Self-Review

**Spec coverage:** additive field/penalty (Task 2-3) ✅; incident boundary = escalation collapses, RESOLVED breaks (Task 1 tests) ✅; scope WARNING+CRITICAL only, `addr<>'all'` (Task 1 query) ✅; capped raw-count penalty shape, not a rate (Task 2) ✅; no schema change, no write-path change (Task 1 only touches a read query) ✅; bounded scan requirement (Task 1 Step 5 query + Step 8 manual verification) ✅; panel column + legend (Task 4) ✅; docs (Task 5) ✅. Valset departure / rate normalization explicitly out of scope per spec — no task added for them. ✅

**Placeholder scan:** every step shows complete code (full struct literals, full SQL, full test bodies) with exact file paths; the one manual (non-automated) step is Task 1 Step 8, explicitly justified (planner behavior on tiny test tables is not a reliable oracle) rather than left vague. ✅

**Type consistency:** `ValidatorScoreRaw.IncidentCount` (Task 1) → `score.Inputs.IncidentCount` (Task 2, threaded in Task 3's `mergeParticipationAndAlerts`) → `periodScore.IncidentCount` json `incident_count` (Task 3) → `PeriodScore.incident_count` (Task 4) — same name/shape at every hop except the deliberate Go `IncidentCount` / JSON `incident_count` casing split, consistent with every other field in this struct (`CriticalCount`/`critical_count`, etc.). `Weights.FreqWeight`/`FreqCap` and `KeyFreqWeight`/`KeyFreqCap` used consistently between Task 2's `Compute`, `WeightsFromConfig`, and Task 4's `ScoreLegend.tsx` key strings (`report_score_freq_weight`/`report_score_freq_cap`). ✅
