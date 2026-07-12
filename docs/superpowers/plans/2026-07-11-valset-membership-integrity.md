# Valset Membership Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop validators that never (or no longer) belong to the valset from polluting `/api/reports/validators`, and add real-time alerts (on all existing broadcast channels) when a validator leaves the valset or rotates its signing address.

**Architecture:** Four independent-but-related fixes in the `gnovalidator`/`database` packages: (1) a one-line SQL filter fix, (2) a shared, thread-safe guard replacing a stale local snapshot in the backfill path, (3) turning the in-memory `MonikerMap` into a true per-cycle snapshot instead of an accumulator, and (4) a departure/rotation classifier plus a live-computed (no schema change) retroactive cleanup query. All new alerts reuse the existing `internal.SendInfoValidator` broadcast function (Discord + Slack + Telegram in one call).

**Tech Stack:** Go, GORM (Postgres via `jackc/pgx/v5`), `testify/require`, `testoutils.NewTestDB` for Postgres-backed tests.

## Global Constraints

- English only for all comments, docs, commit messages (per CLAUDE.md).
- Every query stays scoped by `chain_id`.
- No change to `score.Compute` or its weights.
- `go vet ./...` and `go test ./...` must pass (Postgres test DB required — see CLAUDE.md; `testoutils.NewTestDB(t)` handles schema isolation automatically).
- Commands below assume the working directory is `backend/`.
- Branch: create `fix/valset-membership-integrity` off `main` before Task 1.

---

## Task 1: `GetValidatorScores` excludes `addr='all'`

**Files:**
- Modify: `backend/internal/database/db_score.go:252-280` (`GetValidatorScores`)
- Test: `backend/internal/database/db_score_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: no signature change to `GetValidatorScores(db *gorm.DB, chainID, period string) ([]ValidatorScoreRaw, error)`.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/database/db_score_test.go` (uses the existing `seedScoreAlert` helper already defined in that file):

```go
func TestGetValidatorScoresExcludesAddrAll(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	inMonth := time.Date(now.Year(), now.Month(), 2, 12, 0, 0, 0, time.UTC)

	seedScoreAlert(t, db, "test12", "g1aaa", "CRITICAL", 100, 130, inMonth)
	// Chain-wide "blockchain stuck" alert — must never appear as a fake validator.
	seedScoreAlert(t, db, "test12", "all", "CRITICAL", 1, 999, inMonth)

	rows, err := database.GetValidatorScores(db, "test12", "current_month")
	if err != nil {
		t.Fatalf("GetValidatorScores: %v", err)
	}
	for _, r := range rows {
		if r.Addr == "all" {
			t.Fatalf("addr='all' must be excluded from GetValidatorScores, got: %+v", rows)
		}
	}
	if len(rows) != 1 || rows[0].Addr != "g1aaa" {
		t.Fatalf("want exactly [g1aaa], got %+v", rows)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/database/... -run TestGetValidatorScoresExcludesAddrAll -v`
Expected: FAIL — the assertion `len(rows) != 1` trips because `all` is currently included (`rows` has 2 entries).

- [ ] **Step 3: Write minimal implementation**

In `backend/internal/database/db_score.go`, in `GetValidatorScores`, change the `WHERE` clause:

```go
	err = db.Raw(`
		SELECT al.addr AS addr,
		       COALESCE(MAX(am.moniker), MAX(al.moniker), '') AS moniker,
		       COUNT(*) FILTER (WHERE al.level = 'CRITICAL') AS critical_count,
		       COUNT(*) FILTER (WHERE al.level = 'WARNING')  AS warning_count,
		       COALESCE(SUM(
		           CASE WHEN al.level = 'CRITICAL' AND al.end_height > al.start_height
		                THEN al.end_height - al.start_height ELSE 0 END
		       ), 0) AS downtime_blocks
		FROM alert_logs al
		LEFT JOIN addr_monikers am ON am.chain_id = al.chain_id AND am.addr = al.addr
		WHERE al.chain_id = ?
		  AND al.level IN ('CRITICAL','WARNING')
		  AND al.addr <> 'all'
		  AND al.sent_at >= ? AND al.sent_at < ?
		GROUP BY al.addr
		ORDER BY al.addr
	`, chainID, start, end).Scan(&rows).Error
```

(the only change is the added `AND al.addr <> 'all'` line, mirroring `GetLastAlertTimes` just above it in the same file).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/database/... -run TestGetValidatorScoresExcludesAddrAll -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite**

Run: `go test ./internal/database/... -run TestGetValidatorScores -v`
Expected: PASS (both `TestGetValidatorScoresCurrentMonth`, `TestGetValidatorScoresInvalidPeriod`, and the new test all pass — confirms no regression on the existing behavior).

- [ ] **Step 6: Commit**

```bash
git add internal/database/db_score.go internal/database/db_score_test.go
git commit -m "fix(score): exclude addr='all' from GetValidatorScores

alert_logs carries chain-wide 'blockchain stuck' rows under the sentinel
address 'all'. GetLastAlertTimes already excludes them; GetValidatorScores
did not, so 'all' showed up in /api/reports/validators as a fake
validator scoring 0 (no participation rows exist for it)."
```

---

## Task 2: `BackfillParallel` stops inventing pre-activation history

**Context for the implementer:** `BackfillParallel` (`sync.go`) currently reads `firstActiveBlocks := GetFirstActiveBlockMap(chainID)` **once** before spawning 20 concurrent workers (`sync.go:171`), then every worker reads/writes only that frozen local copy (`sync.go:204`, `sync.go:208`) — never the live, thread-safe `FirstActiveBlockMap` behind `GetFirstActiveBlock`/`SetFirstActiveBlock` (`gnovalidator_realtime.go:25-42`). Because nothing ever updates the local snapshot, a validator whose real first activation falls inside the backfilled range gets a `participated=false` row for every block before the point where some worker happens to process its real activation — including blocks that predate the validator's real existence. `BackfillRange` (the sequential counterpart) has no caller anywhere in the codebase (confirmed via `grep -rn "BackfillRange("` — zero matches outside its own definition); it is dead code and is intentionally left untouched by this task to keep the diff focused on the function that's actually invoked in production.

