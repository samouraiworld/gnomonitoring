# Changelog

All notable changes to Gnomonitoring are documented here.
Entries are ordered newest-first within each section.

---

## [Unreleased]

### Fixed

- **GovDAO proposal statuses were parsed from the wrong strings, so a rejected
  proposal was never detected** (#112) — the parser keyed off `"ACTIVE"`, a
  string the proposal page never contains, then fell back to `"Vote YES"`,
  which comes from the action bar the realm renders on *every* proposal
  including accepted and denied ones. A rejected proposal was therefore stored
  as `IN PROGRESS` forever, and its `default: REJECTED` branch only fired when
  the render was unreadable — fabricating a rejection out of an RPC hiccup.
  The parser now matches the explicit marker lines emitted by
  `proposalStatus.String()` (`PROPOSAL HAS BEEN ACCEPTED` / `HAS BEEN DENIED` /
  `Proposal is open for votes`) and returns `UNKNOWN`, which is inert, for
  anything it cannot read. This also stops a proposal whose own description
  contains the word "ACCEPTED" from being read as accepted.

- **No alert was ever sent when a GovDAO proposal was rejected** (#112) — only
  the `ACCEPTED` transition notified. Rejections now send the same
  Discord/Slack + Telegram notification (❌) and update `govdaos.status`, both
  branches sharing one code path. Note that gov/dao v3 has no expiry concept:
  `ACCEPTED` and `REJECTED` are the only terminal states the realm exposes.

- **A GovDAO status update overwrote its namesakes on every other chain** —
  `govdaos` is keyed on `(id, chain_id)`, but the update filtered on `id`
  alone, so proposal #0 turning `ACCEPTED` on betanet rewrote the status of
  proposal #0 on every configured chain. The update is now scoped by both.

- **RPC failover only tried the second endpoint, never a third** — the pool
  now walks every configured endpoint in priority order on a failure, so a
  third healthy endpoint is used when the first two are down.

- **A full RPC outage was written to the log only, never dispatched** — it
  now sends a CRITICAL alert to Discord/Slack/Telegram, and recovery sends a
  RESOLVED alert once connectivity returns.

- **`/validators` and `/genesis` fetches could hang monitoring startup
  indefinitely** — they previously retried `rpc_endpoints[0]` three times
  with `http.DefaultClient`, which has no timeout: a hung primary blocked
  startup forever. They now fail over across all configured endpoints and
  are bounded by a 15s timeout.

- **An empty `/validators` response wiped the valset** — this used to fire
  one "Validator left the valset" alert per validator. An empty response is
  now ignored and the previous valset is kept instead of being replaced.

- **A failover to a lagging endpoint could suppress the "Blockchain stuck"
  alert indefinitely** — two endpoints a few blocks apart made the observed
  height oscillate, which reset the stagnation timer on every oscillation.
  A brief height regression is now tolerated instead of resetting it.

- **GovDAO proposal title and status lookups had no failover** — they now go
  through the pooled RPC client like the rest of chain monitoring.

- **`first_active_block` stuck at `-1`/`NULL` for validators that join but
  never sign** — it is now anchored on the validator's first observed
  `/validators` snapshot (valset join) instead of its first true signature,
  so missed blocks are tracked from the real join point. A companion fix in
  `WatchValidatorAlerts`' dead-validator silence window means a validator
  with zero participation since joining can now generate its first
  WARNING/CRITICAL instead of being silently skipped forever. A follow-up
  pass heals rows left stuck by the old behavior (both pre-existing
  `addr_monikers` rows and cases where `PopulateFirstActiveBlocks`' fallback
  lost its only evidence to `PruneRawData`) on the next startup/poll.

- **Validator webhook `PUT` silently dropped `chain_id`** —
  `UpdateMonitoringWebhookHandler` hardcoded `nil` when calling
  `UpdateMonitoringWebhook`, so a client could never set or change a
  validator webhook's chain_id no matter what it sent. Now mirrors the
  GovDAO PUT handler: requires and forwards the decoded chain_id.

### Added

- **`govdaos.status_synced`** (new column, defaults false) — records whether a
  proposal's stored status was actually read from the chain. Rows written
  before the GovDAO parser fix land on false, because the old parser reported
  every rejected proposal as `IN PROGRESS`. The watcher corrects such a row
  against the chain once, *silently*, before it starts treating status changes
  as live events: without this, the first run after deploy would announce a
  rejection for every historical proposal in the backlog at once. Added by
  AutoMigrate; no manual migration needed.

- **`rpc_health_check_seconds`** (admin_config, default 60) — how often the
  RPC pool probes its configured endpoints in the background to return to
  the highest-priority one once it is healthy again.

- **Live RPC data in daily report and `/status`** — `ChainHealthSnapshot` now
  fetches validator set with voting power (`Validators()`), valset changes from
  `r/sys/validators/v2`, peer count (`NetInfo()`), and mempool size
  (`NumUnconfirmedTxs()`) in parallel goroutines.
  See [`feat/feat-daily-report-rpc-enrichment.md`](feat/feat-daily-report-rpc-enrichment.md).

- **Per-validator uptime over last 24h in report** — validator section replaced
  the misleading precommit bitmap with participation rate from DB over the last
  24h window, showing top 5 worst performers with uptime % and voting power %.
  Monikers resolved from `addr_monikers` (authoritative) with fallback to
  `daily_participations.moniker`.

- **Valset changes filtered to last 24h** — additions and removals from
  `r/sys/validators/v2` are filtered to the current 24h block window; genesis
  entries (Block #0) no longer pollute the section.

- **New Prometheus metrics** — `gnoland_validator_voting_power`,
  `gnoland_chain_peer_count`, `gnoland_chain_mempool_tx_count`,
  `gnoland_chain_valset_size` added to the 5-minute metrics cycle.

- **REST endpoint `GET /api/chain/:chainID/health`** — returns the full
  `ChainHealthSnapshot` as JSON. Public, read-only, 20s timeout.

### Fixed

- **`PUT /alert-contacts` skipped the ownership/validation checks `POST` already
  had** — updating a contact could attach it to a webhook owned by another
  user, accepted an empty `moniker`/`namecontact` and silently blanked the
  stored values via `Updates(map[string]interface{}{...})`, and returned 200
  even when the `id`/`user_id` matched no row (0 rows affected, nil error).
  `UpdateAlertContactHandler` now runs the same webhook-ownership and
  required-field checks as `InsertAlertContactHandler`, rejects a payload that
  would unlink a contact from its webhook (an omitted `id_webhook` decodes to
  `0` and would otherwise be persisted, breaking the `id_webhook` join in
  `SendAllValidatorAlerts`), and returns 404 for an unknown contact.
  `UpdateAlertContact` returns `ErrAlertContactNotFound` when no row is
  affected as a backstop.

- **Deleting a validator webhook orphaned its `alert_contacts` rows** —
  `AlertContact.IDwebhook` has no DB-level foreign key, so removing a webhook
  left contacts pointing at an `id_webhook` that matched nothing, permanently
  unable to fire a mention. `DeleteMonitoringWebhook` now deletes the
  webhook's `alert_contacts` in the same transaction, skipping the cascade for
  the new `NoWebhookLinked` (`0`) sentinel so it can never sweep up every
  contact the user left unlinked.

- **Webhook-ownership check swallowed DB errors** — the `Count` call in both
  the `POST` and `PUT` `/alert-contacts` guards ignored its error, so a
  transient database failure surfaced as 400 "Webhook not found" instead of
  500.

- **`ValidatorVotingPower` metric wiped all chains on each cycle** — replaced
  `Reset()` with `DeletePartialMatch(chainLabel)` so only the current chain's
  stale entries are cleared.

- **`parseValsetChanges` never matched** — parser was written for a markdown
  table but the realm emits a bullet-list (`- #blockNum: addr (power)`).
  Rewritten to match the actual format.

- **`DumpConsensusState` peer_state decode failures** — amino encodes
  `PeerStateExposed` as a base64 string inside a JSON string; added two-step
  unwrap (JSON string → base64 decode → JSON unmarshal). Also added custom
  `UnmarshalJSON` for `bitArrayJSON` to handle amino's string-encoded `int`
  and `uint64` fields (`"bits": "7"`, `"elems": ["121"]`).

- **Goroutine leak in `enrichValidatorInfoFromValopers`** — removed inner
  goroutine wrapping that leaked on context timeout; ABCIQuery now called
  directly in the outer goroutine.

---

### Fixed

- **Alert dedup permanently blocked during backfill** — `SendResolveAlerts` is
  now called inside the sync-gate branch so RESOLVED alerts are dispatched even
  while the chain is catching up. Previously, multi-chain SQLite contention kept
  `chainSynced == false` indefinitely in production, preventing any RESOLVED from
  being inserted and silently blocking all subsequent WARNING/CRITICAL alerts for
  the same validator.
  See [`feat/fix-sync-gate-resolved-alerts.md`](feat/fix-sync-gate-resolved-alerts.md).

### Added

- **Diagnostic logs for alert pipeline** — three new log lines make the alert
  decision path observable without a debugger: CTE window count per cycle, dedup
  skip reason (with heights and window duration), dead-validator silence skip, and
  pending-RESOLVED count per `SendResolveAlerts` call.

- **Web daily reports fixed for multi-chain users** — `SheduleUserReport` now
  queries the user's registered webhook chains instead of hard-coding
  `DefaultChain`; web users with webhooks on `test12` or `gnoland1` now receive
  reports for the correct chain.
  See [`feat/feat-fix-web-report-multi-chain.md`](feat/feat-fix-web-report-multi-chain.md).

### Changed

- **Removed per-validator liveness section from `/status` and stuck-chain report**
  — the point-in-time precommit snapshot was not actionable and has been replaced
  by the 24h participation rate section.
  See [`feat/feat-remove-liveness-section.md`](feat/feat-remove-liveness-section.md).

---

## [2026-04-07]

### Added

- **Interactive Telegram command menu** — inline keyboard for common commands.
  See [`feat/feat:interactive-command-menu.md`](feat/feat:interactive-command-menu.md).

### Changed

- **Improved daily report format v2** — cleaner layout, better emoji usage, uptime
  percentage added.
  See [`feat/feat-improve-report-format-v2.md`](feat/feat-improve-report-format-v2.md).

- **Improved daily summary** — 24h alert summary reworked for readability.
  See [`feat/feat-improve-daily-summary.md`](feat/feat-improve-daily-summary.md).

### Fixed

- **RESOLVED alert spam** — replaced `start_height`-based dedup with time-based
  dedup; `SendResolveAlerts` now uses `alert_logs` as source of truth instead of
  a recomputed sliding window.
  See [`feat/feat-fix-alert-dedup-and-sync-gate.md`](feat/feat-fix-alert-dedup-and-sync-gate.md).

- **Backfill sync gate** — `WatchValidatorAlerts` skips alert processing during
  large-gap backfill to prevent historical alert spam.

- **Dead-validator silence** — validators with no participation in the last 7 days
  (configurable via `admin_config`) no longer generate daily alert noise.

- **Daily report suppressed when chain is stuck or disabled** — avoids sending
  empty or misleading reports during chain outages.

- **RPC fallback URLs** — added fallback RPC/GraphQL/GnoWeb endpoints per chain
  to survive single-endpoint failures.

- **Stagnation alert scoped per chain** — `lastProgressTime` and
  `lastStagnationAlertTime` are now per-chain maps; one chain stalling no longer
  suppresses the anti-spam guard for other chains.

---

## [2026-03-23]

### Added

- **Prometheus metrics** — 10 metrics across three phases (validator uptime,
  chain health, alert counts) with per-chain labels and a 5-minute update cycle.

- **Admin panel frontend** — React/TypeScript/Vite UI for managing thresholds,
  webhooks, and chain configuration.

- **Admin panel backend** — REST endpoints for reading and writing `admin_config`
  rows; Clerk authentication in production, `X-Debug-UserID` header in dev mode.

### Changed

- **Multi-chain support** — all queries, alert goroutines, Telegram commands,
  and Prometheus metrics are scoped by `chain_id`. Per-chat active chain stored
  in `chat_chain_state` table and hydrated on startup.

- **Telegram scheduler key format** — composite key `tg:<chat_id>:<chain_id>`
  so each (chat, chain) pair has an independent schedule entry.

---

## [2026-03-11]

### Added

- **Telegram report scheduling** — `/report` command allows setting a daily
  report time from Telegram; stored in `hour_reports` table.
  See [`feat/feat-telegram-report-schedule.md`](feat/feat-telegram-report-schedule.md).

- **Telegram daily summary** — `/summary` command sends a 24h alert digest.
  See [`feat/feat-daily-summary-24h-alerts.md`](feat/feat-daily-summary-24h-alerts.md).

### Fixed

- **Pagination in Telegram validator list** — `/validators` now pages results
  to stay within Telegram's 4096-char message limit.

- **GovDAO bot** — sends latest proposal to new chat on first interaction.
