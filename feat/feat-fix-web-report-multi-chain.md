# feat: fix web daily reports for multi-chain users

## Problem

`SheduleUserReport` in `backend/internal/gnovalidator/gnovalidator_report.go:43`
hard-codes `internal.Config.DefaultChain` when dispatching daily reports for web users:

```go
SendDailyStatsForUser(db, internal.Config.DefaultChain, &userID, nil, loc)
```

`SendUserReportAlert` (in `internal/fonction.go:472`) correctly scopes its webhook query to
`WHERE user_id = ? AND chain_id = ?`, so if the user's webhooks are for `test12` / `gnoland1`
but DefaultChain is `betanet`, the query returns no rows and nothing is dispatched.

Telegram reports are correct: `SheduleTelegramReport` already receives `chainID` as a parameter.
Only web-registered (Clerk) users are affected.

---

## Fix overview

1. Add a small DB helper `GetUserWebhookChains(db, userID)` that returns the distinct
   `chain_id` values from `webhook_validators` for that user.
2. Update `SheduleUserReport` to call the helper and loop over the returned chain IDs,
   calling `SendDailyStatsForUser` once per chain.
3. Fallback: if the user has no webhooks (or the DB call fails), fall back to
   `internal.Config.DefaultChain` — same behaviour as today, no regression.

---

## Implementation plan

### Step 1 — Add `GetUserWebhookChains` to `database/db.go`

**File:** `backend/internal/database/db.go`

Add this function near the other webhook helpers (after `GetWebhookByID`):

```go
// GetUserWebhookChains returns the distinct chain IDs that the user has configured
// in webhook_validators. Returns nil (not an error) if the user has no webhooks.
func GetUserWebhookChains(db *gorm.DB, userID string) ([]string, error) {
    var chains []string
    err := db.Model(&WebhookValidator{}).
        Distinct("chain_id").
        Where("user_id = ? AND chain_id IS NOT NULL AND chain_id != ''", userID).
        Pluck("chain_id", &chains).Error
    return chains, err
}
```

No migration needed — reads from the existing `webhook_validators` table.

### Step 2 — Update `SheduleUserReport` in `gnovalidator_report.go`

**File:** `backend/internal/gnovalidator/gnovalidator_report.go`

Replace the current dispatch block inside the `timer.C` case:

```go
// current (to replace):
SendDailyStatsForUser(db, internal.Config.DefaultChain, &userID, nil, loc)
```

With:

```go
chains, err := database.GetUserWebhookChains(db, userID)
if err != nil || len(chains) == 0 {
    // fallback: send for the default chain
    chains = []string{internal.Config.DefaultChain}
}
for _, chainID := range chains {
    SendDailyStatsForUser(db, chainID, &userID, nil, loc)
}
```

Add `"github.com/samouraiworld/gnomonitoring/backend/internal/database"` to the imports of
`gnovalidator_report.go` if it is not already there.

---

## What stays untouched

- `SheduleTelegramReport` — already receives `chainID` directly. No change.
- `SendDailyStatsForUser` — signature unchanged.
- `SendUserReportAlert` — already scopes by `chain_id`. No change.
- Scheduler goroutine management — reload channel, timer logic, all unchanged.

---

## Edge cases

| Scenario | Behaviour after fix |
|---|---|
| User has webhooks for 2 chains | Receives one report per chain |
| User has webhooks for 0 chains | Falls back to DefaultChain (same as today) |
| DB call fails | Falls back to DefaultChain, logs error |
| User has webhooks for a disabled chain | `FetchChainHealthSnapshot` returns `IsDisabled=true` → `FormatDisabledReport` sent |
| Same chain configured twice (two webhooks) | `Distinct` in the query deduplicates — one report per chain |

---

## Files changed

| File | Change |
|---|---|
| `backend/internal/database/db.go` | Add `GetUserWebhookChains` |
| `backend/internal/gnovalidator/gnovalidator_report.go` | Loop over user chains in `SheduleUserReport`; add `database` import |

No DB migration. No new table. No change to alert dispatch or Telegram scheduler.
