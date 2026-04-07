# feat: interactive command menu (`/cmd`)

## Objective

Introduce a `/cmd` command that opens a guided inline-keyboard menu, allowing users to execute
existing commands without typing parameters manually.

This improves UX for new users while keeping all existing text commands intact.

---

## Existing infrastructure (already present — do NOT reinvent)

| Component | Location | Role |
| --- | --- | --- |
| `BuildTelegramCallbackHandler()` | `validator.go:788` | Returns `func(chatID int64, msgID int, data string)` — already wired in `main.go` |
| `StartCommandLoop()` callback dispatch | `telegram.go` | Already calls `callbackHandler(chatID, messageID, callbackData)` for `CallbackQuery` updates |
| `encodeCallbackData()` / `parseCallbackData()` | `validator.go:955,929` | URL-style compact encoding (`c=st&p=1&l=10`) to stay under 64-byte Telegram limit |
| `SendMessageTelegramWithMarkup()` | `telegram.go` | Sends a message with `InlineKeyboardMarkup` |
| `EditMessageTelegramWithMarkup()` | `telegram.go` | Edits existing message in-place (avoids chat spam) |
| `buildPaginationMarkup()` | `validator.go:896` | Builds Prev/Next pagination buttons for DB-paginated results |
| `buildSecondaryButtons()` | `validator.go:991` | Builds Sort and Search secondary buttons |
| `searchState map[int64]SearchState` | `validator.go:1131` | In-memory TTL state (2 min) with 5-min cleanup goroutine |
| `chatChainState map[int64]string` | `validator.go:99` | Per-chat active chain — already used by all command handlers |
| `cmdToCode` / `codeToCmd` | `validator.go:1030,1049` | Encode/decode command keys to short codes |

**No new state storage mechanism needed.** Add a parallel `CmdState` struct following the same
TTL pattern as `SearchState`.

---

## Scope

This feature targets the **validator bot only** (`validator.go`).
The GovDAO bot (`govdao.go`) has `nil` as its callback handler in `main.go` and has no inline
keyboard support — add it later as a separate feature.

---

## New command

```
/cmd
```

Opens the main menu. No arguments.

---

## Interactive flow

### Step 1 — Command selection

```
What do you want to do?
[Subscribe]  [Report]
[Validators] [Chain]
```

Callback data: `c=mn&a=sb`, `c=mn&a=rp`, `c=mn&a=vl`, `c=mn&a=ch`

### Step 2 — Action selection (command-specific)

**Subscribe:** `[ON]  [OFF]`
**Report:** `[Enable]  [Disable]  [Change schedule]`
**Validators:** `[Status]  [Uptime]  [Rate]`
**Chain:** show available chains (from `enabledChains`)

### Step 3 — Validator selection (subscribe only)

Show validators from `GetMonikerMap(chainID)` as inline buttons, 5 per page.
Include `[All validators]` shortcut.

**Important**: validator addresses are ~45 bytes each and cannot be put in callback data (64-byte
Telegram limit). Instead, each button encodes only a short index (`c=mn&a=vsel&i=3`). The
`CmdState` struct holds the ordered list of addresses for the current page so the index can be
resolved server-side. Include `[All validators]` as `i=-1`.

A custom button builder `buildValidatorSelectMarkup` is needed — `buildPaginationMarkup` works
only for DB-paginated results and cannot be reused here.

### Step 4 — Confirmation

Edit same message via `EditMessageTelegramWithMarkup` to show summary:

```
Subscribe ON
Validators: moniker1, moniker2
Chain: betanet

[✅ Confirm]  [❌ Cancel]
```

Callback data: `c=mn&a=cf` / `c=mn&a=cx`

### Step 5 — Execution

On confirm: translate session state into the equivalent command string and call the existing
handler directly (e.g., `handleSubscribe(token, db, chatID, chainID, "on g1abc g1def")`).
This reuses all existing logic without duplication.

Note: `handleSubscribe` signature is `(token string, db *gorm.DB, chatID int64, chainID, args string)`.

---

## Session state

```go
type CmdState struct {
    Step           string    // "root", "action", "validators", "confirm"
    Command        string    // "subscribe", "report", "validators", "chain"
    Action         string    // "on", "off", "status", "schedule", etc.
    ChainID        string
    ValidatorPage  []string  // ordered addresses shown on current validator-select page
    SelectedAddrs  []string  // addresses confirmed by user
    Period         string
    ExpiresAt      time.Time
}

var cmdState   = map[int64]CmdState{}
var cmdStateMu sync.Mutex
```

TTL: 5 minutes (longer than `searchTTL` since multi-step flow takes more time).

**Cleanup**: modify `startSearchStateCleanup()` (line 1139) to also purge expired `cmdState`
entries in the same ticker loop. It currently only cleans `searchState` — adding `cmdState`
requires a small change to that function.

---

## Callback data encoding

Reuse `encodeCallbackData()` / `parseCallbackData()`. Add new command codes to `cmdToCode` /
`codeToCmd`:

