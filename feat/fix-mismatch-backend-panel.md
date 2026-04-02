# Fix: Backend ↔ Panel JSON Mismatch

**Branch:** `feat/admin-panel`  
**Files concerned:**
- `backend/internal/database/db_init.go` — struct definitions
- `backend/internal/database/db_admin.go` — admin-specific DTOs
- `panel/src/types/api.ts` — TypeScript interfaces

---

## Root cause

Most Go structs in `db_init.go` lacked `json:` tags. Go's `encoding/json` then serializes using the exact field name in PascalCase, while the frontend expects snake_case. Every struct without json tags produced mismatched JSON keys.

---

## Status per endpoint

| Endpoint | Struct | Status |
| --- | --- | --- |
| `GET /admin/status` | inline `chainStatus` | ✅ OK (has json tags) |
| `GET /admin/chains` | inline `chainInfo` | ✅ OK (has json tags) |
| `GET /admin/webhooks` | `WebhookAdmin` | ✅ OK (has json tags) |
| `GET /admin/govdao/proposals` | `Govdao` | ✅ OK (frontend uses PascalCase intentionally) |
| `GET /admin/users` | `User` | ✅ OK (has json tags) |
| `GET /admin/alerts` | `AlertLog` | ✅ FIXED (json tags added) |
| `GET /admin/monikers` | `AddrMoniker` | ✅ FIXED (json tags added) |
| `GET /admin/schedules` | `HourReportAdmin` | ✅ FIXED (separate DTO in db_admin.go) |
| `GET /admin/telegram/chats` | `Telegram` | ✅ FIXED (json tags added, typo fixed, ChatTitle field added) |
| `GET /admin/telegram/subs` | `TelegramValidatorSub` | ✅ FIXED (json tags added, CreatedAt hidden) |
| `GET /admin/telegram/schedules` | `TelegramHourReport` | ✅ FIXED (json tags added) |

---

## Detailed fixes

### 1. `AlertLog` — `db_init.go:115`

**Added json tags to all fields:**

| Field Name | JSON Tag |
| --- | --- |
| `ID` | (no change) |
| `ChainID` | `json:"chain_id"` |
| `Addr` | `json:"addr"` |
| `Moniker` | `json:"moniker"` |
| `Level` | `json:"level"` |
| `StartHeight` | `json:"start_height"` |
| `EndHeight` | `json:"end_height"` |
| `Skipped` | `json:"skipped"` |
| `Msg` | `json:"msg"` |
| `SentAt` | `json:"sent_at"` |
| `ResolvedAt` | `json:"resolved_at"` |

**Status:** ✅ Fixed — all fields now serialize to snake_case.

---

### 2. `AddrMoniker` — `db_init.go:128`

**Added json tags to all fields:**

| Field Name | JSON Tag |
| --- | --- |
| `ID` | (no change, not used in frontend rendering) |
| `ChainID` | `json:"chain_id"` |
| `Addr` | `json:"addr"` |
| `Moniker` | `json:"moniker"` |
| `FirstActiveBlock` | `json:"first_active_block"` |

**Status:** ✅ Fixed — all fields now serialize to snake_case.

---

### 3. `HourReport` — `db_init.go:56`

**Approach:** Left unchanged to avoid breaking the general API. Instead, a separate DTO `HourReportAdmin` was created in `db_admin.go` exclusively for the admin panel.

**`HourReportAdmin` (new struct in db_admin.go) has json tags:**

| Field Name | JSON Tag |
| --- | --- |
| `UserID` | `json:"user_id"` |
| `DailyReportHour` | `json:"daily_report_hour"` |
| `DailyReportMinute` | `json:"daily_report_minute"` |
| `Timezone` | `json:"timezone"` |

**Status:** ✅ Fixed — admin panel uses `HourReportAdmin` with proper json tags, while the public API remains unchanged.

---

### 4. `Telegram` — `db_init.go:21`

**Added json tags to all fields and fixed existing issues:**

