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
- **Correction from the first draft of this spec:** issue #2's historical bad rows are *not* actually a gap — `PopulateFirstActiveBlocks` + `CleanupSpuriousParticipations` (`db_init.go`) already run on every service startup and delete `participated=false` rows preceding a validator's real `first_active_block`. The `BackfillParallel` code fix (Fix 2) still matters (it keeps the report correct *between* restarts), but no new cleanup is needed for it.
- Issue #3 (trailing ghost rows after a real departure, e.g. VALIDARIOS's old address) has **no existing cleanup mechanism**. Fix 4 below adds one — computed live via a correlated subquery at cleanup time, not a new persisted column, since (unlike `first_active_block`) nothing reads this value in the per-block hot path.

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

## Fix 4 — Retroactive cleanup for trailing post-departure ghost rows

Conceptually symmetric to `first_active_block` / `CleanupSpuriousParticipations` (`db_init.go`), but for the *end* of a validator's real activity instead of the start — **without** a schema addition.

**No persisted column.** `first_active_block` is persisted because it is read on *every block, for every validator* in the hot path (`SaveParticipation`, `BackfillParallel`) — computing it live there would be far too expensive, hence the in-memory cache backed by a DB column. `last_active_block` has no such hot-path reader: once `MonikerMap` is properly pruned (Fix 3a), the map itself is what stops new ghost rows from being written the moment a departure is detected — nothing needs to persist "when it stopped" to prevent future writes. The only two consumers are (a) a one-time-per-chain historical cleanup and (b) recording an already-known departure, both cold paths. So `MAX(block_height) WHERE participated=true` is computed as a correlated subquery at cleanup time instead of stored — no new column, no migration, no `Set/UpsertLastActiveBlock` helpers, no `-1` sentinel to manage.

**`CleanupTrailingGhostParticipations(db, chainID string, departedAddrs []string) error`** (new function, `db_init.go` alongside `CleanupSpuriousParticipations`). `departedAddrs` is supplied by the caller as "addresses with history for this chain that are not a key in `GetMonikerMap(chainID)`" — this is exactly the guard that makes it safe (a validator mid-downtime but still bonded is never in this list, since it's still a `MonikerMap` key):

```sql
DELETE FROM daily_participations dp
USING (
    SELECT addr, MAX(block_height) AS last_true
    FROM daily_participations
    WHERE chain_id = ? AND addr = ANY(?) AND participated = true
    GROUP BY addr
) last_true_block
WHERE dp.chain_id = ?
  AND dp.addr = last_true_block.addr
  AND dp.participated = false
  AND dp.block_height > last_true_block.last_true
```

plus the equivalent for `daily_participation_agregas` (delete/adjust aggregate day-rows entirely after each address's `last_true`, same day-granularity boundary tolerance already accepted by the existing `CleanupSpuriousParticipations` for the start-of-activity case). An address in `departedAddrs` with **no** `participated=true` row at all (never really active — e.g. a pure Fix-2-style backfill ghost that also never truly joined) simply has nothing matched by the `GROUP BY` and is left untouched by this query; that case is already fully handled by the existing `CleanupSpuriousParticipations`.

**Sequencing.** Called from `StartValidatorMonitoring` (`gnovalidator_realtime.go:603-613`), once per chain, right after the initial `InitMonikerMap(db, chainID, client, chainCfg)` call and before `WatchNewValidators`/`CollectParticipation` start — that's the earliest point where `GetMonikerMap(chainID)` reflects the real, current, per-chain valset, needed to compute `departedAddrs`. Idempotent (a second run finds nothing left to delete), so safe on every restart, matching the existing pair's behavior.

**Going forward.** Once Fix 3b lands, `WatchNewValidators` already knows the exact moment and address of every new departure; it can call `CleanupTrailingGhostParticipations` immediately with just that one address instead of waiting for the next restart's full per-chain scan — same function, smaller `departedAddrs` slice. The startup-time full-chain call remains as the catch-up path for departures that happened before this code shipped (e.g. VALIDARIOS's old address) and as a safety net for anything the live watcher might miss across a restart.

**Testing.** DB-backed test seeding a departed address (rows before and after a simulated departure height) plus a still-bonded address with a long downtime tail, asserting: only the departed address's trailing `participated=false` rows are removed, its `participated=true` rows are untouched, and the still-bonded address (not in `departedAddrs`) is entirely untouched even though its own tail also looks like a downtime.

---

## Files Touched

- `backend/internal/database/db_score.go` — `addr <> 'all'` filter in `GetValidatorScores`.
- `backend/internal/gnovalidator/sync.go` — `BackfillParallel` uses live `GetFirstActiveBlock`/`SetFirstActiveBlock`.
- `backend/internal/gnovalidator/valoper.go` — `InitMonikerMap` replaces `MonikerMap[chainID]` instead of merging; exposes the valoper list to callers.
- `backend/internal/gnovalidator/gnovalidator_realtime.go` — `WatchNewValidators` adds the removed-validator diff, rotation correlation, and new alert dispatch; `StartValidatorMonitoring` calls `CleanupTrailingGhostParticipations` once per chain, right after the initial `InitMonikerMap`, with the addresses absent from `GetMonikerMap(chainID)`.
- `backend/internal/database/db_init.go` — new `CleanupTrailingGhostParticipations(db, chainID, departedAddrs)` function, no schema change.
- `CLAUDE.md` — update the Known Limitations / Alert Thresholds sections to describe the new departure/rotation alert and the trailing-ghost cleanup, and note MonikerMap is now a live snapshot, not an accumulation.

## Testing

- `db_score_test.go` — `addr='all'` exclusion (Fix 1).
- `sync_test.go` (or new file) — sequential first-active-block guard behavior (Fix 2).
- `gnovalidator_realtime_test.go` (or new file) — rotation/departure/arrival classification (Fix 3), RPC-free.
- `db_init_test.go` (or new file) — `CleanupTrailingGhostParticipations` (Fix 4), Postgres-backed.
- `go vet ./...`, `go test ./...` (Postgres test DB per CLAUDE.md).

## Out of Scope

- No change to `addr_monikers.voting_power` staleness for departed validators (their last-known VP keeps being used in severity weighting until their rows age out) — a related but separate cleanup, not requested here.
- No use of `r/sys/validators/v2`'s change log or `r/sys/validators/v3`'s `IsValidator`/`GetValidators` as a new RPC dependency — the existing `/validators` polling already gives us everything needed.
- Fix 4's one-time backfill only recovers addresses that are *currently* absent from the live valset at the time it runs. A departure-then-permanent-silence pattern is covered; a validator that left and already rejoined with a new address before this code ships will have its old ghost tail correctly cleaned (still absent under the old address), which is the common case.
