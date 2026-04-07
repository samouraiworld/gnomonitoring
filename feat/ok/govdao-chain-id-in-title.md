# GovDAO Chain ID in Notification Titles

**Status:** DONE
**Date:** 2026-03-26
**Implemented:** 2026-03-27
**Impact:** MINOR — Notification formatting only; no DB schema changes, no API changes
**Category:** User-facing improvement

---

## 1. PROBLEM STATEMENT

GovDAO proposal notifications are sent across Telegram, Discord, Slack, and the REST API. Users subscribed to multiple chains cannot distinguish which chain a proposal comes from because the chain ID is missing from the message title.

**Example:** A user monitoring both `test12` and `gnoland1` receives:
```
🗳️ New Proposal Nº 42: Some Title
```

With no indication of which chain the proposal belongs to.

---

## 2. GOAL

Add the chain ID to the title of every GovDAO notification message across all channels:

- **Telegram:** `🗳️ [test12] New Proposal Nº 42: Some Title`
- **Discord:** `🗳️ ** [test12] New Proposal N° 42: Some Title **`
- **Slack:** `🗳️ * [test12] New Proposal N° 42: Some Title *`

This applies to:
1. New proposal notifications (via `FormatTelegramMsg` and `SendReportGovdao`)
2. Proposal status-change messages (ACCEPTED/REJECTED, via `CheckProposalStatus`)

---

## 3. SCOPE

### 3.1 IN SCOPE

- Add `chainID string` parameter to all GovDAO message formatting functions
- Prepend `[chainID]` to proposal titles in all notification formats
- Update all call sites to pass the chain ID down the call chain
- Update tests that validate message format strings

### 3.2 OUT OF SCOPE

- Fixing the webhook channel filtering bug (where a webhook scoped to chain A receives proposals from chain B) — that is a separate concern
- DB schema changes — the `Govdao` model already has `ChainID`
- REST API changes — webhook endpoints and responses are not affected
- Proposal status display in the UI

---

## 4. CURRENT MESSAGE FLOW

The chain ID is available at the top level but is not passed down:

```
ProcessProposal(chainID)          ← has chainID ✅
  └── MultiSendReportGovdao()     ← NO chainID ❌
        ├── SendReportGovdao()    ← NO chainID ❌ (Discord/Slack)
        └── telegram.MsgTelegram()
              └── FormatTelegramMsg() ← NO chainID ❌

CheckProposalStatus()             ← fetches chainID from DB ✅
  ├── SendInfoGovdao(msg)         ← msg pre-built without chainID ❌
  └── telegram.MsgTelegram()      ← NO chainID ❌
```

---

## 5. FILES TO MODIFY

| File | Function | Change |
|------|----------|--------|
| `internal/telegram/govdao.go` | `FormatTelegramMsg()` | Add `chainID string` parameter; prepend `[chainID]` to title line |
| `internal/telegram/govdao.go` | `SendReportGovdaoTelegram()` | Add `chainID string` parameter; pass it to `FormatTelegramMsg()` |
| `internal/fonction.go` | `SendReportGovdao()` | Add `chainID string` parameter; prepend `[chainID]` in Discord and Slack title strings |
| `internal/fonction.go` | `MultiSendReportGovdao()` | Add `chainID string` parameter; pass it to `SendReportGovdao()` and `FormatTelegramMsg()` |
| `internal/fonction.go` | `SendInfoGovdao()` | Add `chainID string` parameter; prepend `[chainID]` to msg before sending |
| `internal/govdao/govdao.go` | `ProcessProposal()` | Pass `chainID` to `MultiSendReportGovdao()` (already has it) |
| `internal/govdao/govdao.go` | `CheckProposalStatus()` | Read `chainID` from `proposal.ChainID`; pass to `SendInfoGovdao()` and format Telegram msg with `[chainID]` prefix |

---

## 6. IMPLEMENTATION ORDER

### Step 1: Innermost helpers (Telegram formatting)

1. Update `FormatTelegramMsg()` signature:
   ```go
   func FormatTelegramMsg(chainID string, proposal *Govdao, ...) string
   ```
   Change the title line from:
   ```
   🗳️ New Proposal Nº %d: %s
   ```
   to:
   ```
   🗳️ [%s] New Proposal Nº %d: %s
   ```
   (where `%s` is `chainID`, `%d` is proposal ID, second `%s` is proposal title)

