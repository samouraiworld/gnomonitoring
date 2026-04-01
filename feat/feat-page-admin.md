# Admin Panel Feature

## Overview

An admin panel to control the backend without touching config files or the database directly.
The interface must be simple, responsive, and entirely contained in a `panel/` directory at the repo root.

- **Frontend stack**: React (Vite + React). All admin panel code lives in `panel/`.
- **Backend routes**: All admin routes go in a new `backend/internal/api/api-admin.go` file, not in `api.go`.

---

## Authentication

**Problem**: The existing API uses Clerk, scoped per `user_id`. The admin panel needs unscoped access
(e.g., list all users, all webhooks, all alerts).

**Why not Clerk**: The admin panel will be on a different domain. Clerk satellite domains (cross-domain
auth) cost $10/month per domain. Not worth it for an internal tool.

**Chosen approach**: Static admin token in `config.yaml` under `admin_token`, sent via `X-Admin-Token` header.
No JWT, no session. The token must be generated with `openssl rand -hex 32`.
In `dev_mode`, the token check is bypassed (same pattern as the existing Clerk bypass).
Normal users (webhooks, subscriptions) continue to use Clerk unchanged.

**CORS**: The admin panel domain must be added to `allow_origin` in `config.yaml`.

---

## Implementation Plan

Progress legend: `[ ]` todo — `[x]` done — `[~]` in progress

---

### Phase 1 — Backend foundation

#### 1.1 — Admin auth middleware
- [ ] Add `AdminToken string` field to `config` struct in `backend/internal/fonction.go`
- [ ] Load `admin_token` from `config.yaml`
- [ ] Create `backend/internal/api/api-admin.go` with `adminAuthMiddleware` function
- [ ] Register `/admin` route group in `api.go` using this middleware
- [ ] Bypass token check in `dev_mode`

#### 1.2 — `admin_config` DB table
- [ ] Create `AdminConfig` model in `backend/internal/database/db_init.go`
- [ ] Add migration to create the table with default values
- [ ] Add `GetAdminConfig(key)` and `SetAdminConfig(key, value)` helpers in `db_metrics.go`

Default keys and values:

| Key | Default |
|-----|---------|
| `warning_threshold` | `5` |
| `critical_threshold` | `30` |
| `mute_after_n_alerts` | `1` |
| `mute_duration_minutes` | `60` |
| `resolve_mute_after_n` | `4` |
| `stagnation_first_alert_seconds` | `20` |
| `stagnation_repeat_minutes` | `30` |
| `rpc_error_cooldown_minutes` | `10` |
| `new_validator_scan_minutes` | `5` |
| `alert_check_interval_seconds` | `20` |
| `raw_retention_days` | `7` |
| `aggregator_period_minutes` | `60` |

#### 1.3 — Replace hardcoded thresholds
- [ ] Replace constants in `gnovalidator_realtime.go` with reads from `admin_config` table
- [ ] Cache values in memory; refresh cache on each write via `PUT /admin/config/thresholds`

#### 1.4 — Goroutine lifecycle management
- [ ] Wrap each chain's monitoring goroutine with a `context.Context` + per-chain `cancelFunc`
- [ ] Maintain a `chainRegistry map[string]context.CancelFunc` in memory (in `main.go` or a dedicated manager)
- [ ] `POST /admin/chains` starts a new goroutine and registers its cancel
- [ ] `DELETE /admin/chains/:id` calls its cancel, then purges DB

---

### Phase 2 — Backend admin routes (api-admin.go)

All routes are registered under `/admin` with `adminAuthMiddleware`.

#### 2.1 — Live status
- [ ] `GET /admin/status` — chain heights, active alert counts, total users/webhooks/chats

#### 2.2 — Chain management
- [ ] `GET /admin/chains` — list all chains from config + live DB status
- [ ] `POST /admin/chains` — add chain (write config.yaml + start goroutine)
- [ ] `PUT /admin/chains/:id` — enable/disable/update RPC endpoints (write config.yaml + reload)
- [ ] `DELETE /admin/chains/:id` — stop goroutine + remove from config + purge all DB data
- [ ] `POST /admin/chains/:id/reinit` — purge `daily_participations`, `daily_participation_agregas`, `alert_logs`
- [ ] `POST /admin/config/reload` — hot-reload `config.yaml` into `internal.Config`

#### 2.3 — Alert configuration
- [ ] `GET /admin/config/thresholds` — return all `admin_config` key/values
- [ ] `PUT /admin/config/thresholds` — update one or more keys, refresh in-memory cache

#### 2.4 — Alert management
- [ ] `GET /admin/alerts` — list `alert_logs` with filters: `?chain=`, `?level=`, `?from=`, `?to=`
- [ ] `DELETE /admin/alerts` — purge `alert_logs` for a given `?chain=`

#### 2.5 — Moniker management
- [ ] `GET /admin/monikers` — list all `addr_monikers` rows, optionally filtered by `?chain=`
- [ ] `POST /admin/monikers` — insert override (chain, addr, moniker)
- [ ] `PUT /admin/monikers/:chain/:addr` — update moniker name
- [ ] `DELETE /admin/monikers/:chain/:addr` — delete override