| Field Name | JSON Tag | Notes |
| --- | --- | --- |
| `ChatID` | `json:"chat_id"` | Added |
| `Type` | `json:"chat_type"` | Added; field name remains `Type` in Go |
| `ChainID` | `json:"chain_id"` | Added |
| `ChatTitle` | `json:"chat_title"` | New field added to store Telegram chat title |

**Bug fixes:**

- Fixed GORM typo: `olumn:type` → `column:type`
- Added `ChatTitle string` field (GORM will auto-migrate the column to the `telegrams` table on startup)

**Status:** ✅ Fixed — all fields now serialize to snake_case, column name bug fixed, and chat title field added.

---

### 5. `TelegramValidatorSub` — `db_init.go:35`

**Added json tags to all fields:**

| Field Name | JSON Tag | Notes |
| --- | --- | --- |
| `ID` | (no change) | |
| `ChatID` | `json:"chat_id"` | Added |
| `ChainID` | `json:"chain_id"` | Added |
| `Moniker` | `json:"moniker"` | Added |
| `Addr` | `json:"addr"` | Added |
| `Activate` | `json:"activate"` | Added |
| `CreatedAt` | `json:"-"` | Hidden from JSON output (not used in frontend) |

**Status:** ✅ Fixed — all serializable fields now use snake_case, and CreatedAt is hidden.

---

### 6. `TelegramHourReport` — `db_init.go:26`

**Added json tags to all fields:**

| Field Name | JSON Tag |
| --- | --- |
| `ChatID` | `json:"chat_id"` |
| `ChainID` | `json:"chain_id"` |
| `DailyReportHour` | `json:"daily_report_hour"` |
| `DailyReportMinute` | `json:"daily_report_minute"` |
| `Activate` | `json:"activate"` |
| `Timezone` | `json:"timezone"` |

**Note:** This struct uses a composite key `chat_id + chain_id` without an autoincrement ID. The frontend uses `key={`${s.chat_id}-${s.chain_id}`}` for React rendering.

**Status:** ✅ Fixed — all fields now serialize to snake_case.

---

## Confirmed non-issues

- **`WebhookAdmin`** — all fields have correct json tags ✅
- **`Govdao`** — frontend intentionally uses PascalCase (`p.Id`, `p.Title`, `p.Tx`, etc.), matching Go serialization without json tags ✅
- **`User`** — all fields have correct json tags ✅
- **Status / Chains** — inline structs with correct json tags ✅
- **`AlertConfig`** — returns `map[string]string` with database keys, direct correspondence ✅

---

## Implementation summary

All fixes were applied in **`backend/internal/database/db_init.go`**, except for the admin schedules endpoint which uses a separate DTO `HourReportAdmin` in **`backend/internal/database/db_admin.go`**.

### Changes made

1. **`AlertLog`** — Added json tags to all fields (impact: Dashboard, AlertHistory)
2. **`AddrMoniker`** — Added json tags to all fields (impact: Monikers page)
3. **`TelegramValidatorSub`** — Added json tags, hidden CreatedAt from JSON (impact: Telegram subscriptions)
4. **`TelegramHourReport`** — Added json tags to all fields (impact: Telegram schedules)
5. **`Telegram`** — Added json tags, fixed GORM typo (`olumn` → `column`), added `ChatTitle` field (impact: Telegram chats list)
6. **`HourReport`** — Left unchanged in `db_init.go` to preserve API stability. Created `HourReportAdmin` DTO in `db_admin.go` with proper json tags for admin panel use (impact: Admin panel schedules endpoint)

### Key notes

- Adding json tags does not affect GORM (which uses gorm tags or column names). The changes are backward-compatible with the database.
- The `Telegram.Type` field name remains `Type` in Go; only the JSON serialization key changes to `chat_type`.
- The `ChatTitle` field was added to the `Telegram` struct and GORM will auto-migrate the SQLite column on startup.
- The `TelegramValidatorSub.CreatedAt` field is hidden from JSON output with `json:"-"` since the frontend does not use it.
- The `HourReportAdmin` DTO ensures the admin panel receives snake_case json while the public API remains unchanged.
