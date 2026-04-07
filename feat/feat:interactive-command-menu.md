# feat: interactive command menu (`/cmd`)

## Objective

Introduce a `/cmd` command that opens a guided inline-keyboard menu, allowing users to execute
existing commands without typing parameters manually.

This improves UX for new users while keeping all existing text commands intact.

---

## Existing infrastructure (already present — do NOT reinvent)

| Component | Location | Role |
|---|---|---|
| `BuildTelegramCallbackHandler()` | `validator.go:725` | Returns `func(chatID int64, msgID int, data string)` — already wired in `main.go:161` |
| `StartCommandLoop()` callback dispatch | `telegram.go:386` | Already calls `callbackHandler(chatID, messageID, callbackData)` for `CallbackQuery` updates |
| `encodeCallbackData()` / `parseCallbackData()` | `validator.go:866,892` | URL-style compact encoding (`c=st&p=1&l=10`) to stay under 64-byte Telegram limit |
| `SendMessageTelegramWithMarkup()` | `telegram.go:110` | Sends a message with `InlineKeyboardMarkup` |
| `EditMessageTelegramWithMarkup()` | `telegram.go:152` | Edits existing message in-place (avoids chat spam) |
| `buildPaginationMarkup()` | `validator.go:833` | Already builds Prev/Next pagination buttons |
| `buildSecondaryButtons()` | `validator.go:928` | Already builds Sort and Search secondary buttons |
| `searchState map[int64]SearchState` | `validator.go:1068` | In-memory TTL state (2 min) with 5-min cleanup goroutine — reuse for `/cmd` session state |
| `chatChainState map[int64]string` | `validator.go:90` | Per-chat active chain — already used by all command handlers |

**No new state storage mechanism needed.** Extend `SearchState` or add a parallel `CmdState` struct
following the same TTL pattern.

---

## Scope

This feature targets the **validator bot only** (`validator.go`).
The GovDAO bot (`govdao.go`) has `nil` as its callback handler in `main.go:178` and has no inline
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

Callback data: `c=cmd&a=subscribe`, `c=cmd&a=report`, etc.

### Step 2 — Action selection (command-specific)

**Subscribe:** `[ON]  [OFF]`
**Report:** `[Enable]  [Disable]  [Change schedule]`
**Validators:** `[Status]  [Uptime]  [Rate]`
**Chain:** show available chains (from `enabledChains`)

### Step 3 — Validator / parameter selection (where applicable)

Reuse existing pagination: show validators from `MonikerMap(chainID)` with `buildPaginationMarkup`.
Max 5 per page.
Include `[All validators]` shortcut.

### Step 4 — Confirmation

Edit same message (via `EditMessageTelegramWithMarkup`) to show summary:

```
Subscribe ON
Validators: g1abc..., g1def...
Chain: betanet

[✅ Confirm]  [❌ Cancel]
```

Callback data: `c=cmd&a=confirm` / `c=cmd&a=cancel`

### Step 5 — Execution

On confirm: translate session state into the equivalent command string and call the existing handler
directly (e.g., call `handleSubscribe(db, token, chatID, chainID, "on g1abc g1def")`).
This reuses all existing logic without duplication.

---

## Session state

Extend (or parallel to) `SearchState`. Add a `CmdState` struct:

```go
type CmdState struct {
    Step       string    // "choose_action", "choose_chain", "choose_validators", "confirm"
    Command    string    // "subscribe", "report", "validators", "chain"
    Action     string    // "on", "off", "status", etc.
    ChainID    string
    Validators []string
    Period     string
    ExpiresAt  time.Time
}

var cmdState   = map[int64]CmdState{}
var cmdStateMu sync.RWMutex
```

TTL: 5 minutes (longer than `searchTTL` since multi-step flow takes more time).
Cleanup: piggyback on the existing 5-min cleanup goroutine in `validator.go:1076`.

---

## Callback data encoding

Reuse `encodeCallbackData()` / `parseCallbackData()`. Add new command codes to the existing
`cmdToCode` / `codeToCmd` maps:

| Logical value | Code |
|---|---|
| `cmd` (menu entry) | `mn` |
| `confirm` | `ok` |
| `cancel` | `cx` |
| `subscribe` | `sb` (reuse or add) |
| `report` | `rp` |
| `chain` | `ch` (reuse or add) |

Keep total encoded size under ~45 bytes to leave room for page/filter fields.

---

## Wiring into existing callback handler

The existing `BuildTelegramCallbackHandler()` routes on the `c` (command) field parsed from callback
data. Add a new branch for `c=mn` (menu):

```go
case "mn":
    msg, markup := handleCmdMenu(db, chatID, msgID, params)
    _ = EditMessageTelegramWithMarkup(token, chatID, msgID, msg, markup)
```

No changes needed to `StartCommandLoop`, `main.go`, or the polling loop.

---

## Help text

Add to `formatHelp()`:

```
🎛 /cmd — Interactive command menu (guided step-by-step)
```

---

## Files changed

| File | Change |
|---|---|
| `internal/telegram/validator.go` | Add `/cmd` to handler map; add `CmdState` struct + helpers; add `handleCmdMenu` routing function; extend `BuildTelegramCallbackHandler` with `mn` case; extend `cmdToCode`/`codeToCmd`; update `formatHelp` |
| `main.go` | No change needed |

No new dependency. No DB migration. No Redis.

---

## Constraints & edge cases

- **64-byte callback limit**: managed by existing `encodeCallbackData`. Keep action names short (use codes).
- **Expired state**: if `CmdState` not found for a chatID, send "Session expired — run /cmd again."
- **Invalid callback data**: `parseCallbackData` returns empty map on malformed input; handle gracefully.
- **Concurrent taps**: protect `cmdState` with `cmdStateMu` (same pattern as `searchState`).
- **GovDAO bot**: out of scope — no callback handler wired there yet.

---

## Backward compatibility

All existing text commands (`/subscribe`, `/chain`, `/validators`, `/report`, etc.) remain unchanged.
`/cmd` is an additional entry point, not a replacement.
