# Changelog

All notable changes to Gnomonitoring are documented here.
Entries are ordered newest-first within each section.

---

## [Unreleased]

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
