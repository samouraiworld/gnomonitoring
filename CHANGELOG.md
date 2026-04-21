# Changelog

All notable changes to Gnomonitoring are documented here.
Entries are ordered newest-first within each section.

---

## [Unreleased]

### Added

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
