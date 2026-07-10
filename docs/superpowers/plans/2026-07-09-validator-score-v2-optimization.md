# Validator Score v2 Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the seven score-v2 code-review findings as behavior-preserving refactors — remove duplication, cut the report handler's DB round-trips, batch the voting-power upsert, and drop redundant migrations — with no change to any score, JSON field, or CSV cell.

**Architecture:** Centralize the raw/aggregate day-partition math in one `computePartition` helper; fold the chain-block count into the participation query so it returns in a single round-trip; batch the per-validator VP upsert; delete the `addColumnIfMissing` migrations (AutoMigrate already covers the columns); collapse the handler's parallel maps and the duplicated proposer derivation.

**Tech Stack:** Go 1.x, GORM + raw SQL over PostgreSQL, `net/http` handlers, React + TypeScript panel (panel untouched this plan).

## Global Constraints

- **Behavior-preserving.** No observable change to scores, `/api/reports/validators` payload, panel, or CSV. Existing suites are the safety net and must stay green.
- **English only** for all comments, docs, commit messages.
- **`chain_id` scoping** on every query touching `daily_participations`, `daily_participation_agregas`, `alert_logs`, `addr_monikers`.
- **`score` package stays DB-free / pure.**
- **Branch:** `feat/validator-score-v2` (continue on it; do not branch).
- **Tests need Postgres** (per repo CLAUDE.md). If none is running:
  ```bash
  docker run --rm -d --name gnomonitoring-test-pg -p 5432:5432 \
    -e POSTGRES_USER=gnomonitoring -e POSTGRES_PASSWORD=gnomonitoring -e POSTGRES_DB=gnomonitoring_test \
    postgres:16
  ```
- All backend commands run from `backend/`.
- Commit after each task.

---

## File Structure

- `backend/internal/database/db_score.go` — add `computePartition`; refactor `GetValidatorParticipation` (new signature returns chain blocks); delete `GetChainTotalBlocks`.
- `backend/internal/database/periodbounds_internal_test.go` — add `TestComputePartition` (internal package).
- `backend/internal/database/db_score_test.go` — update participation tests to the new signature; retarget the chain-blocks test.
- `backend/internal/database/db.go` — add `AddrVP` type and `UpsertAddrMonikerVPBatch`.
- `backend/internal/database/db_vp_test.go` — add `TestUpsertAddrMonikerVPBatch`.
- `backend/internal/database/db_init.go` — delete the three `addColumnIfMissing` blocks, the helper, and the `database/sql` import.
- `backend/internal/score/score.go` — replace `intOr`/`floatOr` with generic `numOr`.
- `backend/internal/gnovalidator/sync.go`, `gnovalidator_realtime.go` — de-dupe proposer derivation.
- `backend/internal/gnovalidator/valoper.go` — call the batch VP upsert.
- `backend/internal/api/api_report.go` — read chain blocks from `GetValidatorParticipation`; collapse the merge into one map.

---

# PHASE A — DB layer

## Task 1: Extract `computePartition` helper

Pure extraction. `GetValidatorParticipation` starts using it; `GetChainTotalBlocks` is left untouched (deleted in Task 2). Build stays green.

**Files:**
- Modify: `backend/internal/database/db_score.go`
- Test: `backend/internal/database/periodbounds_internal_test.go`

