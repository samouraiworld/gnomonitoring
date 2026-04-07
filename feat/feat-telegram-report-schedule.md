# Feat: Change daily report schedule from Telegram

## Problem

The daily report schedule (hour, minute, timezone) can only be changed via the admin panel API.
From the Telegram bot, users can only activate or deactivate the report (`/report activate=true/false`).
There is no way to change the time of the report from Telegram.

---

## Goal

Extend the existing `/report` command to accept `hour`, `minute`, and `timezone` parameters so users
can update their report schedule directly from the Telegram chat:

```
/report hour=8 minute=30 timezone=Europe/Paris
```

---

## Existing infrastructure (no new DB migration needed)

All the moving parts are already in place:

| Component | Location | Role |
|---|---|---|
| `telegram_hour_reports` table | DB (existing) | Stores `(chat_id, chain_id, daily_report_hour, daily_report_minute, timezone, activate)` |
| `database.UpdateTelegramScheduleAdmin` | `internal/database/db_admin.go:248` | Updates any combination of the five columns for a `(chat_id, chain_id)` row |
| `scheduler.Schedulerinstance.ReloadForTelegram` | `internal/scheduler/scheduler.go:129` | Reads new config from DB and replaces the running goroutine with the new schedule |
| `/report` handler | `internal/telegram/validator.go:264` | Currently only handles `activate=` — to be extended |
| `reportActivate` helper | `internal/telegram/validator.go:572` | Handles activate/deactivate — schedule change is a separate code path |

---

## Implementation plan

### Step 1 — Extend `/report` handler to detect schedule params

**File:** `internal/telegram/validator.go`

In the `/report` handler, after extracting `params`, check for `hour` and/or `minute` alongside
`activate`. Route to a new `reportSchedule` helper when at least one schedule param is present:

```go
"/report": func(chatID int64, args string) {
    chainID := getActiveChain(chatID, defaultChainID)
    params := parseParams(args)

    // Schedule change: hour= or minute= present
    if params["hour"] != "" || params["minute"] != "" || params["timezone"] != "" {
        msg := reportSchedule(db, schedulerInstance, chatID, chainID, params)
        _ = SendMessageTelegram(token, chatID, msg)
        return
    }

    // Existing activate/deactivate path
    activate := params["activate"]
    msg, err := reportActivate(db, chatID, chainID, activate)
    if err != nil {
        log.Printf("[telegram] report activate error: %v", err)
    }
    _ = SendMessageTelegram(token, chatID, msg)
},
```

### Step 2 — Add `reportSchedule` helper

**File:** `internal/telegram/validator.go`

```go
// reportSchedule updates the daily report schedule for a Telegram chat.
// It reads the current row from DB, applies the provided overrides, persists,
// and triggers a scheduler reload so the new time takes effect immediately.
func reportSchedule(db *gorm.DB, sched *scheduler.Scheduler, chatID int64, chainID string, params map[string]string) string {
    // 1. Read current schedule (so unspecified params keep their current value).
    current, err := database.GetHourTelegramReport(db, chatID, chainID)
    if err != nil {
        return "⚠️ No active report schedule found. Use <code>/report activate=true</code> first."
    }

    hour   := current.DailyReportHour
    minute := current.DailyReportMinute
    tz     := current.Timezone

    // 2. Apply overrides.
    if v := params["hour"]; v != "" {
        h, err := strconv.Atoi(v)
        if err != nil || h < 0 || h > 23 {
            return "⚠️ Invalid hour. Must be an integer between 0 and 23."
        }
        hour = h
    }
    if v := params["minute"]; v != "" {
        m, err := strconv.Atoi(v)
        if err != nil || m < 0 || m > 59 {
            return "⚠️ Invalid minute. Must be an integer between 0 and 59."
        }
        minute = m
    }
    if v := params["timezone"]; v != "" {
        if _, err := time.LoadLocation(v); err != nil {
            return fmt.Sprintf("⚠️ Unknown timezone <code>%s</code>. Use IANA format, e.g. <code>Europe/Paris</code>.", html.EscapeString(v))
        }
        tz = v
    }

    // 3. Persist the new schedule (keep current activate flag).
    if err := database.UpdateTelegramScheduleAdmin(db, chatID, chainID, hour, minute, tz, current.Activate); err != nil {
        return fmt.Sprintf("❌ Failed to update schedule: %v", err)
    }

    // 4. Reload the scheduler goroutine with the new time.
    if err := sched.ReloadForTelegram(chatID, chainID, db); err != nil {
        log.Printf("[telegram] scheduler reload failed (chat %d chain %s): %v", chatID, chainID, err)
        // Non-fatal: schedule is persisted; goroutine will pick it up on next restart.
    }

    return fmt.Sprintf(
        "✅ Report schedule updated (chain: <code>%s</code>)\nTime: <b>%02d:%02d</b> — Timezone: <code>%s</code>",
        html.EscapeString(chainID), hour, minute, html.EscapeString(tz),
    )
}
```

