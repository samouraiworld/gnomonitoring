# Valset Membership Integrity — Backfill Ghosts, `addr='all'` Leak, Departure/Rotation Alerts

**Status:** Approved (design). Ready for implementation plan.
**Branch:** new branch off `main`, e.g. `fix/valset-membership-integrity`.
**Goal:** Stop validators that never (or no longer) belong to the valset from polluting `/api/reports/validators`, and add real-time alerts when a validator leaves the valset or rotates its signing address — on all existing broadcast channels.

## Context

Investigation of the validator report surfaced three related issues, all rooted in the same gap: nothing in the codebase distinguishes "has a row somewhere" from "is actually in the valset right now."

1. **`GetValidatorScores` (`db_score.go`) leaks `addr='all'`.** `alert_logs` carries chain-wide "blockchain stuck" rows under the sentinel address `all`. `GetLastAlertTimes` already excludes them (`AND addr <> 'all'`); `GetValidatorScores` — which feeds the per-period CRITICAL/WARNING counts merged into the report — does not. `all` shows up in the report as a fake validator with a score of 0 (no participation rows exist for it).

2. **`BackfillParallel` (`sync.go`) invents missed-block history for validators before they ever joined.** It reads `firstActiveBlocks := GetFirstActiveBlockMap(chainID)` **once** before spawning 20 concurrent workers, then never refreshes that snapshot for the rest of the run. For a validator whose real first activation falls inside the backfilled range, every block processed before the worker that happens to observe the real activation gets a `participated=false` row — including blocks that predate the validator's real existence. These rows are permanent and drag down `current_year`/`current_month` scores indefinitely.