**Files:**
- Modify: `backend/internal/gnovalidator/sync.go` (add `RecordActivationOrSkip`; rewrite `BackfillParallel`'s guard at lines 171, 199-211 to use it)
- Test: `backend/internal/gnovalidator/sync_test.go` (new file)

**Interfaces:**
- Consumes: `GetFirstActiveBlock(chainID, addr string) int64`, `SetFirstActiveBlock(chainID, addr string, block int64)` (both already exported in `gnovalidator_realtime.go:25-42`); `database.UpsertFirstActiveBlock(db *gorm.DB, chainID, addr string, block int64) error` (already exported in `internal/database/db.go:341`).
- Produces: `func RecordActivationOrSkip(db *gorm.DB, chainID, addr string, height int64, participated bool) bool` — returns `true` when the caller should skip writing a row for this `(chainID, addr, height)` because it predates the validator's recorded first activation.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/gnovalidator/sync_test.go`:

```go
package gnovalidator_test

import (
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/require"
)

func TestRecordActivationOrSkip(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chainID := "test-record-activation"
	addr := "g1neveractivated"

	// Before any true activation is ever recorded, a participated=false row
	// must be skipped (this is the guard that stops phantom pre-join history).
	require.True(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 10, false))

	// A true participation at height 30 records the activation and must
	// never itself be skipped.
	require.False(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 30, true))
	require.Equal(t, int64(30), gnovalidator.GetFirstActiveBlock(chainID, addr))

	// A height BEFORE the recorded activation, evaluated AFTER the
	// activation was recorded (simulating an out-of-order concurrent
	// backfill worker), is now correctly skipped — this is the exact
	// scenario the old frozen-snapshot code got wrong.
	require.True(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 15, false))

	// A height AFTER activation that still didn't participate is a real
	// miss and must NOT be skipped.
	require.False(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 31, false))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gnovalidator/... -run TestRecordActivationOrSkip -v`
Expected: FAIL with `undefined: gnovalidator.RecordActivationOrSkip`

- [ ] **Step 3: Write minimal implementation**

In `backend/internal/gnovalidator/sync.go`, add this function after the `out` type definition (currently ends around line 26) and before `flushBatch`:

```go
// RecordActivationOrSkip is the shared first-activation guard used while
// writing daily_participations rows. It reads and writes the live,
// thread-safe FirstActiveBlockMap (GetFirstActiveBlock/SetFirstActiveBlock)
// rather than a point-in-time snapshot, so concurrent backfill workers
// observe each other's discoveries immediately instead of each working off
// a copy that's stale for the whole run. Returns true when the caller
// should skip writing a row for this (chainID, addr, height) because it
// predates the validator's first recorded activation.
func RecordActivationOrSkip(db *gorm.DB, chainID, addr string, height int64, participated bool) bool {
	if participated {
		if GetFirstActiveBlock(chainID, addr) == -1 {
			SetFirstActiveBlock(chainID, addr, height)
			_ = database.UpsertFirstActiveBlock(db, chainID, addr, height)
		}
		return false
	}
	if fab := GetFirstActiveBlock(chainID, addr); fab > 0 && height < fab {
		return true
	}
	return false
}
```

Then rewrite `BackfillParallel`'s worker body. Remove the frozen snapshot at line 171:

```go
	firstActiveBlocks := GetFirstActiveBlockMap(chainID)
```

Replace the guard block at lines 199-211:

```go
				rows := make([]dpRow, 0, len(monikerMap))
				for addr, mon := range monikerMap {
					p := participating[addr] // zero value (not participated/proposed) if absent
					if p.Participated {
						// Dynamic detection: record first_active_block when first seen
						if fab := firstActiveBlocks[addr]; fab == -1 {
							SetFirstActiveBlock(chainID, addr, j.H)
							_ = database.UpsertFirstActiveBlock(db, chainID, addr, j.H)
						}
					} else if fab := firstActiveBlocks[addr]; fab > 0 && j.H < fab {
						// Guard: skip rows before the validator's activation block
						continue
					}
					rows = append(rows, dpRow{
```

with:

```go
				rows := make([]dpRow, 0, len(monikerMap))
				for addr, mon := range monikerMap {
					p := participating[addr] // zero value (not participated/proposed) if absent
					if RecordActivationOrSkip(db, chainID, addr, j.H, p.Participated) {
						continue
					}
					rows = append(rows, dpRow{
```

(the rest of the `dpRow{...}` literal and the closing of the loop/worker are unchanged).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gnovalidator/... -run TestRecordActivationOrSkip -v`
Expected: PASS

- [ ] **Step 5: Run the package build and existing tests**

Run: `go build ./... && go test ./internal/gnovalidator/... -v`
Expected: build succeeds, all existing tests still pass (in particular any test touching `BackfillParallel`, `flushBatch`, `flushChunk` — confirm no compile errors from the removed `firstActiveBlocks` variable).

- [ ] **Step 6: Commit**

```bash
git add internal/gnovalidator/sync.go internal/gnovalidator/sync_test.go
git commit -m "fix(backfill): stop inventing pre-activation history in BackfillParallel

BackfillParallel read a frozen local snapshot of first_active_block once
before spawning 20 concurrent workers, so a validator's real first
activation discovered mid-run was never reflected back into that
snapshot — every block before whichever worker happened to observe the
real activation got a phantom participated=false row, including blocks
that predate the validator's real existence. Extract the guard into
RecordActivationOrSkip, backed by the already-thread-safe
GetFirstActiveBlock/SetFirstActiveBlock, so workers see each other's
discoveries immediately."
```

---

## Task 3: `InitMonikerMap` becomes a true snapshot

**Context for the implementer:** `MonikerMap` (`gnovalidator_realtime.go:17-18`) is meant to represent "validators currently in the valset," but `InitMonikerMap` (`valoper.go:331`) only ever *adds* keys to it via the per-address `SetMoniker` call — nothing ever removes an address once it drops out of the live `/validators` response. `SaveParticipation` (`gnovalidator_realtime.go:560`) iterates this ever-growing map on every block, so a validator that has genuinely left the valset keeps accumulating `participated=false` rows forever. This task makes `InitMonikerMap` replace the per-chain map wholesale each refresh cycle instead of merging into it, and also has it return the valoper registry it already fetched internally (`GetValopers`), so the next task can use it without a second RPC round-trip.

**Files:**
- Modify: `backend/internal/gnovalidator/gnovalidator_realtime.go` (add `ReplaceMonikerMap` near `SetMoniker`/`GetMonikerMap`)
- Modify: `backend/internal/gnovalidator/valoper.go:331,421-423` (`InitMonikerMap` signature + snapshot replace)
- Test: `backend/internal/gnovalidator/gnovalidator_realtime_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func ReplaceMonikerMap(chainID string, m map[string]string)` (new, `gnovalidator_realtime.go`); `func InitMonikerMap(db *gorm.DB, chainID string, client gnoclient.Client, chainCfg *internal.ChainConfig) []Valoper` (signature change: now returns the valoper registry fetched this cycle — `nil` on any early-return error path, matching the function's existing best-effort error handling).

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/gnovalidator/gnovalidator_realtime_test.go`:

```go
func TestReplaceMonikerMap(t *testing.T) {
	chainID := "test-replace-moniker"

	gnovalidator.ReplaceMonikerMap(chainID, map[string]string{
		"g1old": "Old Validator",
		"g1stay": "Still Here",
	})
	require.Equal(t, "Old Validator", gnovalidator.GetMonikerMap(chainID)["g1old"])

	// A second replace with a different set must DROP g1old entirely, not
	// merge it in — this is the behavior that stops SaveParticipation from
	// writing rows for validators that have left the valset forever.
	gnovalidator.ReplaceMonikerMap(chainID, map[string]string{
		"g1stay": "Still Here",
		"g1new":  "New Validator",
	})
	got := gnovalidator.GetMonikerMap(chainID)
	require.Len(t, got, 2)
	require.Equal(t, "Still Here", got["g1stay"])
	require.Equal(t, "New Validator", got["g1new"])
	_, stillPresent := got["g1old"]
	require.False(t, stillPresent, "g1old must be pruned after ReplaceMonikerMap, not accumulated")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gnovalidator/... -run TestReplaceMonikerMap -v`
Expected: FAIL with `undefined: gnovalidator.ReplaceMonikerMap`

- [ ] **Step 3: Write minimal implementation**

In `backend/internal/gnovalidator/gnovalidator_realtime.go`, add this function right after `SetMoniker` (currently ends at line 638):

```go
// ReplaceMonikerMap atomically replaces the entire per-chain moniker map with
// m, instead of merging into it. Used by InitMonikerMap so MonikerMap always
// reflects exactly the validators currently in the live valset — a validator
// that drops out of /validators is dropped from this map on the very next
// refresh cycle, instead of lingering forever (which used to make
// SaveParticipation keep writing participated=false rows for it long after
// it left).
func ReplaceMonikerMap(chainID string, m map[string]string) {
	MonikerMutex.Lock()
	defer MonikerMutex.Unlock()
	MonikerMap[chainID] = m
}
```

In `backend/internal/gnovalidator/valoper.go`, change the function signature at line 331:

```go
func InitMonikerMap(db *gorm.DB, chainID string, client gnoclient.Client, chainCfg *internal.ChainConfig) []Valoper {
```

Change each of the 5 early-return blocks before `valopers` is populated (lines 349-373) to `return nil`, since the function now has a return type. Each occurrence, with enough surrounding context to locate it precisely (do not touch `return` statements in any other function in this file):

```go
	if err != nil {
		log.Printf("[valoper][%s] failed to retrieve validators after retries: %v", chainID, err)
		return nil
	}
```

```go
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading validator response: %v", err)
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("Invalid HTTP status %d from /validators: %s", resp.StatusCode, string(body))
		return nil
	}

	if !json.Valid(body) {
		log.Printf("Invalid JSON received from /validators:\n%s", string(body))
		return nil
	}
	var validatorsResp ValidatorsResponse
	if err := json.Unmarshal(body, &validatorsResp); err != nil {
		log.Printf("Error decoding validator JSON: %v\nRaw body: %s", err, string(body))
		return nil
	}
```

(the log message text itself is unchanged — only the `return` → `return nil` matters; the snippets above may render the leading emoji differently than the source file, match by the surrounding structure, not the exact emoji bytes)

Replace the merge loop at lines 421-423:

```go
	for addr, moniker := range tempMonikers {
		SetMoniker(chainID, addr, moniker)
	}
```

with:

```go
	ReplaceMonikerMap(chainID, tempMonikers)
```

Finally, add `return valopers` as the last line of the function (after the existing final `log.Printf("[valoper][%s] moniker sync: %d resolved persisted, %d unresolved (not persisted)", chainID, resolved, unresolved)` statement, right before the function's closing `}`).

- [ ] **Step 4: Update both call sites so the package still compiles**

`backend/internal/gnovalidator/gnovalidator_realtime.go:334` (inside `WatchNewValidators`) and `:609` (inside `StartValidatorMonitoring`) both currently call `InitMonikerMap(db, chainID, client, chainCfg)` as a bare statement. Go permits discarding a function's return value when it's called as a statement, so **line 609 needs no change**. Leave line 334 as a bare statement for now too — Task 4 will change it to capture the returned `[]Valoper`.

- [ ] **Step 5: Run test to verify it passes, and confirm the package builds**

Run: `go build ./... && go test ./internal/gnovalidator/... -run TestReplaceMonikerMap -v`
Expected: build succeeds, test PASSes.

- [ ] **Step 6: Run the full gnovalidator test suite**

Run: `go test ./internal/gnovalidator/... -v`
Expected: all tests pass, including `TestMultiChain_MonikerMapIsolation` in `multichain_integration_test.go` (which calls `SetMoniker` directly, untouched by this change) and everything from Task 2.

- [ ] **Step 7: Commit**

```bash
git add internal/gnovalidator/gnovalidator_realtime.go internal/gnovalidator/valoper.go internal/gnovalidator/gnovalidator_realtime_test.go
git commit -m "fix(valoper): MonikerMap is a true snapshot, not an accumulator

InitMonikerMap only ever added keys to MonikerMap via SetMoniker; nothing
removed an address once it dropped out of the live /validators response,
so SaveParticipation kept writing participated=false rows forever for
validators that had genuinely left the valset. Replace the whole
per-chain map each refresh cycle via the new ReplaceMonikerMap instead of
merging into it. InitMonikerMap now also returns the valoper registry it
already fetches internally, for the departure/rotation detection in the
next commit."
```

---

## Task 4: Detect departures and correlate address rotation

**Context for the implementer:** `WatchNewValidators` (`gnovalidator_realtime.go:319-349`) currently only detects *new* validators (an address present after `InitMonikerMap` runs that wasn't present before) and sends a "New Validator detected" notice via `internal.SendInfoValidator`. This task adds the symmetric detection for validators that *left*, and — using the valoper registry's stable operator `Address` vs. rotatable `SigningAddress` (`valoper.go:25-37`) — collapses a signing-key rotation (old address leaves, new address arrives, both belonging to the same operator) into a single "address changed" event instead of two unrelated-looking notices.

New pure logic lives in a new file, `valset_changes.go`, so it's testable without any RPC mocking (per the design spec's requirement that this classification be RPC-free).

**Files:**
- Create: `backend/internal/gnovalidator/valset_changes.go`
- Test: `backend/internal/gnovalidator/valset_changes_test.go` (new file)
- Modify: `backend/internal/gnovalidator/gnovalidator_realtime.go:319-349` (`WatchNewValidators` body)

**Interfaces:**
- Consumes: `Valoper{Name, Address, SigningAddress string}` (already exported, `valoper.go:25-37`); `internal.SendInfoValidator(chainID, msg, level string, db *gorm.DB) error` (already used elsewhere in this file).
- Produces: `type ValsetChangeKind int` with constants `ValidatorJoined`, `ValidatorLeft`, `ValidatorAddressChanged`; `type ValsetChangeEvent struct { Kind ValsetChangeKind; Moniker, OldAddr, NewAddr string }`; `func classifyValsetChanges(oldMap, newMap map[string]string, prevSigningToOperator map[string]string, currentValopers []Valoper) []ValsetChangeEvent`; `func signingToOperatorFromValopers(valopers []Valoper) map[string]string`; `func getSigningToOperator(chainID string) map[string]string`; `func setSigningToOperator(chainID string, m map[string]string)`.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/gnovalidator/valset_changes_test.go`:

```go
package gnovalidator

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// sortEvents gives deterministic ordering for assertions, independent of the
// map-iteration order inside classifyValsetChanges.
func sortEvents(events []ValsetChangeEvent) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].Kind != events[j].Kind {
			return events[i].Kind < events[j].Kind
		}
		return events[i].OldAddr+events[i].NewAddr < events[j].OldAddr+events[j].NewAddr
	})
}