### Step 3 — Use existing `database.GetHourTelegramReport`

**No new function needed.**

`GetHourTelegramReport(db *gorm.DB, chatID int64, chainID string) (*TelegramHourReport, error)`
already exists in `internal/database/db_telegram.go` (line 168) and returns exactly the row needed
(`TelegramHourReport` with `DailyReportHour`, `DailyReportMinute`, `Timezone`, `Activate`).

`TelegramHourReport` model (defined in `db_init.go`):
```go
type TelegramHourReport struct {
    ChatID            int64  `gorm:"primaryKey;column:chat_id"`
    ChainID           string `gorm:"primaryKey;column:chain_id;default:betanet"`
    DailyReportHour   int    `gorm:"column:daily_report_hour;default:9"`
    DailyReportMinute int    `gorm:"column:daily_report_minute;default:0"`
    Activate          bool   `gorm:"column:activate;default:true"`
    Timezone          string `gorm:"column:timezone;default:Europe/Paris"`
}
```

### Step 4 — Pass the scheduler instance to `BuildTelegramHandlers`

**File:** `internal/telegram/validator.go` + `main.go`

The `/report` handler needs access to `scheduler.Schedulerinstance` to call `ReloadForTelegram`.
The scheduler is already accessible in `main.go` as `scheduler.Schedulerinstance`.

Option A (simplest): add a package-level variable in `telegram/validator.go`:

```go
// Set by main.go before building handlers.
var SchedulerInstance *scheduler.Scheduler
```

And set it in `main.go`:

```go
telegram.SchedulerInstance = scheduler.Schedulerinstance
```

Option B: add a `sched *scheduler.Scheduler` parameter to `BuildTelegramHandlers`. More explicit but
requires updating all callers. Prefer option A to minimise blast radius.

### Step 5 — Update `formatHelp`

**File:** `internal/telegram/validator.go`

Add schedule change examples to the `/report` section in `formatHelp()`:

```
📬 /report [activate=true|false] [hour=H] [minute=M] [timezone=TZ]
Configure the daily report.
• /report                               — show current status
• /report activate=true                 — enable daily report
• /report activate=false                — disable daily report
• /report hour=8 minute=30             — set report time (keeps current timezone)
• /report hour=8 minute=0 timezone=Europe/Paris — set time and timezone
```

---

## Validation rules

| Param | Valid range | Error message |
|---|---|---|
| `hour` | 0–23 integer | "Invalid hour. Must be an integer between 0 and 23." |
| `minute` | 0–59 integer | "Invalid minute. Must be an integer between 0 and 59." |
| `timezone` | Must pass `time.LoadLocation` | "Unknown timezone. Use IANA format, e.g. Europe/Paris." |

---

## Edge cases

- **No existing row**: `GetHourTelegramReport` returns an error → prompt user to run `/report activate=true` first (which creates the row via `ActivateTelegramReport`). The error message must say explicitly: "No active report schedule found. Run `/report activate=true` first, then set the schedule."
- **Scheduler reload fails**: non-fatal — schedule is persisted in DB, goroutine will pick it up on next process restart. Log the error and return a success message (the update worked even if reload failed).
- **Partial update**: only specified params are overridden; unspecified ones keep their current DB values. E.g. `/report hour=9` changes only the hour, minute and timezone stay the same.
- **`activate` and schedule in the same command**: `/report activate=true hour=8` triggers the schedule path only (schedule params take priority). If the row does not exist yet, `GetHourTelegramReport` returns an error and the user is told to activate first — activation does not happen silently. The user must run `/report activate=true` then `/report hour=8` as two separate commands.

---

## Files changed

| File | Change |
|---|---|
| `internal/telegram/validator.go` | Extend `/report` handler routing; add `reportSchedule`; update `formatHelp`; add `SchedulerInstance` package var |
| `internal/database/db_telegram.go` | No change — `GetHourTelegramReport` already exists and is reused |
| `main.go` | Set `telegram.SchedulerInstance = scheduler.Schedulerinstance` |

No DB migration. No new dependency.