| Logical value | Code | Notes |
| --- | --- | --- |
| `menu` (entry point) | `mn` | new |
| `confirm` | `cf` | new — avoid `ok` (ambiguous with Go bool returns) |
| `cancel` | `cx` | new |
| `subscribe` | `sb` | new |
| `report` | `rp` | new |
| `validators` (menu category) | `vl` | new |
| `chain` (menu category) | `ch` | new |
| `vsel` (validator select) | `vs` | new — `i=N` carries the index |

Keep total encoded size under ~45 bytes to leave room for index and action fields.

**Note**: `parseCallbackData` currently returns `!ok` when `cmdKey == ""` (i.e., when `codeToCmd`
returns `""`). The new codes must be added to both `cmdToCode` and `codeToCmd` before the routing
works — otherwise all menu callbacks are silently dropped.

---

## Refactor `BuildTelegramCallbackHandler` to add dispatch

**This is a required structural change.** The current handler (line 788) calls
`buildPaginatedResponse` directly after the `"search"` check, with no dispatch mechanism:

```go
// current — no room for menu handling
if action == "search" { ... return }
msg, markup, err := buildPaginatedResponse(...)
```

Refactor to a switch on `cmdKey` before falling through to `buildPaginatedResponse`:

```go
cmdKey, page, limit, period, filter, sortOrder, action, ok := parseCallbackData(data)
if !ok {
    return
}

switch cmdKey {
case "menu":
    handleCmdMenuCallback(token, db, chatID, messageID, action, params)
    return
}

// existing paths below (search + paginated response) unchanged
if action == "search" { ... }
msg, markup, err := buildPaginatedResponse(...)
```

`handleCmdMenuCallback` reads/writes `cmdState` and calls `EditMessageTelegramWithMarkup` to
update the message in place at each step.

The `params` map (raw key-value from `parseCallbackParams`) is passed to
`handleCmdMenuCallback` so it can read `i=N` (validator index) without going through the typed
`parseCallbackData` return values.

---

## New helper: `buildValidatorSelectMarkup`

`buildPaginationMarkup` builds buttons for DB-paginated results — it cannot be reused for
validator selection. Add a dedicated builder:

```go
// buildValidatorSelectMarkup returns buttons for the validator-select step.
// addrs is the full list; page and pageSize control which slice is shown.
// Each button encodes only the index within the current page (not the address).
func buildValidatorSelectMarkup(addrs []string, monikers map[string]string, page, pageSize int) (*InlineKeyboardMarkup, []string)
// returns (markup, currentPageAddrs)
// currentPageAddrs is saved to CmdState.ValidatorPage so the index can be resolved on tap.
```

---

## Wiring into existing callback handler

No changes needed to `StartCommandLoop`, `main.go`, or the polling loop.

The only wiring change is the refactor of `BuildTelegramCallbackHandler` described above.

---

## Help text

Add to `formatHelp()`:

```
🎛 /cmd — Interactive command menu (guided step-by-step)
```

---

## Files changed

| File | Change |
| --- | --- |
| `internal/telegram/validator.go` | Add `/cmd` to handler map; add `CmdState` struct + helpers; add `handleCmdMenuCallback` + `buildValidatorSelectMarkup`; refactor `BuildTelegramCallbackHandler` dispatch; extend `cmdToCode`/`codeToCmd`; modify `startSearchStateCleanup`; update `formatHelp` |
| `main.go` | No change needed |

No new dependency. No DB migration. No Redis.

---

## Constraints & edge cases

- **64-byte callback limit**: validator addresses never go in callback data — only short indexes
  resolved via `CmdState.ValidatorPage`.
- **Expired state**: if `CmdState` not found for a `chatID`, send "Session expired — run `/cmd` again."
- **Invalid callback data**: `parseCallbackData` returns `!ok` on malformed input; the new `menu`
  branch must also handle missing `CmdState` gracefully.
- **Concurrent taps**: protect `cmdState` with `cmdStateMu` (same `sync.Mutex` pattern as
  `searchStateMu`).
- **GovDAO bot**: out of scope — no callback handler wired there yet.

---

## Backward compatibility

All existing text commands (`/subscribe`, `/chain`, `/report`, etc.) remain unchanged.
`/cmd` is an additional entry point, not a replacement.

---

## Implementation status

### ✅ Done (first pass)

- `CmdState` struct + `setCmdState`/`getCmdState`/`deleteCmdState` helpers (5-min TTL)
- `cmdState` cleanup added inside `startSearchStateCleanup` ticker loop
- `encodeCmdCallback` helper
- `buildCmdRootMarkup` — 4-button root menu (Subscribe / Report / Validators / Chain)
- `buildValidatorSelectMarkup` — paginated validator selection (index-only in callback data)
- `BuildTelegramCallbackHandler` refactored — dispatches `c=mn` to `handleCmdMenuCallback`
- `handleCmdMenuCallback` — full step routing (root → action → validator select → confirm/cancel)
- `buildActionMarkup` — per-command inline keyboards
- `executeCmdMenuAction` — executes confirmed action via existing handlers
- `/cmd` added to `BuildTelegramHandlers` handler map
- `formatHelp` updated
- New codes in `cmdToCode`/`codeToCmd`: `mn`, `cf`, `cx`, `sb`, `rp`, `vl`, `ch`, `vs`