func TestClassifyValsetChanges_MatchedRotation(t *testing.T) {
	oldMap := map[string]string{"g1old": "Validaria"}
	newMap := map[string]string{"g1new": "Validaria"}
	prevSigningToOperator := map[string]string{"g1old": "g1operator"}
	currentValopers := []Valoper{
		{Name: "Validaria", Address: "g1operator", SigningAddress: "g1new"},
	}

	events := classifyValsetChanges(oldMap, newMap, prevSigningToOperator, currentValopers)
	require.Len(t, events, 1)
	require.Equal(t, ValidatorAddressChanged, events[0].Kind)
	require.Equal(t, "g1old", events[0].OldAddr)
	require.Equal(t, "g1new", events[0].NewAddr)
	require.Equal(t, "Validaria", events[0].Moniker)
}

func TestClassifyValsetChanges_UnmatchedDeparture(t *testing.T) {
	oldMap := map[string]string{"g1gone": "Ghostly"}
	newMap := map[string]string{}
	// No valoper profile ties g1gone to any operator that rotated.
	events := classifyValsetChanges(oldMap, newMap, map[string]string{}, nil)
	require.Len(t, events, 1)
	require.Equal(t, ValidatorLeft, events[0].Kind)
	require.Equal(t, "g1gone", events[0].OldAddr)
	require.Equal(t, "Ghostly", events[0].Moniker)
}