**Interfaces:**
- Produces:
  - `type periodPartition struct { rawStart, end time.Time; agregaStart, agregaEnd string; includeAgrega bool }`
  - `func computePartition(period string, now time.Time) (periodPartition, error)`

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/database/periodbounds_internal_test.go`:

```go
func TestComputePartition(t *testing.T) {
	// 2026-07-09T02:30:00Z, expressed in UTC-5 (still Jul 8 locally).
	minus5 := time.FixedZone("UTC-5", -5*3600)
	now := time.Date(2026, 7, 8, 21, 30, 0, 0, minus5)
	todayUTC := "2026-07-09"

	t.Run("current_month includes agrega, raw starts today UTC", func(t *testing.T) {
		p, err := computePartition("current_month", now)
		if err != nil {
			t.Fatal(err)
		}
		if !p.includeAgrega {
			t.Fatalf("current_month should include agrega")
		}
		if p.agregaEnd != todayUTC {
			t.Fatalf("agregaEnd = %q, want %q", p.agregaEnd, todayUTC)
		}
		if p.agregaStart != "2026-07-01" {
			t.Fatalf("agregaStart = %q, want 2026-07-01", p.agregaStart)
		}
		wantRaw := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
		if !p.rawStart.Equal(wantRaw) {
			t.Fatalf("rawStart = %s, want %s", p.rawStart, wantRaw)
		}
	})

	t.Run("last_24h excludes agrega, raw is full window", func(t *testing.T) {
		p, err := computePartition("last_24h", now)
		if err != nil {
			t.Fatal(err)
		}
		if p.includeAgrega {
			t.Fatalf("last_24h must not include agrega")
		}
		wantRaw := time.Date(2026, 7, 9, 2, 30, 0, 0, time.UTC).Add(-24 * time.Hour)
		if !p.rawStart.Equal(wantRaw) {
			t.Fatalf("rawStart = %s, want %s", p.rawStart, wantRaw)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/database/... -run TestComputePartition -v`
Expected: FAIL — `undefined: computePartition`.

- [ ] **Step 3: Add the helper**

In `db_score.go`, immediately after the `periodBounds` function, add:

```go
// periodPartition describes how a report period splits across the durable daily
// aggregate (complete past days) and the raw current-day rows, with the seam
// fixed at today 00:00 UTC to avoid double counting.
type periodPartition struct {
	rawStart, end          time.Time // raw window [rawStart, end)
	agregaStart, agregaEnd string    // aggregate window [agregaStart, agregaEnd) as YYYY-MM-DD
	includeAgrega          bool
}

// computePartition derives the raw/aggregate windows for a report period. All
// bounds are UTC so they line up with the UTC block timestamps and block_date
// day strings.
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

- [ ] **Step 4: Refactor `GetValidatorParticipation` to use it**

Replace the body of `GetValidatorParticipation` (from its `start, end, err :=` line through the `includeAgrega :=` line) so it uses the helper. The function signature and the SQL below stay exactly as they are today; only the bound-derivation block changes:

```go
func GetValidatorParticipation(db *gorm.DB, chainID, period string) ([]ParticipationRaw, error) {
	p, err := computePartition(period, time.Now())
	if err != nil {
		return nil, err
	}

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
			       CASE WHEN proposed THEN 1 ELSE 0 END AS proposed
			FROM daily_participations
			WHERE chain_id = ? AND date >= ? AND date < ?
	`
	args := []any{chainID, p.rawStart, p.end}
	if p.includeAgrega {
		q += `
			UNION ALL
			SELECT addr,
			       participated_count AS signed,
			       total_blocks       AS total,
			       proposed_count     AS proposed
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ? AND block_date < ?
		`
		args = append(args, chainID, p.agregaStart, p.agregaEnd)
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

- [ ] **Step 5: Run the partition + participation tests**

Run: `go test ./internal/database/... -run 'TestComputePartition|TestGetValidatorParticipation' -v`
Expected: PASS (partition unit test green; both existing participation tests still green — signature unchanged).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/database/db_score.go backend/internal/database/periodbounds_internal_test.go
git commit -m "refactor(db): extract computePartition helper for score period windows"
```

## Task 2: Fold chain blocks into `GetValidatorParticipation`; delete `GetChainTotalBlocks`

`GetValidatorParticipation` returns the chain-block count as a second value via a `CROSS JOIN` to the identical scalar it used to compute separately. `GetChainTotalBlocks` is deleted and its handler call removed. Value is unchanged.

**Files:**
- Modify: `backend/internal/database/db_score.go`
- Modify: `backend/internal/api/api_report.go:100`
- Test: `backend/internal/database/db_score_test.go`

**Interfaces:**
- Produces: `func GetValidatorParticipation(db *gorm.DB, chainID, period string) ([]ParticipationRaw, int64, error)` (chain blocks is the new middle return)
- Removes: `func GetChainTotalBlocks(...)`
- Consumes: `computePartition` (Task 1)

- [ ] **Step 1: Update the participation tests to the new signature and retarget the chain-blocks test**

In `db_score_test.go`, change the two `GetValidatorParticipation` call sites to accept three returns, and replace `TestGetChainTotalBlocks` so it asserts the chain-block count now returned by `GetValidatorParticipation`.

`TestGetValidatorParticipation_UnionAgregaAndToday` — change:
```go
	rows, err := database.GetValidatorParticipation(db, chain, "current_month")
```
to:
```go
	rows, _, err := database.GetValidatorParticipation(db, chain, "current_month")
```

`TestGetValidatorParticipation_Last24hRawOnly` — change:
```go
	rows, err := database.GetValidatorParticipation(db, chain, "last_24h")
```
to:
```go
	rows, _, err := database.GetValidatorParticipation(db, chain, "last_24h")
```

Replace the whole `TestGetChainTotalBlocks` function with:
```go
func TestGetValidatorParticipation_ChainBlocks(t *testing.T) {
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
	_, chainBlocks, err := database.GetValidatorParticipation(db, chain, "last_24h")
	if err != nil {
		t.Fatal(err)
	}
	if chainBlocks != 2 {
		t.Fatalf("chain blocks = %d, want 2 (distinct heights)", chainBlocks)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/database/... -run 'TestGetValidatorParticipation' -v`
Expected: FAIL — compile error (assignment mismatch: `GetValidatorParticipation` returns 2 values, tests expect 3).

- [ ] **Step 3: Rewrite `GetValidatorParticipation` to return chain blocks**

Replace the entire `GetValidatorParticipation` function with:

```go
// GetValidatorParticipation returns per-validator signed/total (and proposed)
// block counts for the period, plus the chain's total block count over the same
// period (used to size expected proposal counts). It reads durable daily
// aggregates for complete past days and raw rows for the current day,
// partitioned at today 00:00 UTC to avoid double counting. last_24h reads only
// raw rows (block granularity). Scoped to chain_id.
func GetValidatorParticipation(db *gorm.DB, chainID, period string) ([]ParticipationRaw, int64, error) {
	p, err := computePartition(period, time.Now())
	if err != nil {
		return nil, 0, err
	}

	// Participation arms: raw current-day rows, plus aggregate past days.
	combined := `
		SELECT addr,
		       CASE WHEN participated THEN 1 ELSE 0 END AS signed,
		       1 AS total,
		       CASE WHEN proposed THEN 1 ELSE 0 END AS proposed
		FROM daily_participations
		WHERE chain_id = ? AND date >= ? AND date < ?`
	args := []any{chainID, p.rawStart, p.end}
	if p.includeAgrega {
		combined += `
		UNION ALL
		SELECT addr, participated_count AS signed, total_blocks AS total, proposed_count AS proposed
		FROM daily_participation_agregas
		WHERE chain_id = ? AND block_date >= ? AND block_date < ?`
		args = append(args, chainID, p.agregaStart, p.agregaEnd)
	}

	// Chain-block scalar: distinct raw heights, plus (only when the aggregate
	// window applies) the per-day chain block count summed over past days. This
	// matches the semantics of the former GetChainTotalBlocks exactly.
	cb := `SELECT (SELECT COUNT(DISTINCT block_height) FROM daily_participations
	               WHERE chain_id = ? AND date >= ? AND date < ?)`
	args = append(args, chainID, p.rawStart, p.end)
	if p.includeAgrega {
		cb += ` + COALESCE((SELECT SUM(day_blocks) FROM (
		            SELECT MAX(total_blocks) AS day_blocks
		            FROM daily_participation_agregas
		            WHERE chain_id = ? AND block_date >= ? AND block_date < ?
		            GROUP BY block_date) t), 0)`
		args = append(args, chainID, p.agregaStart, p.agregaEnd)
	}
	cb += ` AS chain_blocks`

	q := `
		SELECT combined.addr AS addr,
		       SUM(combined.signed)   AS signed_blocks,
		       SUM(combined.total)    AS total_blocks,
		       SUM(combined.proposed) AS proposed_blocks,
		       cb.chain_blocks        AS chain_blocks
		FROM (` + combined + `) combined
		CROSS JOIN (` + cb + `) cb
		GROUP BY combined.addr, cb.chain_blocks
		ORDER BY combined.addr`

	type scanRow struct {
		Addr           string
		SignedBlocks   int64
		TotalBlocks    int64
		ProposedBlocks int64
		ChainBlocks    int64
	}
	var scanned []scanRow
	if err := db.Raw(q, args...).Scan(&scanned).Error; err != nil {
		return nil, 0, fmt.Errorf("GetValidatorParticipation(%s,%s): %w", chainID, period, err)
	}

	rows := make([]ParticipationRaw, len(scanned))
	var chainBlocks int64
	for i, s := range scanned {
		rows[i] = ParticipationRaw{
			Addr:           s.Addr,
			SignedBlocks:   s.SignedBlocks,
			TotalBlocks:    s.TotalBlocks,
			ProposedBlocks: s.ProposedBlocks,
		}
		chainBlocks = s.ChainBlocks // identical on every row (grouped scalar)
	}
	return rows, chainBlocks, nil
}
```

- [ ] **Step 4: Delete `GetChainTotalBlocks`**

Remove the entire `GetChainTotalBlocks` function from `db_score.go` (the block starting `// GetChainTotalBlocks returns the number of blocks produced` through its closing brace).

- [ ] **Step 5: Update the API handler call site**

In `backend/internal/api/api_report.go`, inside the `for _, period := range reportPeriods` loop, replace these two blocks:

```go
		partRows, err := database.GetValidatorParticipation(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		chainBlocks, err := database.GetChainTotalBlocks(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
```

with:

```go
		partRows, chainBlocks, err := database.GetValidatorParticipation(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
```

- [ ] **Step 6: Build, then run DB + API tests**

Run: `go build ./... && go test ./internal/database/... ./internal/api/... -v`
Expected: PASS — `TestGetValidatorParticipation_ChainBlocks` returns 2; union case still 81/102; `TestGetValidatorReportHandler*` still green (scores unchanged).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/database/db_score.go backend/internal/database/db_score_test.go backend/internal/api/api_report.go
git commit -m "perf(db): return chain blocks from GetValidatorParticipation, drop GetChainTotalBlocks"
```

## Task 3: Batch voting-power upsert

Add a chunked multi-row upsert next to the single-row `UpsertAddrMonikerVP` (which stays for existing callers).

**Files:**
- Modify: `backend/internal/database/db.go`
- Test: `backend/internal/database/db_vp_test.go`

**Interfaces:**
- Produces:
  - `type AddrVP struct { Addr string; VotingPower int64 }`
  - `func UpsertAddrMonikerVPBatch(db *gorm.DB, chainID string, rows []AddrVP) error`

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/database/db_vp_test.go`:

```go
func TestUpsertAddrMonikerVPBatch(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Pre-existing moniker row must keep its moniker; VP updates in place.
	if err := database.UpsertAddrMoniker(db, "test13", "a", "alpha"); err != nil {
		t.Fatal(err)
	}

	// 300 rows crosses the chunk boundary (>247).
	rows := make([]database.AddrVP, 0, 300)
	rows = append(rows, database.AddrVP{Addr: "a", VotingPower: 100})
	for i := 1; i < 300; i++ {
		rows = append(rows, database.AddrVP{Addr: fmt.Sprintf("v%03d", i), VotingPower: int64(i)})
	}
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", rows); err != nil {
		t.Fatal(err)
	}

	var vpA, vp299 int64
	var monA string
	if err := db.Raw(`SELECT voting_power, moniker FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "a").Row().Scan(&vpA, &monA); err != nil {
		t.Fatal(err)
	}
	if vpA != 100 || monA != "alpha" {
		t.Fatalf("addr a: vp=%d moniker=%q, want 100/alpha", vpA, monA)
	}
	if err := db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "v299").Scan(&vp299).Error; err != nil {
		t.Fatal(err)
	}
	if vp299 != 299 {
		t.Fatalf("addr v299: vp=%d, want 299 (chunk boundary)", vp299)
	}
}
```

The test file needs `fmt` in its imports. Its current imports are `"testing"`, the `database` package, and `testoutils`; add `"fmt"`:

```go
import (
	"fmt"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/database/... -run TestUpsertAddrMonikerVPBatch -v`
Expected: FAIL — `undefined: database.AddrVP` / `UpsertAddrMonikerVPBatch`.

- [ ] **Step 3: Implement the batch upsert**

In `db.go`, immediately after `UpsertAddrMonikerVP`, add (`fmt` is already imported):

```go
// AddrVP pairs a validator address with its voting power for batch upserts.
type AddrVP struct {
	Addr        string
	VotingPower int64
}

// UpsertAddrMonikerVPBatch writes voting power for many validators in chunked
// multi-row upserts, inserting a row (empty moniker) when none exists. Same
// per-row semantics as UpsertAddrMonikerVP. Scoped to chain_id.
func UpsertAddrMonikerVPBatch(db *gorm.DB, chainID string, rows []AddrVP) error {
	if len(rows) == 0 {
		return nil
	}
	const perRowBinds = 3 // chain_id, addr, voting_power (moniker is a literal '')
	const maxBinds = 990
	maxRows := maxBinds / perRowBinds
	for i := 0; i < len(rows); i += maxRows {
		j := i + maxRows
		if j > len(rows) {
			j = len(rows)
		}
		chunk := rows[i:j]
		q := `INSERT INTO addr_monikers (chain_id, addr, moniker, voting_power) VALUES `
		args := make([]any, 0, len(chunk)*perRowBinds)
		for k, r := range chunk {
			if k > 0 {
				q += ","
			}
			q += "(?, ?, '', ?)"
			args = append(args, chainID, r.Addr, r.VotingPower)
		}
		q += ` ON CONFLICT(chain_id, addr) DO UPDATE SET voting_power = excluded.voting_power`
		if err := db.Exec(q, args...).Error; err != nil {
			return fmt.Errorf("UpsertAddrMonikerVPBatch(%s): %w", chainID, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/database/... -run TestUpsertAddrMonikerVPBatch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/database/db.go backend/internal/database/db_vp_test.go
git commit -m "perf(db): batch voting-power upsert (UpsertAddrMonikerVPBatch)"
```

## Task 4: Generic `numOr` config helper

**Files:**
- Modify: `backend/internal/score/score.go`

**Interfaces:**
- Consumes/Produces (internal): replaces `intOr` and `floatOr` with `numOr[T]`. `WeightsFromConfig` signature is unchanged.

- [ ] **Step 1: Replace the two helpers with one generic**

In `score.go`, delete both `intOr` and `floatOr` and add:

```go
// numOr returns the parsed config value for key, or fallback when the key is
// absent or unparseable.
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

- [ ] **Step 2: Update `WeightsFromConfig` call sites**

Replace the body of `WeightsFromConfig` (the `w.X = intOr(...)` / `floatOr(...)` lines) with:

```go
	w.CriticalWeight = numOr(cfg, KeyCriticalWeight, w.CriticalWeight, strconv.Atoi)
	w.CriticalCap = numOr(cfg, KeyCriticalCap, w.CriticalCap, strconv.Atoi)
	w.DowntimeBlocksPerPoint = numOr(cfg, KeyDowntimeBlocksPerPoint, w.DowntimeBlocksPerPoint, strconv.Atoi)
	w.DowntimeCap = numOr(cfg, KeyDowntimeCap, w.DowntimeCap, strconv.Atoi)
	w.WarningWeight = numOr(cfg, KeyWarningWeight, w.WarningWeight, strconv.Atoi)
	w.WarningCap = numOr(cfg, KeyWarningCap, w.WarningCap, strconv.Atoi)
	w.ProposerMinExpected = numOr(cfg, KeyProposerMinExpected, w.ProposerMinExpected, strconv.Atoi)
	w.SignWeight = numOr(cfg, KeySignWeight, w.SignWeight, parseFloat64)
	w.ProposerWeight = numOr(cfg, KeyProposerWeight, w.ProposerWeight, parseFloat64)
	w.VpSeverityFactor = numOr(cfg, KeyVpSeverityFactor, w.VpSeverityFactor, parseFloat64)
```

`strconv.Atoi` already has signature `func(string) (int, error)`. Add a small adapter for the float parser (a plain `strconv.ParseFloat` takes two args, so it needs wrapping) near `numOr`:

```go
func parseFloat64(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
```

- [ ] **Step 3: Build and run the score tests**

Run: `go build ./... && go test ./internal/score/... -v`
Expected: PASS — `weights_test.go` (`TestWeightsFromConfig*`) and `score_test.go` unchanged and green.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/score/score.go
git commit -m "refactor(score): collapse intOr/floatOr into generic numOr"
```

## Task 5: Remove redundant `addColumnIfMissing` migrations

AutoMigrate (`db_init.go:418`) already covers `voting_power`, `proposed`, `proposed_count` via the struct tags. Delete the hand-rolled ALTERs, the helper, and the now-unused import.

**Files:**
- Modify: `backend/internal/database/db_init.go`

- [ ] **Step 1: Delete the three migration blocks in `InitDB`**

Remove these lines (the block between `ApplyGovdaoCompositePrimaryKeyMigration` and `CreateOrReplaceIndexes`):

```go
	// Idempotent: voting_power on addr_monikers (score v2).
	if err := addColumnIfMissing(sqlDB, "addr_monikers", "voting_power",
		"ALTER TABLE addr_monikers ADD COLUMN voting_power BIGINT NOT NULL DEFAULT 0"); err != nil {
		return nil, fmt.Errorf("addColumnIfMissing(addr_monikers.voting_power): %w", err)
	}

	// Idempotent: proposed / proposed_count (score v2 proposer metric).
	if err := addColumnIfMissing(sqlDB, "daily_participations", "proposed",
		"ALTER TABLE daily_participations ADD COLUMN proposed BOOLEAN NOT NULL DEFAULT false"); err != nil {
		return nil, fmt.Errorf("addColumnIfMissing(daily_participations.proposed): %w", err)
	}
	if err := addColumnIfMissing(sqlDB, "daily_participation_agregas", "proposed_count",
		"ALTER TABLE daily_participation_agregas ADD COLUMN proposed_count INTEGER NOT NULL DEFAULT 0"); err != nil {
		return nil, fmt.Errorf("addColumnIfMissing(daily_participation_agregas.proposed_count): %w", err)
	}
```

- [ ] **Step 2: Delete the `addColumnIfMissing` helper**

Remove the whole function (starts at the comment `// addColumnIfMissing runs alterSQL only when table.column does not yet exist.`).

- [ ] **Step 3: Remove the now-unused `database/sql` import**

In the `import` block of `db_init.go`, delete the `"database/sql"` line. (It was used only by `addColumnIfMissing`'s `*sql.DB` parameter; every other `sqlDB` is a local from `db.DB()`.)

- [ ] **Step 4: Build and vet**

Run: `go build ./... && go vet ./internal/database/...`
Expected: no errors (no "imported and not used", no "undefined: addColumnIfMissing").

- [ ] **Step 5: Verify the columns still migrate**

Confirm AutoMigrate produces the columns on a fresh DB by running the DB suite (each test spins an isolated schema and migrates it):

Run: `go test ./internal/database/... -run 'TestUpsertAddrMonikerVP|TestGetValidatorParticipation' -v`
Expected: PASS — inserts/reads against `voting_power` / `proposed` / `proposed_count` succeed, proving AutoMigrate created them without the manual ALTERs.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/database/db_init.go
git commit -m "refactor(db): drop addColumnIfMissing migrations, rely on AutoMigrate"
```

---

# PHASE B — Collectors

## Task 6: De-dupe proposer derivation across the three collectors

Each collector calls `ProposerAddress.String()` twice. Compute it once and derive the tx-proposer from it. Semantics unchanged; the existing collector/backfill tests are the gate.

**Files:**
- Modify: `backend/internal/gnovalidator/gnovalidator_realtime.go:291-299`
- Modify: `backend/internal/gnovalidator/sync.go:107-115` (BackfillRange) and `:209-216` (BackfillParallel)

- [ ] **Step 1: `CollectParticipation` (realtime)**

In `gnovalidator_realtime.go`, replace:

```go
				// == IF in json return section Data, have a tx and get proposer of tx
				var txProposer string
				if len(block.Block.Data.Txs) > 0 {
					txProposer = block.Block.Header.ProposerAddress.String()

				}
				// Actual block proposer, unconditional (a block always has a proposer,
				// independent of whether it contains transactions).
				proposerAddr := block.Block.Header.ProposerAddress.String()
```

with:

```go
				// Actual block proposer, resolved once. A block always has a
				// proposer; txProposer is the same value, meaningful only when
				// the block carries transactions.
				proposerAddr := block.Block.Header.ProposerAddress.String()
				var txProposer string
				if len(block.Block.Data.Txs) > 0 {
					txProposer = proposerAddr
				}
```

- [ ] **Step 2: `BackfillRange` (sync.go, sequential)**

In `sync.go`, replace:

```go
			// == IF in json return section Data, have a tx and get proposer of tx
			var txProposer string
			if len(block.Block.Data.Txs) > 0 {
				txProposer = block.Block.Header.ProposerAddress.String()

			}
			// Actual block proposer, unconditional (a block always has a proposer,
			// independent of whether it contains transactions).
			proposerAddr := block.Block.Header.ProposerAddress.String()
```

with:

```go
			// Actual block proposer, resolved once. txProposer is the same value,
			// meaningful only when the block carries transactions.
			proposerAddr := block.Block.Header.ProposerAddress.String()
			var txProposer string
			if len(block.Block.Data.Txs) > 0 {
				txProposer = proposerAddr
			}
```

- [ ] **Step 3: `BackfillParallel` (sync.go, worker)**

In `sync.go`, replace:

```go
				hasTx := len(b.Block.Data.Txs) > 0
				var txProp string
				if hasTx {
					txProp = b.Block.Header.ProposerAddress.String()
				}
				// Actual block proposer, unconditional (a block always has a proposer,
				// independent of whether it contains transactions).
				proposerAddr := b.Block.Header.ProposerAddress.String()
```

with:

```go
				// Actual block proposer, resolved once; txProp is the same value,
				// meaningful only when the block carries transactions.
				hasTx := len(b.Block.Data.Txs) > 0
				proposerAddr := b.Block.Header.ProposerAddress.String()
				txProp := ""
				if hasTx {
					txProp = proposerAddr
				}
```

- [ ] **Step 4: Build, vet, and run the collector tests**

Run: `go build ./... && go vet ./internal/gnovalidator/... && go test ./internal/gnovalidator/... -v`
Expected: PASS — `proposerAddr`, `txProposer`/`txProp`, `Proposed`, and `TxContribution` produce the same values as before; aggregator/backfill tests green.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/gnovalidator/gnovalidator_realtime.go backend/internal/gnovalidator/sync.go
git commit -m "refactor(collect): resolve block proposer once per block"
```

## Task 7: Use the batch VP upsert in `InitMonikerMap`

**Files:**
- Modify: `backend/internal/gnovalidator/valoper.go:425-437`

**Interfaces:**
- Consumes: `database.AddrVP`, `database.UpsertAddrMonikerVPBatch` (Task 3)

- [ ] **Step 1: Replace the per-validator upsert loop with a batch**

In `valoper.go`, replace:

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

with:

```go
	// Persist current voting power for score severity weighting (best-effort),
	// in a single batched upsert rather than one round-trip per validator.
	vpRows := make([]database.AddrVP, 0, len(validatorsResp.Result.Validators))
	for _, val := range validatorsResp.Result.Validators {
		if val.VotingPower == "" {
			continue
		}
		vp, err := strconv.ParseInt(val.VotingPower, 10, 64)
		if err != nil {
			continue
		}
		vpRows = append(vpRows, database.AddrVP{Addr: val.Address, VotingPower: vp})
	}
	if err := database.UpsertAddrMonikerVPBatch(db, chainID, vpRows); err != nil {
		log.Printf("[valoper][%s] failed to persist voting power batch: %v", chainID, err)
	}
```

- [ ] **Step 2: Build and vet**

Run: `go build ./... && go vet ./internal/gnovalidator/...`
Expected: no errors. (`strconv` and `log` remain used; `database.UpsertAddrMonikerVP` is still referenced by its own tests, so no dead code.)

- [ ] **Step 3: Commit**

```bash
git add backend/internal/gnovalidator/valoper.go
git commit -m "perf(valoper): persist voting power in one batched upsert per refresh"
```

---

# PHASE C — API

## Task 8: Collapse the handler's parallel maps into one

Replace `inputs` + `monikers` + the `ensure` closure with a single `merged` map built by a helper. Behavior unchanged.

**Files:**
- Modify: `backend/internal/api/api_report.go`

**Interfaces:**
- Consumes: `database.ParticipationRaw`, `database.ValidatorScoreRaw`, `score.Inputs`

- [ ] **Step 1: Add the `merged` type and merge helper**

In `api_report.go`, above `GetValidatorReportHandler`, add:

```go
// merged carries one validator's score inputs plus the moniker discovered while
// joining participation and alert rows for a period.
type merged struct {
	in      score.Inputs
	moniker string
}

// mergeParticipationAndAlerts joins per-validator participation and alert rows
// into one map keyed by address, honoring an optional address filter. Moniker is
// taken from alert rows (participation rows carry no moniker here).
func mergeParticipationAndAlerts(partRows []database.ParticipationRaw, alertRows []database.ValidatorScoreRaw, addrFilter string) map[string]*merged {
	out := map[string]*merged{}
	ensure := func(addr string) *merged {
		m, ok := out[addr]
		if !ok {
			m = &merged{}
			out[addr] = m
		}
		return m
	}
	for _, p := range partRows {
		if addrFilter != "" && p.Addr != addrFilter {
			continue
		}
		m := ensure(p.Addr)
		m.in.SignedBlocks = p.SignedBlocks
		m.in.TotalBlocks = p.TotalBlocks
		m.in.ProposedBlocks = p.ProposedBlocks
	}
	for _, a := range alertRows {
		if addrFilter != "" && a.Addr != addrFilter {
			continue
		}
		m := ensure(a.Addr)
		m.in.CriticalCount = a.CriticalCount
		m.in.WarningCount = a.WarningCount
		m.in.DowntimeBlocks = a.DowntimeBlocks
		if a.Moniker != "" {
			m.moniker = a.Moniker
		}
	}
	return out
}
```

- [ ] **Step 2: Replace the per-period merge/compute block**

In the `for _, period := range reportPeriods` loop, replace everything from the `// Merge both sources into one Inputs per addr.` comment through the end of the `for _, addr := range addrs { ... }` loop with:

```go
		byAddrMerged := mergeParticipationAndAlerts(partRows, alertRows, addrFilter)

		addrs := make([]string, 0, len(byAddrMerged))
		for addr := range byAddrMerged {
			addrs = append(addrs, addr)
		}
		sort.Strings(addrs)

		for _, addr := range addrs {
			m := byAddrMerged[addr]
			in := m.in
			in.VotingPower = vpByAddr[addr]
			in.SumVotingPower = vpSum
			in.MaxVotingPower = vpMax
			in.ChainBlocks = chainBlocks

			rep, ok := byAddr[addr]
			if !ok {
				rep = &validatorReport{Addr: addr, Moniker: m.moniker, Periods: map[string]periodScore{}}
				byAddr[addr] = rep
				order = append(order, addr)
			}
			if rep.Moniker == "" {
				rep.Moniker = m.moniker
			}

			res := score.Compute(in, weights)
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
```

- [ ] **Step 3: Build, vet, and run the API tests**

Run: `go build ./... && go vet ./internal/api/... && go test ./internal/api/... -v`
Expected: PASS — `TestGetValidatorReportHandler`, `TestGetValidatorReportHandlerAlertOnlyNoParticipation`, and `TestGetValidatorReportHandlerInvalidChain` all green; identical scores/monikers.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/api_report.go
git commit -m "refactor(api): merge participation and alerts into a single map"
```

---

## Final Verification

- [ ] **Full backend suite**

Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: all PASS (Postgres reachable per Global Constraints).

- [ ] **Panel build (unchanged, sanity only)**

Run: `cd panel && npm run build`
Expected: build succeeds.

- [ ] **Confirm behavior parity**

The whole point is zero behavior change: every pre-existing test in `score`, `database`, `api`, and `gnovalidator` remains green with no assertion values edited (only call-site signatures in Task 2 and the retargeted chain-blocks test). If any pre-existing assertion had to change its expected value, stop — that signals a behavior regression, not a refactor.

---

## Notes / Out of Scope

- **Indexes (finding #7b):** deliberately no change. `addr_monikers` is one row per validator; `proposed`/`proposed_count` reads ride existing `(chain_id, date)` / `(chain_id, block_date)` indexes. Recorded as a decision, not an oversight.
- **Accepted score-model limitations** (current-VP snapshot on historical periods; sub-1h aggregator-lag seam) are documented in the score-v2 plan and untouched here.
