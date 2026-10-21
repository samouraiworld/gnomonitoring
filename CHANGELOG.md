# Changelog

All notable changes to Gnomonitoring are documented here.
Entries are ordered newest-first within each section.

---

## [Unreleased]

### Changed

- **Validator monikers resolve through one shared rule** — every query that
  returns a moniker (alerts, health and daily reports, score report,
  Prometheus labels, REST API, Telegram lists) now uses
  `database.MonikerSQL` instead of its own `LEFT JOIN addr_monikers` +
  `COALESCE`/`MAX` variant. Order: `addr_monikers`, then the moniker of the
  validator's latest `daily_participations` row, then its latest named
  `daily_participation_agregas` day, then its latest named non-latency
  `alert_logs` row, then `''` or the address (each query keeps its previous
  last resort). Empty and `unknown` names never count at any step. The frozen
  fallback is now the most recent name, not the lexically largest one
  `MAX(moniker)` returned (#110). When `addr_monikers` has a name, nothing
  changes. Without one, queries that used to fall back to the address
  (latency alerts, the 1h/24h/7d missed-block and precommit-latency metrics,
  first-seen, Telegram validator lists) now show the frozen name first. The
  `/latest_incidents` query resolves monikers only for the rows it returns.
  No schema change; the frozen moniker columns are still written (#108).

- **Periodic background jobs run on fixed, separate phases to flatten
  Postgres CPU spikes** — the 5-minute jobs used to start at boot and sleep
  after each run, so they fired together and drifted together. They now run on
  wall-clock slots of a shared 5-minute cycle (package `jobclock`): GovDAO
  status watcher at +0s, valset refresh spread over +20s..+60s, Prometheus
  metrics at +60s, latency alert check spread over +180s..+300s (one slot per
  chain). Missed-block alert checks are spread evenly across
  `alert_check_interval_seconds`, one slot per chain. Slots follow chains added
  or removed at runtime. The plan assumes the default 5-minute periods. Each loop logs its period and offset at start, and the
  metrics update logs its duration. The first metrics cycle still runs at boot.
  One metrics update now runs at most 4 database queries at once instead of
  ~13. The hourly aggregator selects days with half-open timestamp ranges
  instead of `date::date` casts, so it uses the `(chain_id, date, addr)` index
  instead of scanning the chain, and its day scan is bounded to the days after
  the aggregation watermark. The original `date::date` predicate is kept
  alongside the index bounds, so the aggregator selects the same days and
  writes the same rows as before, whatever TimeZone the DSN sets. No schema, index or
  configuration change.

- **GovDAO "Interact & Vote" links now point to memba.club** — new-proposal
  notifications on Discord, Slack and Telegram link to
  `https://memba.club/os/dao/govdao/proposals/<id>` instead of the retired
  `memba.samourai.app` path. The URL is built in one place,
  `telegram.MembaProposalURL`.

- **The daily-summary validator report link is a full URL, `report_url`** —
  the admin_config key `report_base_url` (the link was always
  `<report_base_url>/reports/<chain>`) is replaced by `report_url`, used
  verbatim with an optional `{chain}` placeholder, so the link can target a
  page such as `https://memba.club/os/validators`. It is now editable from the
  panel Reports page. `PUT /admin/config/thresholds` rejects a `report_url`
  that is not an absolute http(s) URL or contains spaces or any of
  `` < > " ' ` & | ``, since the value is inserted verbatim into Telegram (HTML),
  Slack and Discord messages. An existing `report_base_url` value is no
  longer read: the link stays off until `report_url` is set.

- **The root `docker-compose.yml` is now `docker-compose-prod.yml`, and the
  local devnet has its own `compose_dev_chain.yml`** — the root compose
  declared the external `gnoland-test_default` devnet network, and
  `backend/docker-compose.yml` attached the backend to it unconditionally, so
  a plain `docker compose up -d` failed whenever the local devnet was not
  running. Production now runs `docker compose -f docker-compose-prod.yml up
  -d` with no devnet dependency (project name pinned to `gnomonitoring`, so the
  `gnomonitoring_pgdata` volume is unchanged), and `backend/docker-compose.yml`
  no longer joins the devnet network. Any deploy script calling a bare
  `docker compose` from a checkout of this repository must pass `-f
  docker-compose-prod.yml`.

  `compose_dev_chain.yml` runs the gnoland-test devnet, the backend and
  Postgres as one project (`cd gnoland-test && make dev-up`). Every start
  resets the chain to block 0 and purges the `dev` chain rows (webhooks,
  alert contacts and Telegram subscriptions are kept) via the one-shot
  `chain-reset` service, which skips the reset if the validators are already
  running. The backend, tx-indexer and gnoweb wait for a `validator`
  healthcheck (RPC answering with at least one block), which removes the
  "restart the backend after every reset" step and the spurious RPC-outage
  alert of a backend started before the chain. Backend config:
  `backend/config_dev_chain.yaml` (gitignored, from the committed template).

### Added

- **Daily report shows the BFT margin and each flagged validator's proposed
  blocks** (#99) — every daily report (Discord, Slack, Telegram) gains a line
  such as `🛡️ BFT margin (last 24h): 6/7 validators online (85.7% VP) — 19.0%
  VP above 2/3 quorum, can lose 1 more validator (largest first) ⚠️`. Only
  voting power counts: a validator is online when it signed at least 50% of
  the last 24h blocks, and the tolerable losses remove the largest online
  validators first (worst case). Before the first voting-power snapshot the
  line reads `unavailable` instead of showing a wrong number. Each Watch or
  Critical validator line now ends with `Proposed: N` over the same last-24h
  window.

- **Signing-latency alerts (`LATENCY` / `LATENCY_RESOLVED`)** — a validator
  whose median precommit lag over the last hour is at once >=
  `latency_alert_min_lag_ms` (default 50) and >= `latency_alert_peer_factor`
  (default 3) times its peers' median now raises a `LATENCY` alert, resolved
  with 0.6x hysteresis. Such a validator misses no block, so the alert carries
  no contact mention, does not affect the health score, and is worded
  distinctly ("No blocks missed. Check flush_throttle_timeout,
  peer_gossip_sleep_duration, timeout_commit and NTP sync."). The comparison is
  made on the lag rather than on the `late_for_quorum` ratio, which is biased by
  voting power: a high-VP validator often completes the quorum itself and can
  almost never be late, a low-VP one never can. Rows land in `alert_logs` under
  the new levels and are counted as `gnoland_active_alerts{level="LATENCY"}`;
  `GetActiveAlertCount` now restricts its latest-row lookup to the missed-block
  levels, fixing a case where a newer row of any other level hid an unresolved
  WARNING or CRITICAL from that count. Tunable from the panel's Alert
  Configuration page; kill switch `latency_alert_enabled=false`, no restart.

- **Precommit signing latency per validator** — every block already fetched
  now also records, per signing validator, `precommit_lag_ms` (precommit
  timestamp minus the earliest one of the same commit) and `late_for_quorum`
  (signed after the precommit that brought voting power above 2/3), on
  `daily_participations`, rolled up daily into `daily_participation_agregas`
  (`late_for_quorum_count`, `late_for_quorum_samples`, `precommit_lag_p50_ms`,
  `precommit_lag_p90_ms`). Exposed as `gnoland_validator_precommit_lag_ms`
  and `gnoland_validator_late_for_quorum_ratio` (1h/24h/7d). No extra RPC
  call. Explains "missed blocks" reported by tools reading a node's seen
  commit while gnomonitoring shows full participation. Pre-existing rows stay
  `NULL` (no backfill); metrics fill forward from deploy. Timestamps come from
  validator-local clocks, and quorum uses the latest known voting powers.
  The 7d window needs `raw_retention_days >= 7`.

- **Alert contacts are mentioned on WARNING alerts too, and can target a
  Discord role** — `SendAllValidatorAlerts` used to attach contact mentions
  to CRITICAL alerts only; WARNING alerts now carry them as well (RESOLVED and
  info notices still never ping). A `mention_tag` prefixed with `&`
  (`&<role_id>`) is rendered as a role mention `<@&id>` instead of a user
  mention: a snowflake alone does not say whether it identifies a user or a
  role, so the prefix carries the type, and no migration or new API field is
  needed. `POST`/`PUT /alert-contacts` also accept a pasted `<@id>`, `<@!id>`
  or `<@&id>` and store the canonical form. Discord alert payloads now send an
  explicit `allowed_mentions` listing exactly the contact IDs, so nothing else
  in the message can ping. Role tags are dropped on Slack webhooks, and
  contacts with an empty tag no longer render a stray `<@>`.

  Note: a WARNING that escalates to CRITICAL now pings its contacts twice (the
  resend dedup is per level), and a flapping validator can ping every
  `alert_warning_resend_hours` (default 6h).

- **`/latest_incidents` can return one validator's incident history** — new
  optional `addr` and `limit` (1–100) query parameters. Without them the
  endpoint is unchanged: the chain's 10 most recent incidents. That cap is why a
  client asking about one validator (a validator profile page) saw few or none
  of its incidents as soon as another validator had been noisy. `addr` accepts
  lowercase letters and digits only (at most 64), and an out-of-range `limit` is
  rejected with `400` rather than silently clamped. The chain-wide
  pseudo-addresses `all` (chain stagnation) and `rpc` (RPC outage) are refused
  as well — their messages embed raw client errors and endpoint names, and this
  endpoint is unauthenticated — and so is either parameter present but empty, so
  that `addr=` never answers with the chain-wide list. Backed by
  `database.GetAlertLogFiltered`; `GetAlertLog` keeps its signature and
  delegates to it.

- **Keycloak authentication, behind an `auth_provider` config switch** — the API
  can now be protected by the self-hosted `gno-world` Keycloak realm instead of
  Clerk, as Phase 1 of the org-wide migration off Clerk. `auth_provider:
  "clerk"` (the default when the key is absent) leaves every existing
  deployment on the previous path unchanged; `"keycloak"` verifies bearer
  tokens against the realm's JWKS (`internal/keycloakauth`) and reads the admin
  role for `/admin/*` straight out of the validated token, replacing a live
  Clerk API call per admin request. Both paths ship in the binary, so the
  backend rolls back by editing config rather than reverting code.

  The admin role is the `admin` role scoped to the `gnomonitoring-panel`
  *client*, not a realm role: `gno-world` is shared with memba and gnolove, and
  a realm-wide role would make an admin on any one of them an admin on all
  three. `keycloak_allowed_clients` (default `["gnomonitoring-panel"]`)
  additionally restricts which clients' tokens are accepted at all.

  No database migration: every stored `user_id` is a Clerk id, and the realm
  carries that value forward as a `clerk_user_id` claim which
  `Claims.EffectiveUserID()` prefers over Keycloak's own `sub`.

  The general routes (`/webhooks/*`, `/users`, `/alert-contacts`, `/usersH`)
  accept *both* token types in Keycloak mode — memba's `/alerts` page and
  gnolove's leaderboard-webhooks route call them with tokens from those apps'
  own Clerk sessions, and those apps migrate on their own schedule. Set
  `keycloak_clerk_fallback: false` to drop the bridge once both have moved.

  A non-admin who authenticates now gets a clear "Access denied" page instead
  of a panel whose every request fails with 403 — the realm is shared, and its
  social identity providers self-provision accounts, so reaching the panel while
  holding no admin role is an ordinary case rather than an anomaly. The check is
  UX only; the backend remains the authorization boundary.

  The admin panel now uses `keycloak-js` in place of `@clerk/clerk-react`,
  wired into the pre-existing `setTokenProvider` seam in `panel/src/lib/api.ts`
  so no other panel code changed. See `docs/authentication.md`, including the
  realm prerequisite that must be satisfied before the production cutover.

### Fixed

- **Unreachable Keycloak issuer no longer stops the backend** — in keycloak
  mode, a failed OIDC discovery at boot used to exit the process, taking
  monitoring and alerting down with it. The backend now starts, retries
  discovery in the background with backoff (5 s up to 5 min), and answers
  `503` with `Retry-After` to Keycloak tokens until it succeeds. With the
  Clerk fallback on, the general routes keep accepting Clerk tokens
  meanwhile. Requests to the issuer now time out after 15 s, and a malformed
  `keycloak_issuer` still stops startup. See `docs/authentication.md`.

- **GovDAO status checks skip disabled chains** — `CheckProposalStatus` only
  polls proposals of enabled chains. It used to poll every stored proposal and
  log `no RPC pool registered` for each proposal of a disabled chain every
  5 minutes. Rows of disabled chains stay untouched.

- **Daily report link is clickable on Discord** — the `Full report` link sat in
  the embed footer, which Discord renders as plain text. It now goes on the
  embed title and in a last Markdown-link field.

- **Daily report survives untrusted monikers and report URLs** — validator
  names are cut to 64 characters and escaped in Slack (a moniker can no
  longer trigger `<!channel>`); the Telegram report stops adding lines before
  the 4096-character limit and summarizes the rest; `report_url` is
  re-validated when the report is built and capped at 512 characters, so an
  invalid stored value drops the link instead of the whole report.

- **Valoper profiles are fetched once per refresh, with a retry and the last
  known values kept on failure** — the moniker refresh and the chain health
  snapshot each fetched every valoper profile. The moniker refresh now parses
  both the Signing Address and the Server Type from one fetch, and the health
  snapshot reads the Server Type from that result instead of querying the
  realm again. This also fixes an empty Server Type for every validator whose
  signing address differs from its operator address: the health snapshot
  queried the realm by signing address, but the realm is keyed by operator
  address. A failed profile fetch is retried twice with backoff, and a profile
  that still cannot be fetched keeps its last known values instead of being
  reset, so a transient RPC error no longer turns its moniker into `unknown`.
  Profiles are fetched by a fixed pool of 10 workers, and the whole profile
  fetch is bounded to 30 s per refresh. An operator-address key in the moniker
  lookup can no longer shadow another valoper's signing-address key. The
  Signing Address and Server Type fields are read from the rendered field
  lines only, so a profile description that imitates a field line is ignored.

- **`/latest_incidents` returns a deterministic row order and UTC period
  bounds** — rows that share an `endHeight` (a `RESOLVED` row and the alert it
  closes, every `rpc` row at height 0) are now ordered by `sentAt`, newest
  first, so `LIMIT` no longer drops a newer row for an older one and two
  identical requests return the same order (#137). `current_week` starts on
  Monday 00:00 UTC instead of the process's local midnight, as in the
  validator report and for the Prometheus period metrics, and a row sent at
  the exact first instant of the next period is no longer counted in both
  (#139). The production image has no timezone set and already ran in UTC, so
  only deployments on a non-UTC host see a different week. `all_time` no
  longer runs a chain-wide `MIN/MAX(sent_at)` query first (#140). The README
  now states that the endpoint returns the whole alert log, including
  `RESOLVED` and `LATENCY` rows and the `all`/`rpc` system rows (#138); those rows are
  still returned, and the response shape is unchanged.
- **Telegram and GovDAO bot hardening** — a batch of notification and command
  fixes:
  - GovDAO status-change notices (accepted/rejected) now HTML-escape the
    proposal title and URL. A title with `&` or `<` used to make Telegram
    reject the message, so the notice was lost for every chat.
  - GovDAO Telegram notices now reach only the chats whose active chain
    (`/setchain`, else `default_chain`) is the proposal's chain. A chat whose
    stored chain is no longer enabled (for example the `betanet` migration
    default) counts as being on `default_chain`. A newly registered chat gets
    the latest proposal of its own chain instead of the latest of any chain.
  - `/subscribe` escapes monikers, splits long lists across messages, and
    replies with one summary instead of one message per validator for
    `on`/`off`.
  - Pagination buttons carry the chain they were built for, so paging an old
    message after `/setchain` keeps showing the right chain.
  - GovDAO bot: `/status` and `/executedproposals` clamp `limit` to 1-50 and
    reply with an error message instead of an empty one on a DB error; list
    headings end with a newline; `/executedproposals` and `/lastproposal` no
    longer label proposals "New Proposal".
  - Both bots answer `/start` (same text as `/help`) and ignore commands
    addressed to another bot (`/status@OtherBot`) in shared groups.
  - Remaining French user-facing strings are translated to English, and the
    `/report` usage hint uses HTML `<code>` instead of Markdown backticks.
- **Build artifacts were tracked** — `backend/backend`, `backend/coverage.out`
  and `backend/gno.log` are removed from the repository and ignored.

- **`LoadConfig` accumulated duplicate chain IDs when called twice in one
  process** — it appended to the package-level `EnabledChains` without
  resetting it first, so a second call produced duplicates and could leave
  `DefaultChain` resolved against a corrupted list. Only tests call it twice
  today, but the reset belongs in `LoadConfig` either way.


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