func TestClassifyValsetChanges_UnmatchedArrival(t *testing.T) {
	oldMap := map[string]string{}
	newMap := map[string]string{"g1fresh": "Freshling"}
	events := classifyValsetChanges(oldMap, newMap, map[string]string{}, nil)
	require.Len(t, events, 1)
	require.Equal(t, ValidatorJoined, events[0].Kind)
	require.Equal(t, "g1fresh", events[0].NewAddr)
	require.Equal(t, "Freshling", events[0].Moniker)
}

func TestClassifyValsetChanges_UnrelatedDepartureAndArrival(t *testing.T) {
	oldMap := map[string]string{"g1left": "Leaver"}
	newMap := map[string]string{"g1joined": "Joiner"}
	// g1left's operator either isn't found, or its current signing address
	// isn't g1joined — no rotation match should be made.
	prevSigningToOperator := map[string]string{"g1left": "g1operatorA"}
	currentValopers := []Valoper{
		{Name: "Leaver", Address: "g1operatorA", SigningAddress: "g1left"}, // did not rotate
		{Name: "Joiner", Address: "g1operatorB", SigningAddress: "g1joined"},
	}

	events := classifyValsetChanges(oldMap, newMap, prevSigningToOperator, currentValopers)
	sortEvents(events)
	require.Len(t, events, 2)
	require.Equal(t, ValidatorLeft, events[0].Kind)
	require.Equal(t, "g1left", events[0].OldAddr)
	require.Equal(t, ValidatorJoined, events[1].Kind)
	require.Equal(t, "g1joined", events[1].NewAddr)
}