3. **`MonikerMap` is additive and never pruned**, so it silently becomes the deeper root cause behind "ghost" validators (e.g. the VALIDARIOS case: an operator's old signing address leaves the valset, but keeps receiving `participated=false` rows forever). `InitMonikerMap` (`valoper.go`) only ever *adds* keys via `SetMoniker`; nothing removes an address once it drops out of `/validators`. `SaveParticipation` iterates this ever-growing map every block, so a departed address keeps accumulating "missed" rows forever. There is also no alert when a validator leaves, and no correlation to recognize "this is the same validator with a new signing address" versus "an unrelated validator joined."

Live testing on `test13` (`https://rpc.test13.testnets.gno.land/`) confirmed the ground truth needed to fix #3: `r/sys/validators/v3.GetValidators()`/`IsValidator()` mirrors `/validators` exactly (v3 is the live realm on this chain; `r/sys/validators/v2`'s change log is empty on this chain, so it is not a usable data source). Address rotation itself is a two-step on-chain process: a self-service, throttled `UpdateSigningKey` call in `r/gnops/valopers` declares the new signing key (operator `Address` stays stable, `SigningAddress` changes), but the actual bonded valset only changes once a GovDAO proposal (`r/sys/validators/v3`'s `NewValidatorProposalRequest`) is voted and executed. This means the old address's departure and the new address's arrival land in the **same** `/validators` snapshot/poll cycle, which is exactly what the correlation logic below relies on.

## Decisions (locked)

- Fix all three issues in one branch — they share the same "valset membership" theme and the same files.
- `GetValidatorScores`: add the same `addr <> 'all'` filter `GetLastAlertTimes` already has. No behavior change beyond removing the phantom row.
- `BackfillParallel`: replace the frozen local snapshot with the existing thread-safe `GetFirstActiveBlock`/`SetFirstActiveBlock` (same functions the realtime path already uses). This bounds the residual race window to normal worker-concurrency scale instead of the whole backfill range.
- `InitMonikerMap`: replace `MonikerMap[chainID]` wholesale each refresh cycle instead of merging into it. This is the root fix — once applied, `SaveParticipation` naturally stops writing rows for departed addresses.
- `WatchNewValidators`: add the symmetric "removed" diff next to the existing "added" diff, correlate via the valoper registry (`GetValopers`) to distinguish an address rotation (one "address changed" alert) from an unrelated departure/arrival (existing "new validator" alert + new "validator left" alert).
- New alerts are sent through the existing `internal.SendInfoValidator` (Discord + Slack webhooks + Telegram validator bot in one call) — the same delivery path already used for "new validator detected" / "activity restored", so all channels are covered without new plumbing.
- No new RPC calls: everything needed (`/validators` snapshot, valoper registry) is already fetched every cycle by `InitMonikerMap`.
- Historical bad rows already written by issues #2/#3 are **not** retroactively cleaned up by this change (no data migration) — out of scope, noted below.

## Global Constraints

- English only for all comments, docs, commit messages (per CLAUDE.md).
- Every query stays scoped by `chain_id`.
- No change to `score.Compute` or its weights.
- `go vet ./...` and `go test ./...` must pass (Postgres test DB required, see CLAUDE.md).

---

## Fix 1 — `GetValidatorScores` excludes `addr='all'`

**Change.** `db_score.go`, add `AND al.addr <> 'all'` to the `WHERE` clause, mirroring `GetLastAlertTimes`.

**Testing.** Extend the existing `GetValidatorScores` test (or add one) seeding a CRITICAL row with `addr='all'` alongside a real validator's rows; assert `all` never appears in the returned slice.

---

## Fix 2 — `BackfillParallel` stops inventing pre-activation history

**Change.** `sync.go`: remove the local `firstActiveBlocks := GetFirstActiveBlockMap(chainID)` snapshot (line 171) and its per-worker reads/writes (lines ~204-211). Replace with direct calls to the already-thread-safe:

```go
gnovalidator.GetFirstActiveBlock(chainID, addr)
gnovalidator.SetFirstActiveBlock(chainID, addr, blockHeight) // + database.UpsertFirstActiveBlock, unchanged
```

Same read-then-guard-then-set logic as today, just backed by the live shared map instead of a frozen local copy, so a worker that discovers a validator's real first activation immediately makes that visible to every other worker still processing that address's other blocks.

**Testing.** Existing backfill tests exercise `flushChunk`/`flushBatch`; add a focused unit test that simulates a monikerMap containing an address with no prior `first_active_block`, feeds it blocks out of chronological order (mimicking concurrent workers), and asserts no `participated=false` row is written for a height at or after the height where `participated=true` was observed for that address. (A fully deterministic test of the concurrent case is inherently racy; the test targets the sequential guard logic itself, which is what changes.)

---

## Fix 3 — Prune `MonikerMap`, alert on departure / address change

### 3a. `InitMonikerMap` becomes a true snapshot

**Change.** `valoper.go`: after building `tempMonikers` (Step 5), replace the per-chain map instead of merging:

```go
MonikerMutex.Lock()
MonikerMap[chainID] = tempMonikers
MonikerMutex.Unlock()
```

(replacing the current `for addr, moniker := range tempMonikers { SetMoniker(...) }` loop, which only adds keys). Verified safe for all three other readers of `GetMonikerMap` (`Prometheus.go:454`, `health.go:747`, `health.go:855`) — they all use it as a moniker-text lookup against a freshly-fetched current validator set, never as an iteration source of "all validators ever seen," so shrinking it is correct, not a regression.

### 3b. Detect departures and correlate address rotation

**Change.** `WatchNewValidators` (`gnovalidator_realtime.go`): today it captures `oldMap := GetMonikerMap(chainID)`, calls `InitMonikerMap`, then diffs only for additions. Add:

1. `removed := keys(oldMap) − keys(newMap)`, `added := keys(newMap) − keys(oldMap)` (the existing addition diff already computes `added` implicitly; make it explicit so it can be reused).
2. Maintain one new piece of state, mirroring the existing `MonikerMap`/`FirstActiveBlockMap` pattern: a package-level, mutex-protected `SigningToOperator map[chainID]map[signingAddr]operatorAddr`, rebuilt from `GetValopers(client)`'s `SigningAddress -> Address` pairs at the **end** of every `InitMonikerMap` cycle (after this cycle's correlation step below has consumed the *previous* cycle's version). This avoids a second RPC round-trip and avoids depending on the chain retaining retired-key history.
3. For each address `R` in `removed`, look up `R` in the **previous** cycle's `SigningToOperator` to get its operator address, then look up that operator's entry in **this** cycle's freshly-fetched valoper list (`GetValopers`) to read its *current* `SigningAddress`. If that current `SigningAddress` is present in `added`:
   - Match → one **"address changed"** alert: `moniker (operator) rotated signing address: R → new`.
   - No match (operator not found, or its current signing address isn't a new arrival) → generic **"validator left the valset"** alert for `R`/its last known moniker.
4. Addresses in `added` not consumed as a rotation target keep the existing "new validator detected" alert, unchanged.
5. Both new alert types call `internal.SendInfoValidator(chainID, msg, "info", db)` (or `"warning"` for the plain departure, to be visually distinct from a routine rotation) — same function already used for the existing notices.

**Edge cases (from the design discussion):**

- Cold start: `oldMap` already equals the just-populated map from the initial `InitMonikerMap` call in `StartValidatorMonitoring`, so the first ticker cycle produces no spurious diffs.
- A validator that leaves and later rejoins with the **same** address: no longer in `removed` at that point (it's just a fresh "new validator detected" on rejoin, matching current behavior).
- Multiple simultaneous departures/arrivals in one cycle: matched 1:1 by operator address; anything left unmatched falls back to the generic alerts.

**Testing.** Add a pure, RPC-free unit test for the classification function (old moniker keys, new moniker keys, valoper list in/out) asserting: a matched rotation yields exactly one "address changed" event; an unmatched departure yields one "left" event; an unmatched arrival yields one "new validator" event; simultaneous unrelated departure+arrival (different operators) yields two separate events, not a false rotation match.

---

## Files Touched

- `backend/internal/database/db_score.go` — `addr <> 'all'` filter in `GetValidatorScores`.
- `backend/internal/gnovalidator/sync.go` — `BackfillParallel` uses live `GetFirstActiveBlock`/`SetFirstActiveBlock`.
- `backend/internal/gnovalidator/valoper.go` — `InitMonikerMap` replaces `MonikerMap[chainID]` instead of merging; exposes the valoper list to callers.
- `backend/internal/gnovalidator/gnovalidator_realtime.go` — `WatchNewValidators` adds the removed-validator diff, rotation correlation, and new alert dispatch.
- `CLAUDE.md` — update the Known Limitations / Alert Thresholds sections to describe the new departure/rotation alert and note MonikerMap is now a live snapshot, not an accumulation.

## Testing

- `db_score_test.go` — `addr='all'` exclusion (Fix 1).
- `sync_test.go` (or new file) — sequential first-active-block guard behavior (Fix 2).
- `gnovalidator_realtime_test.go` (or new file) — rotation/departure/arrival classification (Fix 3), RPC-free.
- `go vet ./...`, `go test ./...` (Postgres test DB per CLAUDE.md).

## Out of Scope

- No retroactive cleanup of `daily_participations`/`daily_participation_agregas` rows already written by issues #2/#3 — existing bad history ages out of the report's period windows naturally (up to a year for `current_year`), consistent with the project's existing "no retroactive backfill" precedent for the proposer-reliability gap.
- No change to `addr_monikers.voting_power` staleness for departed validators (their last-known VP keeps being used in severity weighting until their rows age out) — a related but separate cleanup, not requested here.
- No use of `r/sys/validators/v2`'s change log or `r/sys/validators/v3`'s `IsValidator`/`GetValidators` as a new RPC dependency — the existing `/validators` polling already gives us everything needed.
