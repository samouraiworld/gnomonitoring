# Fix Pagination — Analysis and Fixes

## Context

Pagination via inline buttons has been implemented in `backend/internal/telegram/validator.go` and `backend/internal/telegram/telegram.go`. It works correctly from a functional standpoint, but produces abnormally high CPU usage on each page navigation.

---

## Core problem

Each click on ⬅️ Prev or ➡️ Next triggers this full chain:

```text
callback → buildPaginatedResponse
         → formatUptime / formatParticipationRAte / FormatTxcontrib / ...
         → database.*Metrics(db)   ← FULL TABLE SCAN each time
         → sort.Slice(all, ...)    ← FULL IN-MEMORY SORT in Go
         → filter*(all, filter)    ← FULL IN-MEMORY FILTER in Go
         → paginate(len, page, limit) → slice [start:end]
```

Participation data does not change every second. Yet navigating from page 1 to page 3 triggers 3 full SQL queries + 3 sorts + 3 identical filters.

---

## Problem 1 — `http.Client` recreated on every Telegram call

**File**: `backend/internal/telegram/telegram.go`

**Location**: lines 88, 132, 174, 208 — in each HTTP function (`SendMessageTelegram`, `SendMessageTelegramWithMarkup`, `EditMessageTelegramWithMarkup`, `AnswerCallbackQuery`)

**Current code**:

```go
client := &http.Client{Timeout: 10 * time.Second}
```

**Problem**: Each navigation generates 3 HTTP calls to the Telegram API (AnswerCallbackQuery + EditMessageText + potentially another). Each one instantiates a new `http.Client`, preventing any TCP connection pool reuse (`keep-alive`). The TCP transport is re-established on every call.

**Impact**: High — this is the main source of unnecessary CPU usage.

**Fix**: Declare a shared client at package level:

```go
// add at the top of telegram.go, with the other vars
var telegramHTTPClient = &http.Client{Timeout: 10 * time.Second}
```

Then replace the 4 local instantiations with `telegramHTTPClient`.

---

## Problem 2 — `AnswerCallbackQuery` does not close `resp.Body`

**File**: `backend/internal/telegram/telegram.go`

**Location**: function `AnswerCallbackQuery`, line ~210

**Current code**:

```go
resp, err := client.Do(req)
if err != nil {
    return fmt.Errorf("do request: %w", err)
}
return nil  // ← resp.Body never closed
```

**Problem**: Without `defer resp.Body.Close()`, the TCP connection is never returned to the transport pool. With many button clicks, file descriptors accumulate until exhaustion.

**Impact**: High — progressive connection leak.

**Fix**:

```go
resp, err := client.Do(req)
if err != nil {
    return fmt.Errorf("do request: %w", err)
}
defer resp.Body.Close()
return nil
```

---

## Problem 3 — No cache between pages

**Files**: `backend/internal/telegram/validator.go`

**Location**: all `format*` functions — `formatUptime`, `formatParticipationRAte`, `FormatTxcontrib`, `formatMissing`, `formatOperationTime`

**Problem**: A user navigating pages 1 → 2 → 3 on `/uptime` triggers 3 full calls to `database.UptimeMetricsaddr(db)`. The data does not change at this time scale.

**Impact**: Medium — redundant, but acceptable with few concurrent users.

**Fix**: In-memory cache with TTL of 30 to 60 seconds, key `(cmdKey, period)`:

```go
type cacheEntry struct {
    data      any
    expiresAt time.Time
}

var (
    metricsCache   = map[string]cacheEntry{}
    metricsCacheMu sync.Mutex
)

func getCached(key string) (any, bool) {
    metricsCacheMu.Lock()
    defer metricsCacheMu.Unlock()
    e, ok := metricsCache[key]
    if !ok || time.Now().After(e.expiresAt) {
        return nil, false
    }
    return e.data, true
}

func setCached(key string, data any, ttl time.Duration) {
    metricsCacheMu.Lock()
    defer metricsCacheMu.Unlock()
    metricsCache[key] = cacheEntry{data: data, expiresAt: time.Now().Add(ttl)}
}
```

