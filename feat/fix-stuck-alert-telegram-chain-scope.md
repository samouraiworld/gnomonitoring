# fix: scope stagnation/info alerts to subscribed Telegram chats only

## Problem

Chain-level alerts (blockchain stuck, activity restored, new validator detected) are
broadcast to **every** Telegram chat registered in the `telegram_chat_ids` table,
regardless of which chain the chat is monitoring.

A chat configured for `test11` or `gnoland1` therefore receives stuck alerts for
`test12` and vice versa.

### Root cause

`SendInfoValidator` (and the stagnation path in `CollectParticipation`) calls
`telegram.MsgTelegram`:

```go
// internal/fonction.go – SendInfoValidator
telegram.MsgTelegram(msg, Config.TokenTelegramValidator, "validator", db)
```

`MsgTelegram` fetches **all** chat IDs of type `"validator"` and sends without any
chain filter:

```go
// telegram/telegram.go – MsgTelegram
ids, _ := database.GetAllChatIDs(db, typeChatid)
for _, chatID := range ids {
    SendMessageTelegram(token, chatID, msg)   // no chain check
}
```

By contrast, validator-specific alerts (`SendAllValidatorAlerts`,
`SendResolveValidator`) use `MsgTelegramAlert`, which checks that the chat has at
least one active subscription matching `(chainID, addr)` before sending.

Discord and Slack webhooks are already scoped correctly via:
```go
db.Model(&database.WebhookValidator{}).
    Where("chain_id = ? OR chain_id IS NULL", chainID).
    Find(&webhooks)
```

### Affected alert types

| Function | Trigger | Telegram call | Bug |
| --- | --- | --- | --- |
| `SendInfoValidator` | Chain stuck, activity restored, new validator | `MsgTelegram` (broadcast) | ✅ yes |
| `SendAllValidatorAlerts` | WARNING / CRITICAL missed blocks | `MsgTelegramAlert` (chain+addr scoped) | ✗ no |
| `SendResolveValidator` | RESOLVED | `MsgTelegramAlert` (chain+addr scoped) | ✗ no |

---

## Goal

Chain-level alerts sent via `SendInfoValidator` must be delivered only to Telegram
chats that have **at least one active subscription for the alert's `chainID`**.

A chat is considered "subscribed to chainID" when it has at least one row in
`telegram_validator_subs` with `chain_id = chainID AND active = 1`.

---

## Implementation plan

### Step 1 — Add `GetChatIDsForChain` DB helper

**File:** `backend/internal/database/db_metrics.go` (or `db_admin.go`)

```go
// GetChatIDsForChain returns all chat IDs of the given type that have at least one
// active validator subscription for chainID.
func GetChatIDsForChain(db *gorm.DB, typeChatid, chainID string) ([]int64, error) {
    var ids []int64
    err := db.Raw(`
        SELECT DISTINCT tci.chat_id
        FROM telegram_chat_ids tci
        JOIN telegram_validator_subs tvs
          ON tvs.chat_id = tci.chat_id
         AND tvs.chain_id = ?
         AND tvs.active   = 1
        WHERE tci.type = ?
    `, chainID, typeChatid).Scan(&ids).Error
    return ids, err
}
```

### Step 2 — Add `MsgTelegramChain` in telegram package

**File:** `backend/internal/telegram/telegram.go`

```go
// MsgTelegramChain sends msg to every chat of typeChatid that has at least one
// active subscription for chainID. Use this for chain-level alerts (stuck,
// activity restored, new validator) where no specific validator address is involved.
func MsgTelegramChain(msg, chainID, token, typeChatid string, db *gorm.DB) error {
    if token == "" {
        return fmt.Errorf("token is empty")
    }

    ids, err := database.GetChatIDsForChain(db, typeChatid, chainID)
    if err != nil {
        log.Printf("❌ GetChatIDsForChain failed (chain=%s): %v", chainID, err)
        return err
    }

    for _, chatID := range ids {
        if err := SendMessageTelegram(token, chatID, msg); err != nil {
            log.Printf("❌ MsgTelegramChain send failed for chat_id=%d: %v", chatID, err)
        } else {
            log.Printf("✅ MsgTelegramChain sent to chat_id=%d (chain=%s)", chatID, chainID)
        }
    }
    return nil
}
```

### Step 3 — Replace `MsgTelegram` with `MsgTelegramChain` in `SendInfoValidator`

**File:** `backend/internal/fonction.go`

```go
// Before
if err := telegram.MsgTelegram(msg, Config.TokenTelegramValidator, "validator", db); err != nil {
    log.Printf("❌ MsgTelegram: %v", err)
}

// After
if err := telegram.MsgTelegramChain(msg, chainID, Config.TokenTelegramValidator, "validator", db); err != nil {
    log.Printf("❌ MsgTelegramChain: %v", err)
}
```

`chainID` is already a parameter of `SendInfoValidator(chainID, msg, level string, db *gorm.DB)`.

---

## Behaviour after fix

| Situation | Before | After |
| --- | --- | --- |
| Chain test12 stuck | All validator chats notified | Only chats subscribed to test12 |
| Chain test11 stuck | All validator chats notified | Only chats subscribed to test11 |
| New validator on gnoland1 | All validator chats notified | Only chats subscribed to gnoland1 |
| Activity restored on test12 | All validator chats notified | Only chats subscribed to test12 |
| Chat with no subscription | Receives all chain alerts | Receives nothing (correct) |

---

## Edge cases

**Chat subscribed to multiple chains** — `GetChatIDsForChain` uses `DISTINCT`, so
the chat receives exactly one message per stuck-chain event, even if it has multiple
subs for that chain.

**Chat with no subscription at all** — receives nothing. If a user wants to receive
all stuck alerts regardless of chain, they should subscribe to at least one validator
on each chain they want to monitor (existing behavior).

**`chain_id IS NULL` webhooks (Discord/Slack)** — the webhook query already handles
these (`WHERE chain_id = ? OR chain_id IS NULL`). No change needed for
Discord/Slack.

---

## Files to change

| File | Change |
| --- | --- |
| `backend/internal/database/db_metrics.go` | Add `GetChatIDsForChain` |
| `backend/internal/telegram/telegram.go` | Add `MsgTelegramChain` |
| `backend/internal/fonction.go` | Replace `MsgTelegram` → `MsgTelegramChain` in `SendInfoValidator` |

No DB migration needed — reads only from existing `telegram_chat_ids` and
`telegram_validator_subs` tables.