#### 2.6 — User management
- [ ] `GET /admin/users` — list all users (user_id, email, name, created_at)
- [ ] `DELETE /admin/users/:id` — delete user + cascade webhooks, alert_contacts, hour_reports

#### 2.7 — Webhook management
- [ ] `GET /admin/webhooks` — list all webhooks (validator + govdao) across all users and chains
- [ ] `DELETE /admin/webhooks/:type/:id` — delete any webhook (`type` = `validator` or `govdao`)
- [ ] `PUT /admin/webhooks/govdao/:id/reset` — reset `last_checked_id` to unblock stuck tracker

#### 2.8 — Telegram management
- [ ] `GET /admin/telegram/chats` — list all rows from `telegrams` table
- [ ] `DELETE /admin/telegram/chats/:id` — deregister chat + cascade `telegram_hour_reports` + `telegram_validator_subs`
- [ ] `GET /admin/telegram/subs` — list all `telegram_validator_subs`, filterable by `?chain=`, `?chat_id=`
- [ ] `PUT /admin/telegram/subs/:id` — toggle `activate` field
- [ ] `GET /admin/telegram/schedules` — list all `telegram_hour_reports`
- [ ] `PUT /admin/telegram/schedules/:chat_id/:chain` — edit hour/minute/timezone/activate

#### 2.9 — Web user report schedules
- [ ] `GET /admin/schedules` — list all `hour_reports`
- [ ] `PUT /admin/schedules/:user_id` — edit hour/minute/timezone

#### 2.10 — GovDAO proposals
- [ ] `GET /admin/govdao/proposals` — list `govdaos` table, filterable by `?chain=`

---

### Phase 3 — Frontend (panel/)

#### 3.1 — Project setup
- [ ] Init Vite + React project in `panel/`
- [ ] Configure proxy to backend API in `vite.config.ts`
- [ ] Add token login screen (stores token in localStorage)
- [ ] Add global `X-Admin-Token` header to all fetch calls

#### 3.2 — Dashboard page
- [ ] Per-chain status cards: block height, sync state, active alert count
- [ ] Last 10 incidents table
- [ ] Counts: users, webhooks, Telegram chats

#### 3.3 — Chain management page
- [ ] Table of all chains with enable/disable toggle
- [ ] Add chain form
- [ ] Remove chain button (with confirmation)
- [ ] Reinitialize chain button (with confirmation)

#### 3.4 — Alert configuration page
- [ ] Form to edit all `admin_config` thresholds
- [ ] Save button calls `PUT /admin/config/thresholds`

#### 3.5 — Alert history page
- [ ] Filterable table: chain, level, date range
- [ ] Purge button per chain (with confirmation)

#### 3.6 — Moniker management page
- [ ] Table of all overrides per chain
- [ ] Inline add / edit / delete

#### 3.7 — User management page
- [ ] Table of all users with their webhook count
- [ ] Delete user button (with confirmation)

#### 3.8 — Webhook management page
- [ ] Tabs: Validator webhooks / GovDAO webhooks
- [ ] Delete button per webhook
- [ ] Reset `last_checked_id` button for GovDAO webhooks

#### 3.9 — Telegram management page
- [ ] Tabs: Chats / Subscriptions / Schedules
- [ ] Deregister chat button
- [ ] Toggle subscription active state
- [ ] Edit schedule per chat/chain

#### 3.10 — GovDAO proposals page
- [ ] Table of tracked proposals per chain
- [ ] Linked webhook list per chain

---

## Files to create or modify

| File | Action | Purpose |
|------|--------|---------|
| `backend/internal/api/api-admin.go` | **Create** | All admin route handlers |
| `backend/internal/api/api.go` | **Modify** | Register `/admin` group + import admin middleware |
| `backend/internal/fonction.go` | **Modify** | Add `AdminToken` to config struct |
| `backend/internal/database/db_init.go` | **Modify** | Add `AdminConfig` model + migration |
| `backend/internal/database/db_metrics.go` | **Modify** | Add `GetAdminConfig` / `SetAdminConfig` helpers |
| `backend/internal/gnovalidator/gnovalidator_realtime.go` | **Modify** | Replace hardcoded thresholds with DB reads |
| `backend/internal/gnovalidator/aggregator.go` | **Modify** | Replace `rawRetentionDays` / `aggregatorPeriod` with DB reads |
| `backend/main.go` | **Modify** | Add goroutine registry for chain lifecycle |
| `config.yaml.template` | **Modify** | Add `admin_token` field |
| `panel/` | **Create** | Entire React frontend |

---

## Known Limitations (not in scope for this feature)

- Web user report schedules always use `DefaultChain` — visible in admin but not configurable per user yet.
- `chatChainState` (active chain per Telegram chat) is in-memory only — lost on restart.
- Removing a chain while its goroutine is mid-sync may cause a brief error log before the goroutine stops.
