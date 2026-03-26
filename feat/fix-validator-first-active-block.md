# Fix — Validator First Active Block

## Problem

During a backfill (`BackfillParallel` / `BackfillRange`), the code iterates over `monikerMap`
which contains **all validators active today** for a given chain.

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
for addr, mon := range monikerMap {      // monikerMap = CURRENT validators on this chain
    if _, ok := seen[addr]; ok { continue }
    rows = append(rows, dpRow{
        ...,
        Participated: false,             // inserted even before activation
    })
}
```

`monikerMap` (pre-scoped to a single chain via `GetMonikerMap(chainID)`) contains no
information about each validator's activation block. The function therefore cannot know
whether a validator was already active at block `j.H`.

---

## Action Plan

### Multi-chain Changes Summary

The original plan has been updated to account for multi-chain integration already present in the codebase:

- `AddrMoniker` now has a `ChainID` field and compound unique index on `(chain_id, addr)`, so Phase 2's UPDATE query must join on both columns.
- `monikerMap` is now `map[string]int64]` keyed by chain ID, and functions call `GetMonikerMap(chainID)` to get the chain-scoped map.
- A new global `FirstActiveBlockMap` structure (Phase 3) mirrors `MonikerMap` and provides thread-safe access per chain.
- `InitMonikerMap` (Phase 3) already accepts `chainID` as a parameter; it will now load `first_active_block` from the DB into the in-memory cache.
- Backfill and real-time functions no longer receive additional parameters; they call `GetFirstActiveBlockMap(chainID)` locally.

---

### Phase 1 — Data model: add `first_active_block`

Add a `first_active_block` field to the `AddrMoniker` struct in `db_init.go`.

The struct already has `ChainID`, `Addr`, and `Moniker` fields:

```go
type AddrMoniker struct {
    ID               uint   `gorm:"primaryKey;autoIncrement;column:id"`
    ChainID          string `gorm:"column:chain_id;not null;default:betanet;uniqueIndex:uniq_chain_addr,priority:1"`
    Addr             string `gorm:"column:addr;not null;uniqueIndex:uniq_chain_addr,priority:2"`
    Moniker          string `gorm:"column:moniker;not null"`
    FirstActiveBlock int64  `gorm:"column:first_active_block;default:-1"`
}
```

`-1` = unknown activation block. GORM handles the migration via `AutoMigrate` (column addition).

---

### Phase 2 — Populate `first_active_block` for existing validators

For validators already present in `daily_participations`, the first block
where `participated = 1` IS their real activation block.

Run this query once at startup, in `InitDB` after `AutoMigrate`:

```sql
UPDATE addr_monikers
SET first_active_block = (
    SELECT MIN(block_height)
    FROM daily_participations
    WHERE daily_participations.addr = addr_monikers.addr
      AND daily_participations.chain_id = addr_monikers.chain_id
      AND participated = 1
)
WHERE first_active_block = -1;
```

The query is chain-scoped via the join on both `chain_id` and `addr`.

For validators **never seen** in `daily_participations`,
`first_active_block` stays at `-1` (unknown — will be determined dynamically).

---

### Phase 3 — In-memory cache and initialization

#### 3a. Add global structures in `valoper.go`

Add a new map and mutex mirroring the `MonikerMap` structure:

```go
var FirstActiveBlockMap = make(map[string]map[string]int64) // chainID -> addr -> first_active_block
var FirstActiveBlockMutex sync.RWMutex
```

#### 3b. Add thread-safe helpers

Implement the following functions in `valoper.go`:

```go
func GetFirstActiveBlock(chainID, addr string) int64 {
    FirstActiveBlockMutex.RLock()
    defer FirstActiveBlockMutex.RUnlock()
    if chain, ok := FirstActiveBlockMap[chainID]; ok {
        if fab, ok := chain[addr]; ok {
            return fab
        }
    }
    return -1  // unknown
}

func SetFirstActiveBlock(chainID, addr string, block int64) {
    FirstActiveBlockMutex.Lock()
    defer FirstActiveBlockMutex.Unlock()
    if _, ok := FirstActiveBlockMap[chainID]; !ok {
        FirstActiveBlockMap[chainID] = make(map[string]int64)
    }
    FirstActiveBlockMap[chainID][addr] = block
}