### ❌ Bug fixes needed

#### Bug 1 — `missing` and `operation_time` missing from validators menu

**Location:** `buildActionMarkup` (validators case) + `executeCmdMenuAction` (validators case)

`buildActionMarkup` for `"validators"` only offers `status`, `uptime`, `rate`.
`executeCmdMenuAction` only handles those three.

**Fix:** Add `missing` and `operation_time` buttons to `buildActionMarkup`:

```go
case "validators":
    return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
        {
            {Text: "Status",         CallbackData: encodeCmdCallback("a", "status")},
            {Text: "Uptime",         CallbackData: encodeCmdCallback("a", "uptime")},
        },
        {
            {Text: "Rate",           CallbackData: encodeCmdCallback("a", "rate")},
            {Text: "Missing blocks", CallbackData: encodeCmdCallback("a", "missing")},
        },
        {
            {Text: "Operation time", CallbackData: encodeCmdCallback("a", "operation_time")},
        },
        {cancelBtn},
    }}
```

Add corresponding cases in `executeCmdMenuAction`:

```go
case "missing":
    msg, _, _, err := formatMissingBlocks(db, state.ChainID, state.Period, 1, limitDefault, "", sortDefault)
    ...
case "operation_time":
    msg, _, _, err := formatOperationTime(db, state.ChainID, 1, limitDefault, "", sortDefault)
    ...
```

Note: `status` does NOT need a period (it calls `ChainHealthFetcher`) — separate it from the
period-aware commands (see Bug 3).

#### Bug 2 — No period selection for period-aware validator commands

**Location:** `handleCmdMenuCallback` validators case + `CmdState`

When the user picks `rate`, `uptime`, `missing`, or `operation_time`, the menu goes straight to
confirm with `periodDefault`. No period is offered.

**Fix:** Add an intermediate `"period"` step between action selection and confirm for
period-aware commands. After selecting the command, show a period picker:

```
Choose period:
[Current week]   [Current month]
[Current year]   [All time]
[❌ Cancel]
```

Callback data: `c=mn&a=pd&v=current_week` etc. (use a new action code `pd` for period-select,
`v` for the period value — fits within 64 bytes).

Update `CmdState`:
```go
Step: "period"   // new step between "action" and "confirm"
```

In `handleCmdMenuCallback`, add a `case "pd":` branch:
```go
case "pd":
    state.Period = rawParams["v"]
    state.Step = "confirm"
    setCmdState(chatID, state)
    // show confirm markup
```

Add `buildPeriodMarkup()`:
```go
func buildPeriodMarkup() *InlineKeyboardMarkup {
    return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
        {
            {Text: "Current week",  CallbackData: encodeCmdCallback("a", "pd", "v", "current_week")},
            {Text: "Current month", CallbackData: encodeCmdCallback("a", "pd", "v", "current_month")},
        },
        {
            {Text: "Current year",  CallbackData: encodeCmdCallback("a", "pd", "v", "current_year")},
            {Text: "All time",      CallbackData: encodeCmdCallback("a", "pd", "v", "all_time")},
        },
        {{Text: "❌ Cancel", CallbackData: encodeCmdCallback("a", "cx")}},
    }}
}
```

Period-aware commands: `rate`, `uptime`, `missing`, `operation_time`.
Non-period commands (skip period step): `status` (chain health), subscribe actions, report actions.

#### Bug 3 — `status` in validators menu calls `formatParticipationRAte` instead of `ChainHealthFetcher`

**Location:** `executeCmdMenuAction`, validators `case "status"`

Current broken code:
```go
case "status":
    msg, _, _, err := formatParticipationRAte(db, state.ChainID, periodDefault, 1, limitDefault, "", sortDefault)
```

This is the same as `rate`. The `/status` command calls `ChainHealthFetcher` and
`formatChainHealthMessage` — the menu must do the same.

**Fix:**
```go
case "status":
    if ChainHealthFetcher == nil {
        return "⚠️ Chain health data is not available yet."
    }
    snap := ChainHealthFetcher(state.ChainID)
    return formatChainHealthMessage(state.ChainID, snap)
```

Note: `status` also does not need a confirm step — it is read-only and instant.
Consider routing it directly to execution (skip confirm) in `handleCmdMenuCallback`.

---

## Encodage `encodeCmdCallback` multi-params

The current `encodeCmdCallback(extras ...string)` signature takes flat `key, value, key, value`
varargs. Verify that adding `"v", "current_week"` as extras stays within 64 bytes:

`c=mn&a=pd&v=current_week` = 25 bytes ✅ — safe.

---

## Summary of files to touch for bug fixes

| File | Change |
| --- | --- |
| `internal/telegram/validator.go` | `buildActionMarkup` validators case; `executeCmdMenuAction` validators case; `handleCmdMenuCallback` period step + status routing; new `buildPeriodMarkup`; `CmdState` no new fields needed (Period already present) |