func TestSigningToOperatorFromValopers(t *testing.T) {
	got := signingToOperatorFromValopers([]Valoper{
		{Name: "A", Address: "g1opA", SigningAddress: "g1signA"},
		{Name: "B", Address: "g1opB", SigningAddress: ""}, // no signing address declared yet — skipped
	})
	require.Equal(t, map[string]string{"g1signA": "g1opA"}, got)
}

func TestSigningToOperatorState(t *testing.T) {
	chainID := "test-signing-to-operator-state"
	require.Empty(t, getSigningToOperator(chainID))

	setSigningToOperator(chainID, map[string]string{"g1sign": "g1op"})
	require.Equal(t, map[string]string{"g1sign": "g1op"}, getSigningToOperator(chainID))
}
```

Note this test file uses `package gnovalidator` (internal test package, not `gnovalidator_test`) because `classifyValsetChanges`, `signingToOperatorFromValopers`, `getSigningToOperator`, and `setSigningToOperator` are unexported — matching the design spec's requirement that this classification logic be pure and independently testable without RPC mocking.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gnovalidator/... -run TestClassifyValsetChanges -v` and `go test ./internal/gnovalidator/... -run TestSigningToOperator -v`
Expected: FAIL with compile errors (`undefined: ValsetChangeEvent`, etc.) — the new file doesn't exist yet.

- [ ] **Step 3: Write minimal implementation**

Create `backend/internal/gnovalidator/valset_changes.go`:

```go
package gnovalidator

import "sync"

// ValsetChangeKind classifies one detected valset membership change.
type ValsetChangeKind int

const (
	ValidatorJoined ValsetChangeKind = iota
	ValidatorLeft
	ValidatorAddressChanged
)

// ValsetChangeEvent describes one detected change between two consecutive
// MonikerMap snapshots, already correlated against the valoper registry to
// distinguish a signing-key rotation from an unrelated departure/arrival.
type ValsetChangeEvent struct {
	Kind    ValsetChangeKind
	Moniker string
	OldAddr string // set for ValidatorLeft and ValidatorAddressChanged
	NewAddr string // set for ValidatorJoined and ValidatorAddressChanged
}

// signingToOperatorMap[chainID][signingAddr] = operatorAddr, as observed the
// last time classifyValsetChanges ran for that chain. Mirrors the
// MonikerMap/FirstActiveBlockMap package-level state pattern.
var signingToOperatorMap = make(map[string]map[string]string)
var signingToOperatorMutex sync.RWMutex

// getSigningToOperator returns a snapshot of the previous cycle's
// signing-address -> operator-address map for chainID (empty if never set).
func getSigningToOperator(chainID string) map[string]string {
	signingToOperatorMutex.RLock()
	defer signingToOperatorMutex.RUnlock()
	m, ok := signingToOperatorMap[chainID]
	if !ok {
		return make(map[string]string)
	}
	snapshot := make(map[string]string, len(m))
	for k, v := range m {
		snapshot[k] = v
	}
	return snapshot
}

// setSigningToOperator replaces the per-chain signing-address -> operator
// map, to be read back by the next cycle's classifyValsetChanges call.
func setSigningToOperator(chainID string, m map[string]string) {
	signingToOperatorMutex.Lock()
	defer signingToOperatorMutex.Unlock()
	signingToOperatorMap[chainID] = m
}

// signingToOperatorFromValopers builds a signing-address -> operator-address
// map from a valoper registry snapshot (skipping profiles with no declared
// signing address), for use as the next cycle's prevSigningToOperator input
// to classifyValsetChanges.
func signingToOperatorFromValopers(valopers []Valoper) map[string]string {
	m := make(map[string]string, len(valopers))
	for _, v := range valopers {
		if v.SigningAddress != "" {
			m[v.SigningAddress] = v.Address
		}
	}
	return m
}

// classifyValsetChanges compares oldMap (the moniker snapshot before this
// refresh cycle) with newMap (the snapshot after), and correlates any
// departures with arrivals via prevSigningToOperator (each now-removed
// signing address's operator, as observed in the PREVIOUS cycle) and
// currentValopers (this cycle's freshly fetched valoper registry, giving
// each operator's up-to-date declared signing address).
//
// A departed address whose operator's current signing address is one of
// this cycle's new arrivals is reported as a single ValidatorAddressChanged
// event instead of a separate ValidatorLeft + ValidatorJoined pair.
func classifyValsetChanges(
	oldMap, newMap map[string]string,
	prevSigningToOperator map[string]string,
	currentValopers []Valoper,
) []ValsetChangeEvent {
	removed := make(map[string]bool)
	for addr := range oldMap {
		if _, ok := newMap[addr]; !ok {
			removed[addr] = true
		}
	}
	added := make(map[string]bool)
	for addr := range newMap {
		if _, ok := oldMap[addr]; !ok {
			added[addr] = true
		}
	}

	operatorCurrentSigning := make(map[string]string, len(currentValopers))
	for _, v := range currentValopers {
		if v.SigningAddress != "" {
			operatorCurrentSigning[v.Address] = v.SigningAddress
		}
	}

	var events []ValsetChangeEvent
	matchedArrival := make(map[string]bool)

	for r := range removed {
		if operator, ok := prevSigningToOperator[r]; ok {
			if newSigning, ok := operatorCurrentSigning[operator]; ok && newSigning != r && added[newSigning] {
				events = append(events, ValsetChangeEvent{
					Kind:    ValidatorAddressChanged,
					Moniker: oldMap[r],
					OldAddr: r,
					NewAddr: newSigning,
				})
				matchedArrival[newSigning] = true
				continue
			}
		}
		events = append(events, ValsetChangeEvent{
			Kind:    ValidatorLeft,
			Moniker: oldMap[r],
			OldAddr: r,
		})
	}

	for a := range added {
		if matchedArrival[a] {
			continue
		}
		events = append(events, ValsetChangeEvent{
			Kind:    ValidatorJoined,
			Moniker: newMap[a],
			NewAddr: a,
		})
	}

	return events
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gnovalidator/... -run "TestClassifyValsetChanges|TestSigningToOperator" -v`
Expected: PASS (all 6 test functions).