func GetFirstActiveBlockMap(chainID string) map[string]int64 {
    FirstActiveBlockMutex.RLock()
    defer FirstActiveBlockMutex.RUnlock()
    if chain, ok := FirstActiveBlockMap[chainID]; ok {
        result := make(map[string]int64)
        for addr, fab := range chain {
            result[addr] = fab
        }
        return result
    }
    return make(map[string]int64)
}
```

#### 3c. Update `InitMonikerMap` to load `first_active_block`

The `InitMonikerMap` function already accepts `chainID string` as a parameter.
Extend it to also populate `FirstActiveBlockMap`:

```go
func InitMonikerMap(db *gorm.DB, chainID string) error {
    // ... existing code to fetch addr_monikers WHERE chain_id = ? ...
    for _, am := range addressMonikers {
        SetMoniker(chainID, am.Addr, am.Moniker)
        SetFirstActiveBlock(chainID, am.Addr, am.FirstActiveBlock)  // NEW
    }
    return nil
}
```

---

### Phase 4 — Modify `BackfillParallel` and `BackfillRange`

Both functions already accept `chainID string` and receive a pre-scoped `monikerMap map[string]string`.

At the start of each function, load the `first_active_block` map:

```go
firstActiveBlocks := GetFirstActiveBlockMap(chainID)
```

In the "participated false" loop, apply the guard:

```go
for addr, mon := range monikerMap {
    if _, ok := seen[addr]; ok {
        continue // already added as participated=true
    }
    fab := firstActiveBlocks[addr]  // -1 if unknown
    if fab > 0 && j.H < fab {
        continue // validator not yet active at this block
    }
    rows = append(rows, dpRow{
        ChainID:      chainID,
        BlockHeight:  j.H,
        Addr:         addr,
        Participated: false,
        ...
    })
}
```

When a validator appears in the precommits of a block (`participated = true`) and their `first_active_block == -1`:

```go
for addr := range precommits {
    fab := firstActiveBlocks[addr]
    if fab == -1 {
        firstActiveBlocks[addr] = j.H  // record activation
        SetFirstActiveBlock(chainID, addr, j.H)
        db.Exec(`UPDATE addr_monikers SET first_active_block = ? WHERE chain_id = ? AND addr = ?`, j.H, chainID, addr)
    }
    // ... insert participated=true row ...
}
```

---

### Phase 5 — Modify `CollectParticipation` (real-time)

The `CollectParticipation` function (in `gnovalidator_realtime.go`) already calls
`GetMonikerMap(chainID)` to get the chain-scoped moniker map.

In the "participated false" loop, apply the same guard:

```go
monikerMap := GetMonikerMap(chainID)
for valAddr, moniker := range monikerMap {
    fab := GetFirstActiveBlock(chainID, valAddr)
    if fab > 0 && currentHeight < fab {
        continue // validator not yet active
    }
    // insert participated=false row
}
```

When a validator appears in the current block's precommits (`participated = true`)
and their `first_active_block == -1`:

```go
for valAddr := range precommits {
    fab := GetFirstActiveBlock(chainID, valAddr)
    if fab == -1 {
        SetFirstActiveBlock(chainID, valAddr, currentHeight)
        db.Exec(`UPDATE addr_monikers SET first_active_block = ? WHERE chain_id = ? AND addr = ?`, currentHeight, chainID, valAddr)
    }
    // ... insert participated=true row ...
}
```

---

### Phase 6 — Cleanup existing data (one-shot migration)

After deployment and validation in staging, delete the spurious rows already in the database:

```sql
DELETE FROM daily_participations
WHERE participated = 0
  AND EXISTS (
      SELECT 1 FROM addr_monikers am
      WHERE am.addr = daily_participations.addr
        AND am.chain_id = daily_participations.chain_id
        AND am.first_active_block > 0
        AND daily_participations.block_height < am.first_active_block
  );
```

The DELETE is chain-scoped via the join on both `chain_id` and `addr`.

**Warning:** Heavy query on a large table — run outside of production
or in maintenance mode (WAL mode preserves concurrent reads).

---

## Summary of files to modify

| File | Change |
| --- | --- |
| `database/db_init.go` | Add `FirstActiveBlock int64` field to `AddrMoniker` struct |
| `database/db_init.go` | One-shot SQL population query at startup (Phase 2) |
| `database/db.go` | Add `UpsertFirstActiveBlock(db, chainID, addr, block)` function |
| `gnovalidator/valoper.go` | Add `FirstActiveBlockMap` global map and mutex |
| `gnovalidator/valoper.go` | Add thread-safe helpers: `GetFirstActiveBlock`, `SetFirstActiveBlock`, `GetFirstActiveBlockMap` |
| `gnovalidator/valoper.go` | Update `InitMonikerMap`: load `first_active_block` from DB into `FirstActiveBlockMap` |
| `gnovalidator/sync.go` | Update `BackfillParallel` + `BackfillRange`: call `GetFirstActiveBlockMap(chainID)`, apply guard in participated=false loop, dynamic detection on participated=true |
| `gnovalidator/gnovalidator_realtime.go` | Update `CollectParticipation`: call `GetFirstActiveBlock(chainID, addr)`, apply guard in participated=false loop, dynamic detection on participated=true |

---

## Recommended implementation order

1. **Phase 1** — Struct field + AutoMigrate (non-breaking)
2. **Phase 2** — Populate at startup (idempotent SQL query)
3. **Phase 3** — In-memory cache + helpers + `InitMonikerMap` update (preparation for Phases 4–5)
4. **Phase 4** — Corrected backfill (guards + dynamic detection)
5. **Phase 5** — Corrected real-time (guards + dynamic detection)
6. **Phase 6** — Data cleanup (after staging validation)