Usage in `formatUptime`:

```go
const cacheTTL = 45 * time.Second

func formatUptime(db *gorm.DB, page, limit int, filter, sortOrder string) (...) {
    key := "uptime"
    var results []database.UptimeMetrics
    if cached, ok := getCached(key); ok {
        results = cached.([]database.UptimeMetrics)
    } else {
        results, err = database.UptimeMetricsaddr(db)
        if err != nil { ... }
        setCached(key, results, cacheTTL)
    }
    // rest unchanged...
}
```

---

## Problem 4 — Sort and filter: two Go passes instead of SQL

**Files**: `backend/internal/telegram/validator.go` + `backend/internal/database/db_metrics.go`

**Location**: all `format*` functions

**Problem**: Sorting (`sort.Slice`) and filtering (`filter*`) are done in Go over the entire result set. The order is inconsistent across commands (some filter before sorting, others after). With growing data, this will become costly.

**Impact**: Medium — acceptable today with ~50 validators, problematic at scale.

**Ideal fix**: Push `ORDER BY`, `WHERE moniker LIKE ?`, `LIMIT ? OFFSET ?` directly into the SQL queries in `db_metrics.go`. Example for `UptimeMetricsaddr`:

```go
func UptimeMetricsaddr(db *gorm.DB, filter string, sortOrder string, limit, offset int) ([]UptimeMetrics, error) {
    var results []UptimeMetrics
    order := "DESC"
    if sortOrder == "asc" {
        order = "ASC"
    }
    q := db.Raw(`
        SELECT ... FROM ...
        WHERE (? = '' OR moniker LIKE ? OR addr LIKE ?)
        ORDER BY uptime ` + order + `
        LIMIT ? OFFSET ?
    `, filter, "%"+filter+"%", "%"+filter+"%", limit, offset)
    return results, q.Scan(&results).Error
}
```

This removes the `sort.Slice` and `filter*` calls from `validator.go` and pushes the work to SQLite which uses its indexes.

**Minimal fix (without touching DB signatures)**: At least combine filter and sort in a single pass, and ensure the filter is applied before sorting in all commands (consistency).

---

## Problem 5 — `searchState` not proactively purged

**File**: `backend/internal/telegram/validator.go`

**Location**: variable `searchState` line ~800, function `HandleSearchInput`

**Problem**: Expired states are only removed when the same `chatID` sends a new message. The map grows without bound over time.

**Impact**: Low — slow memory leak.

**Fix**: Launch a cleanup ticker at bot startup:

```go
func startSearchStateCleanup() {
    go func() {
        ticker := time.NewTicker(5 * time.Minute)
        defer ticker.Stop()
        for range ticker.C {
            searchStateMu.Lock()
            for chatID, s := range searchState {
                if time.Now().After(s.ExpiresAt) {
                    delete(searchState, chatID)
                }
            }
            searchStateMu.Unlock()
        }
    }()
}
```

Call from `BuildTelegramHandlers` or `BuildTelegramCallbackHandler`.

---

## Summary by priority

| # | Problem | File | Impact | Difficulty |
| --- | -------- | ------- | ------ | ---------- |
| 1 | `http.Client` recreated on each call | `telegram.go` | **High** | Trivial (1 line) |
| 2 | `resp.Body` not closed in `AnswerCallbackQuery` | `telegram.go` | **High** | Trivial (1 line) |
| 3 | No cache between pages | `validator.go` | Medium | ~30 lines |
| 4 | Sort + filter in Go instead of SQL | `validator.go` + `db_metrics.go` | Medium | Significant refactor |
| 5 | `searchState` not purged | `validator.go` | Low | ~15 lines |

## Recommended application order

1. **Apply 1 and 2 immediately** — one-line fixes, zero risk, immediate gain.
2. **Apply 5** — quick win, prevents the memory leak.
3. **Apply 3** — reduces redundant DB queries during navigation.
4. **Apply 4** — deeper refactor, to be done on a dedicated branch.