- [ ] **Step 5: Wire the classifier into `WatchNewValidators`**

Replace the whole body of `WatchNewValidators` in `backend/internal/gnovalidator/gnovalidator_realtime.go:319-349`:

```go
func WatchNewValidators(ctx context.Context, db *gorm.DB, chainID string, client gnoclient.Client, chainCfg *internal.ChainConfig, refreshInterval time.Duration) {
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Printf("[monitor][%s] WatchNewValidators stopped", chainID)
				return
			case <-ticker.C:
				oldMap := GetMonikerMap(chainID)
				prevSigningToOperator := getSigningToOperator(chainID)

				valopers := InitMonikerMap(db, chainID, client, chainCfg)

				newMap := GetMonikerMap(chainID)
				setSigningToOperator(chainID, signingToOperatorFromValopers(valopers))

				for _, ev := range classifyValsetChanges(oldMap, newMap, prevSigningToOperator, valopers) {
					var msg string
					switch ev.Kind {
					case ValidatorJoined:
						msg = fmt.Sprintf("[%s] ✅ **New Validator detected**: %s (%s)", chainID, ev.Moniker, ev.NewAddr)
					case ValidatorLeft:
						msg = fmt.Sprintf("[%s] ⚠️ **Validator left the valset**: %s (%s)", chainID, ev.Moniker, ev.OldAddr)
					case ValidatorAddressChanged:
						msg = fmt.Sprintf("[%s] 🔄 **Validator address changed**: %s (%s → %s)", chainID, ev.Moniker, ev.OldAddr, ev.NewAddr)
					}
					log.Println(msg)
					if err := internal.SendInfoValidator(chainID, msg, "info", db); err != nil {
						log.Printf("[monitor][%s] SendInfoValidator error: %v", chainID, err)
					}
				}
			}
		}
	}()
}
```

- [ ] **Step 6: Run the full package build and test suite**

Run: `go build ./... && go vet ./... && go test ./internal/gnovalidator/... -v`
Expected: build and vet succeed; all tests pass, including Tasks 1-3's tests and the new ones from this task.

- [ ] **Step 7: Commit**

```bash
git add internal/gnovalidator/valset_changes.go internal/gnovalidator/valset_changes_test.go internal/gnovalidator/gnovalidator_realtime.go
git commit -m "feat(valoper): alert on valset departure and signing-key rotation

WatchNewValidators only ever detected new validators. Add the symmetric
departure diff, and correlate a departure with an arrival via the
valoper registry's stable operator Address vs. rotatable SigningAddress
so a signing-key rotation produces one clear 'address changed' alert
instead of two unrelated-looking 'left'/'joined' notices. All three
alert kinds broadcast through the existing SendInfoValidator path
(Discord + Slack + Telegram)."
```

---

## Task 5: Retroactive cleanup for trailing post-departure ghost rows

