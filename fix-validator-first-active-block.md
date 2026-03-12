# Fix — Validator First Active Block

## Problem

During a backfill (`BackfillParallel` / `BackfillRange`), the code iterates over `monikerMap`
which contains **all validators active today**.

For each block, any validator absent from the precommits receives a
`participated = false` row in `daily_participations`.

However, a validator accepted by the GovDAO at block 900 did not exist at blocks 0–899.
By creating `participated = false` rows for these blocks, the system attributes
thousands of fictitious missed blocks to that validator, which corrupts:

- uptime (`UptimeMetricsaddr`)
- missed blocks (`MissingBlock`, `CalculateConsecutiveMissedBlocks`)
- alerts (WARNING/CRITICAL thresholds incorrectly triggered)

---

## Root Cause

`BackfillParallel` (sync.go:211) and `BackfillRange` (sync.go:130):

```go
// participated false
for addr, mon := range monikerMap {      // monikerMap = CURRENT validators
    if _, ok := seen[addr]; ok { continue }
    rows = append(rows, dpRow{
        ...,
        Participated: false,             // inserted even before activation
    })
}
```

`monikerMap` contains no information about each validator's activation block.
The function therefore cannot know whether a validator was already active
at block `j.H`.

---

## Action Plan

### Phase 1 — Data model: add `first_active_block`

Add a `first_active_block` field to the `AddrMoniker` struct in
`db_init.go`:

```go
type AddrMoniker struct {
    Addr             string `gorm:"column:addr;primaryKey"`
    Moniker          string `gorm:"column:moniker;not null"`
    FirstActiveBlock int64  `gorm:"column:first_active_block;default:-1"`
}
```

`-1` = unknown. GORM handles the migration via `AutoMigrate` (column addition).

---

### Phase 2 — Populate `first_active_block` for existing validators

For validators already present in `daily_participations`, the first block
where `participated = 1` IS their real activation block:

```sql
UPDATE addr_monikers
SET first_active_block = (
    SELECT MIN(block_height)
    FROM daily_participations
    WHERE daily_participations.addr = addr_monikers.addr
      AND participated = 1
)
WHERE first_active_block = -1;
```

To be run once at startup, in `InitDB` or during
`MonikerMap` initialization, after `AutoMigrate`.

For validators **never seen** in `daily_participations`,
`first_active_block` stays at `-1` (unknown — will be determined dynamically).

---

### Phase 3 — Detect `first_active_block` dynamically

#### 3a. During backfill

In the `BackfillParallel` worker, when a validator appears in the
precommits of a block (`participated = true`) **and their `first_active_block`
is `-1`** in the transmitted map, record this block as their activation:

```go
// When processing precommits (participated = true)
if firstActiveBlocks[addr] == -1 {
    firstActiveBlocks[addr] = j.H   // first observed block
    // + UpsertAddrMoniker with first_active_block = j.H
}
```

#### 3b. During real-time (`CollectParticipation`)

Same logic: when a validator appears for the first time in the
precommits of the current block and their `first_active_block` is `-1`, update
`addr_monikers`.

#### 3c. Alternative option: query valopers.Render

`valopers.Render(":addr")` potentially exposes a registration date.
If the response contains the GovDAO block that accepted the validator, it can
be used directly in `InitMonikerMap`.

To investigate during implementation — this is more precise but depends on the
realm structure.

---

### Phase 4 — Modify `BackfillParallel` and `BackfillRange`

Pass a `firstActiveBlocks map[string]int64` map to both functions.

In the "participated false" loop, skip blocks prior to activation:

```go
for addr, mon := range monikerMap {
    if _, ok := seen[addr]; ok {
        continue // already added as participated=true
    }
    fab := firstActiveBlocks[addr]
    if fab > 0 && j.H < fab {
        continue // validator not yet active at this block
    }
    rows = append(rows, dpRow{
        ...,
        Participated: false,
    })
}
```

`fab == -1` (unknown) → still insert (preserves current behavior;
worst case is a false negative on the first blocks of an unknown validator).

---

### Phase 5 — Modify `CollectParticipation` (real-time)

Same guard in the `participated = false` loop:

```go
for valAddr, moniker := range MonikerMap {
    fab := getFirstActiveBlock(valAddr) // reads addr_monikers or in-memory cache
    if fab > 0 && currentHeight < fab {
        continue
    }
    // insert participated=false
}
```

---

### Phase 6 — Cleanup existing data (one-shot migration)

After deployment, delete the spurious rows already in the database:

```sql
DELETE FROM daily_participations dp
WHERE participated = 0
  AND EXISTS (
      SELECT 1 FROM addr_monikers am
      WHERE am.addr = dp.addr
        AND am.first_active_block > 0
        AND dp.block_height < am.first_active_block
  );
```

**Warning:** Heavy query on a large table — run outside of production
or in maintenance mode (WAL mode preserves concurrent reads).

---

## Summary of files to modify

| File | Change |
| --- | --- |
| `db_init.go` | Add `FirstActiveBlock int64` to `AddrMoniker` |
| `db_init.go` | One-shot SQL population query at startup |
| `db.go` | `UpsertAddrMoniker` accepts `firstActiveBlock int64` |
| `sync.go` | `BackfillParallel` + `BackfillRange`: `firstActiveBlocks` param + guard |
| `gnovalidator_realtime.go` | `CollectParticipation`: guard on `first_active_block` |
| `valoper.go` | `InitMonikerMap`: load `first_active_block` from DB into memory |

---

## Recommended implementation order

1. Phase 1 — struct + AutoMigrate (non-breaking)
2. Phase 2 — populate at startup (idempotent SQL query)
3. Phase 4 — corrected backfill
4. Phase 5 — corrected real-time
5. Phase 3b — dynamic detection during both loops
6. Phase 6 — data cleanup (after staging validation)