2. Update `SendReportGovdaoTelegram()` signature to accept and pass `chainID`:
   ```go
   func SendReportGovdaoTelegram(chatID int64, chainID string, proposal *Govdao, ...) error
   ```

### Step 2: Middle layer (Discord/Slack and aggregation)

3. Update `SendReportGovdao()` signature and logic:
   ```go
   func SendReportGovdao(chainID string, proposal *Govdao, ...) error
   ```
   Change Discord title from:
   ```
   🗳️ ** New Proposal N° %d: %s **
   ```
   to:
   ```
   🗳️ ** [%s] New Proposal N° %d: %s **
   ```
   Change Slack title from:
   ```
   🗳️ * New Proposal N° %d: %s *
   ```
   to:
   ```
   🗳️ * [%s] New Proposal N° %d: %s *
   ```

4. Update `MultiSendReportGovdao()` signature:
   ```go
   func MultiSendReportGovdao(chainID string, proposal *Govdao, ...) error
   ```
   Pass `chainID` to both `SendReportGovdao()` and `SendReportGovdaoTelegram()` calls.

5. Update `SendInfoGovdao()` signature:
   ```go
   func SendInfoGovdao(chainID string, msg string, ...) error
   ```
   Prepend `[chainID]` to `msg` before sending (e.g., if msg is `"ACCEPTED"`, send `"[chainID] ACCEPTED"`).

### Step 3: Call sites (data collection loop)

6. Update `ProcessProposal()` to pass `chainID` to `MultiSendReportGovdao()`:
   ```go
   err := MultiSendReportGovdao(chainID, proposal, ...)
   ```
   (already has `chainID` from function signature)

7. Update `CheckProposalStatus()`:
   - Read `chainID` from `proposal.ChainID` when iterating proposals from the DB
   - Pass `chainID` to `SendInfoGovdao()`:
     ```go
     SendInfoGovdao(proposal.ChainID, "ACCEPTED", ...)
     ```
   - When building Telegram message for status changes, prepend `[chainID]`:
     ```go
     msg := fmt.Sprintf("🗳️ [%s] Proposal Nº %d: %s — %s", proposal.ChainID, proposal.ID, proposal.Title, "ACCEPTED")
     ```

### Step 4: Testing

8. Update test cases in:
   - `internal/telegram/govdao_test.go` — update `FormatTelegramMsg()` test expectations to include `[chainID]`
   - `internal/fonction_test.go` — update `SendReportGovdao()` test expectations to include `[chainID]` in Discord/Slack formats
   - `internal/govdao/govdao_test.go` — update mock message validation to include `[chainID]`

---

## 7. IMPLEMENTATION DETAILS

### Message Format Changes

**Telegram (FormatTelegramMsg):**

```go
// Before:
title := fmt.Sprintf("🗳️ New Proposal Nº %d: %s", proposal.ID, proposal.Title)

// After:
title := fmt.Sprintf("🗳️ [%s] New Proposal Nº %d: %s", chainID, proposal.ID, proposal.Title)
```

**Discord (SendReportGovdao):**

```go
// Before:
"🗳️ ** New Proposal N° %d: %s ** -"

// After:
"🗳️ ** [%s] New Proposal N° %d: %s ** -"  // with chainID as first %s
```

**Slack (SendReportGovdao):**

```go
// Before:
"🗳️ * New Proposal N° %d: %s * -"

// After:
"🗳️ * [%s] New Proposal N° %d: %s * -"  // with chainID as first %s
```

**Status-change messages (CheckProposalStatus):**

For ACCEPTED/REJECTED notifications:

```go
// Before:
msg := "Proposal Nº 42: Some Title — ACCEPTED"

// After:
msg := "[test12] Proposal Nº 42: Some Title — ACCEPTED"
```

### Parameter Threading

All additions are **parameter-only changes**. No breaking changes to function bodies, logic, or return types.

Example signature updates:

```go
// Before
func SendReportGovdao(proposal *Govdao, userID string) error

// After
func SendReportGovdao(chainID string, proposal *Govdao, userID string) error
```

---

## 8. BACKWARD COMPATIBILITY

- **Database:** No schema changes. The `Govdao` model already has `ChainID` column.
- **API:** No endpoint changes. REST webhooks continue to work as-is.
- **Webhooks:** No changes to webhook payload structure; only the message body sent to Discord/Slack/Telegram is modified.

---

## 9. TESTING STRATEGY

### Unit Tests

Update existing tests to validate the new format:

```go
// Example in telegram/govdao_test.go
func TestFormatTelegramMsg(t *testing.T) {
    proposal := &Govdao{ID: 42, Title: "Some Title"}
    msg := FormatTelegramMsg("test12", proposal, ...)

    // Before: assert msg contains "🗳️ New Proposal Nº 42"
    // After: assert msg contains "🗳️ [test12] New Proposal Nº 42"
    assert.Contains(t, msg, "[test12] New Proposal Nº 42")
}
```

### Integration Testing

Verify end-to-end message delivery:
1. Create a test proposal in one chain (e.g., test12)
2. Verify notification title includes `[test12]`
3. Verify resolution message includes `[test12]`

---

## 10. KNOWN LIMITATIONS & OUT OF SCOPE

1. **Webhook multi-chain filtering:** This change does NOT fix the scenario where a webhook registered for chain A receives proposals from chain B. That is a separate architectural issue requiring webhook-to-chain scoping in the DB.

2. **Persistent user preferences:** Users must still switch chains manually per chat (via `/setchain` command in Telegram). This change only makes the chain visible in the notification title.

3. **Historical messages:** Existing notifications already sent to users are not updated. Only new proposals going forward will include the chain ID.

---

## 12. FOLLOW-UP: Chain ID in Telegram Bot Command Responses

**Status:** PENDING
**Reported:** 2026-03-27

### Problem

The `/status` command in the GovDAO Telegram bot (`internal/telegram/govdao.go`) does not display the active chain ID in its response. A user monitoring multiple chains cannot tell which chain the proposals belong to.

Currently `/executedproposals` and `/lastproposal` already show the chain ID via `FormatTelegramMsg()`, but `/status` uses a separate `formatStatusProposal()` function that has no chain ID awareness.

### Goal

Add `[chainID]` to the header of the `/status` response so users always know which chain they're looking at.

**Before:**
```
🗳️ Proposals status:

Nº 42 — Some Title
...
```

**After:**
```
🗳️ [test12] Proposals status:

Nº 42 — Some Title
...
```

### File to Modify

| File | Function | Change |
|------|----------|--------|
| `internal/telegram/govdao.go` | `formatStatusProposal()` | Add `chainID string` parameter; prepend `[chainID]` to the response header |
| `internal/telegram/govdao.go` | `/status` handler | Pass active `chainID` (already in scope via `getActiveChain`) to `formatStatusProposal()` |

### Implementation

```go
// formatStatusProposal signature change:
func formatStatusProposal(chainID string, proposals []database.Govdao) string

// Header line change (first line of the response):
// Before:
sb.WriteString("🗳️ Proposals status:\n\n")
// After:
fmt.Fprintf(&sb, "🗳️ [%s] Proposals status:\n\n", chainID)
```

The `chainID` is already available in the `/status` handler via `getActiveChain(chatID, defaultChainID)`.

### Testing

Update the corresponding test in `internal/telegram/govdao_test.go` (if a test for `formatStatusProposal` exists) to assert `[chainID]` appears in the output header.

---

## 11. VERIFICATION CHECKLIST

After implementation:

- [x] All functions updated with `chainID` parameter
- [x] All call sites pass `chainID` correctly
- [x] Telegram title includes `[chainID]`
- [x] Discord title includes `[chainID]`
- [x] Slack title includes `[chainID]`
- [x] Status-change messages include `[chainID]`
- [x] Unit tests updated and passing
- [x] No regressions in existing tests
- [ ] Manual test: send proposal on test12, verify notification shows `[test12]`
- [ ] Manual test: send proposal on gnoland1, verify notification shows `[gnoland1]`