**Context for the implementer:** Before Task 3, an address that left the valset kept accumulating `participated=false` rows in `daily_participations` forever (`MonikerMap` never pruned it, so `SaveParticipation` kept "seeing" it every block). Task 3 stops *new* phantom rows going forward, but rows already written before this code shipped (e.g. a validator's old signing address from a rotation that already happened) are still sitting in the database and still degrade `current_year`/`current_month` scores. This task adds a cleanup that deletes those trailing rows.

Unlike `first_active_block` (persisted in `addr_monikers` because it's read on *every block, for every validator* in the hot path — `SaveParticipation`, `BackfillParallel`), the "last true participation" value needed here has no hot-path reader: after Task 3, `MonikerMap` itself is what stops future writes the moment a departure is detected. So this value is computed live via a correlated subquery at cleanup time — no new column, no migration.

**Files:**
- Modify: `backend/internal/database/db_init.go` (add `CleanupTrailingGhostParticipations`, placed after `CleanupSpuriousParticipations`)
- Test: `backend/internal/database/db_init_test.go` (new file, or append if it already exists — check first with `ls internal/database/db_init_test.go`)
- Modify: `backend/internal/gnovalidator/gnovalidator_realtime.go:603-613` (`StartValidatorMonitoring`)

**Interfaces:**
- Consumes: `GetMonikerMap(chainID string) map[string]string` (already exported, `gnovalidator_realtime.go:617`).
- Produces: `func CleanupTrailingGhostParticipations(db *gorm.DB, chainID string, currentAddrs []string) error` (new, `database` package).

- [ ] **Step 1: Write the failing test**

Check whether `backend/internal/database/db_init_test.go` already exists:

Run: `ls internal/database/db_init_test.go`

If it does not exist, create it with this content. If it does exist, append the function to it (keep the existing `package database_test` and imports, adding any missing ones from the list below).

```go
package database_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedDP(t *testing.T, db *gorm.DB, chain, addr string, height int64, participated bool, date time.Time) {
	t.Helper()
	row := database.DailyParticipation{
		ChainID: chain, Addr: addr, Moniker: addr + "-mon",
		BlockHeight: height, Date: date, Participated: participated, TxContribution: false,
	}
	require.NoError(t, db.Create(&row).Error)
}

func TestCleanupTrailingGhostParticipations(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test-trailing-ghost"
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// g1departed: really participated up to height 100, then kept getting
	// phantom participated=false rows up to height 110 (the bug this
	// cleanup targets). It is NOT part of the current live valset.
	seedDP(t, db, chain, "g1departed", 99, true, day)
	seedDP(t, db, chain, "g1departed", 100, true, day)
	seedDP(t, db, chain, "g1departed", 105, false, day)
	seedDP(t, db, chain, "g1departed", 110, false, day)

	// g1bonded: still in the current live valset, going through a long
	// real downtime that looks identical to g1departed's tail. Must be
	// left completely untouched because it IS in currentAddrs.
	seedDP(t, db, chain, "g1bonded", 99, true, day)
	seedDP(t, db, chain, "g1bonded", 100, true, day)
	seedDP(t, db, chain, "g1bonded", 105, false, day)
	seedDP(t, db, chain, "g1bonded", 110, false, day)

	currentAddrs := []string{"g1bonded"}
	require.NoError(t, database.CleanupTrailingGhostParticipations(db, chain, currentAddrs))

	var departedRows []database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", chain, "g1departed").
		Order("block_height").Find(&departedRows).Error)
	require.Len(t, departedRows, 2, "only the two participated=true rows should survive for the departed address")
	require.Equal(t, int64(99), departedRows[0].BlockHeight)
	require.Equal(t, int64(100), departedRows[1].BlockHeight)

	var bondedRows []database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", chain, "g1bonded").
		Order("block_height").Find(&bondedRows).Error)
	require.Len(t, bondedRows, 4, "still-bonded address must be untouched even though its tail looks identical")
}

func TestCleanupTrailingGhostParticipations_EmptyCurrentAddrsIsNoop(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test-trailing-ghost-empty-guard"
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	seedDP(t, db, chain, "g1anything", 99, true, day)
	seedDP(t, db, chain, "g1anything", 100, false, day)

	// An empty currentAddrs must be treated as "valset unknown" and be a
	// deliberate no-op, not "everyone is departed" (which would otherwise
	// vacuously match every address via addr <> ALL(empty array) = true).
	require.NoError(t, database.CleanupTrailingGhostParticipations(db, chain, nil))

	var rows []database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ?", chain).Find(&rows).Error)
	require.Len(t, rows, 2)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/database/... -run TestCleanupTrailingGhostParticipations -v`
Expected: FAIL with `undefined: database.CleanupTrailingGhostParticipations`

- [ ] **Step 3: Write minimal implementation**

In `backend/internal/database/db_init.go`, add this function right after `CleanupSpuriousParticipations` (which currently ends at line 632):

```go
// CleanupTrailingGhostParticipations deletes participated=false rows written
// after a validator's real last participation, for any address that has
// history on this chain but is not part of the currently live valset
// (currentAddrs). Idempotent: safe to call on every chain startup and after
// every detected departure (WatchNewValidators). Unlike first_active_block,
// this value has no per-block hot-path reader — once MonikerMap is a true
// snapshot (see ReplaceMonikerMap), it alone stops future ghost writes, so
// "last true participation" is computed live via a correlated subquery
// instead of being persisted.
//
// currentAddrs empty is treated as "valset unknown" (e.g. the initial
// /validators fetch failed) and is a deliberate no-op: an empty exclusion
// list would otherwise make every address vacuously match "departed" via
// addr <> ALL(empty array), which is always true.
func CleanupTrailingGhostParticipations(db *gorm.DB, chainID string, currentAddrs []string) error {
	if len(currentAddrs) == 0 {
		return nil
	}

	start := time.Now()

	const lastTrueCTE = `
		SELECT addr, MAX(block_height) AS last_true
		FROM (
			SELECT addr, block_height FROM daily_participations
			WHERE chain_id = ? AND participated = true AND addr <> ALL(?)
			UNION ALL
			SELECT addr, last_block_height AS block_height FROM daily_participation_agregas
			WHERE chain_id = ? AND participated_count > 0 AND addr <> ALL(?)
		) combined
		GROUP BY addr
	`

	raw := db.Exec(`
		DELETE FROM daily_participations dp
		USING (`+lastTrueCTE+`) last_true_block
		WHERE dp.chain_id = ?
		  AND dp.addr = last_true_block.addr
		  AND dp.participated = false
		  AND dp.block_height > last_true_block.last_true
	`, chainID, currentAddrs, chainID, currentAddrs, chainID)
	if raw.Error != nil {
		return fmt.Errorf("CleanupTrailingGhostParticipations(%s): daily_participations: %w", chainID, raw.Error)
	}
	if raw.RowsAffected > 0 {
		log.Printf("[db][%s] deleted %d trailing ghost rows from daily_participations in %s",
			chainID, raw.RowsAffected, time.Since(start).Round(time.Millisecond))
	}

	agrega := db.Exec(`
		DELETE FROM daily_participation_agregas dpa
		USING (`+lastTrueCTE+`) last_true_block
		WHERE dpa.chain_id = ?
		  AND dpa.addr = last_true_block.addr
		  AND dpa.first_block_height > last_true_block.last_true
	`, chainID, currentAddrs, chainID, currentAddrs, chainID)
	if agrega.Error != nil {
		return fmt.Errorf("CleanupTrailingGhostParticipations(%s): daily_participation_agregas: %w", chainID, agrega.Error)
	}
	if agrega.RowsAffected > 0 {
		log.Printf("[db][%s] deleted %d trailing ghost rows from daily_participation_agregas in %s",
			chainID, agrega.RowsAffected, time.Since(start).Round(time.Millisecond))
	}
	return nil
}
```

Note: `currentAddrs` (a Go `[]string`) binds directly to the Postgres `text[]` parameter used by `<> ALL(?)` because this project's GORM Postgres driver runs on `jackc/pgx/v5` (see `go.mod`), which supports native Go-slice-to-Postgres-array binding — no wrapper type (e.g. `pq.StringArray`, which belongs to the unused `lib/pq` driver) is needed.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/database/... -run TestCleanupTrailingGhostParticipations -v`
Expected: PASS (both tests)

- [ ] **Step 5: Wire the cleanup into `StartValidatorMonitoring`**

In `backend/internal/gnovalidator/gnovalidator_realtime.go:603-613`, change:

```go
func StartValidatorMonitoring(ctx context.Context, db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	rpcClient := NewFallbackRPCClient(chainCfg.RPCEndpoints)
	SetChainRPCClient(chainID, rpcClient)
	client := gnoclient.Client{RPCClient: rpcClient}

	t := GetThresholds()
	InitMonikerMap(db, chainID, client, chainCfg)
	WatchNewValidators(ctx, db, chainID, client, chainCfg, t.NewValidatorScan())
	CollectParticipation(ctx, db, chainID, client)
	WatchValidatorAlerts(ctx, db, chainID, t.AlertCheckInterval())
}
```

to:

```go
func StartValidatorMonitoring(ctx context.Context, db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	rpcClient := NewFallbackRPCClient(chainCfg.RPCEndpoints)
	SetChainRPCClient(chainID, rpcClient)
	client := gnoclient.Client{RPCClient: rpcClient}

	t := GetThresholds()
	InitMonikerMap(db, chainID, client, chainCfg)

	currentAddrs := make([]string, 0)
	for addr := range GetMonikerMap(chainID) {
		currentAddrs = append(currentAddrs, addr)
	}
	if err := database.CleanupTrailingGhostParticipations(db, chainID, currentAddrs); err != nil {
		log.Printf("[monitor][%s] CleanupTrailingGhostParticipations error: %v", chainID, err)
	}

	WatchNewValidators(ctx, db, chainID, client, chainCfg, t.NewValidatorScan())
	CollectParticipation(ctx, db, chainID, client)
	WatchValidatorAlerts(ctx, db, chainID, t.AlertCheckInterval())
}
```

- [ ] **Step 6: Run the full build and test suite**

Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -60`
Expected: build and vet succeed; all tests pass across the whole module (Postgres test DB must be reachable — see CLAUDE.md).

- [ ] **Step 7: Commit**

```bash
git add internal/database/db_init.go internal/database/db_init_test.go internal/gnovalidator/gnovalidator_realtime.go
git commit -m "feat(db): retroactively clean up trailing post-departure ghost rows

Before this change, a validator that left the valset kept accumulating
participated=false rows forever (MonikerMap never pruned it). The
previous commit stops new ghost rows going forward; this one cleans up
rows already written before that fix shipped, for any address with
history but no longer in the live valset. Computed via a correlated
subquery (no schema change — last_active_block has no hot-path reader,
unlike first_active_block), called once per chain at startup right
after the initial InitMonikerMap."
```

---

## Task 6: Documentation

**Files:**
- Modify: `CLAUDE.md` (repo root — not git-tracked per project convention, edit on disk, do not attempt to commit it)

- [ ] **Step 1: Update the Known Limitations section**

In `CLAUDE.md`, under `## Known Limitations`, add a bullet documenting the new behavior (place it alongside the existing score/alert-related bullets):

```markdown
- **Valset membership is now actively pruned and alerted on** — `MonikerMap` is replaced (not merged) on every `InitMonikerMap` refresh cycle, so a validator that leaves the valset stops accumulating `participated=false` rows immediately, and `WatchNewValidators` sends a "Validator left the valset" or "Validator address changed" notice (correlated via the valoper registry's operator identity) on the very next refresh cycle. A one-time `CleanupTrailingGhostParticipations` pass at each chain's startup retroactively removes trailing ghost rows written before this fix shipped.
```

- [ ] **Step 2: Update the Alert Thresholds section**

Under `## Alert Thresholds`, add:

```markdown
- **Valset departure / address change** — detected every `NewValidatorScan` refresh cycle (same cadence as "new validator detected"). A departure whose old signing address is traced back to an operator (via the valoper registry) whose current signing address just joined the valset is reported as a single "address changed" event instead of separate departure/arrival notices. Broadcast via `internal.SendInfoValidator` (Discord + Slack + Telegram), same path as "new validator detected".
```

- [ ] **Step 3: Verify the file is still valid markdown and not staged**

Run: `git status CLAUDE.md`
Expected: `CLAUDE.md` does not appear as tracked/staged (per `project_claudemd_untracked` — it's intentionally not committed).

- [ ] **Step 4: No commit for this task**

`CLAUDE.md` is not git-tracked in this repository — leave the edit on disk. Do not run `git add CLAUDE.md` or include it in any commit.

---

## Final Verification

- [ ] Run the full test suite one more time: `go test ./... -v 2>&1 | tail -80`
- [ ] Run `go vet ./...`
- [ ] Run `go build ./...`
- [ ] Confirm `git log --oneline main..HEAD` shows exactly 5 commits (Tasks 1-5; Task 6 is a local-only doc edit).
