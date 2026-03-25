# Log Cleanup Plan

## Convention to Apply

**Uniform prefix** : `[component][chain]` on all logs.

Examples : `[monitor][betanet]`, `[aggregator][betanet]`, `[metrics][betanet]`, `[govdao][betanet]`, `[telegram]`, `[db]`, `[report]`, `[valoper][betanet]`

**Emojis** : removed from all internal logs. Kept only on messages sent as notifications (Discord/Slack/Telegram).

**Log levels** :
- **Lifecycle** : one line at startup and one summary line per cycle. Always displayed.
- **Warning** : degraded operation but running (retry, fallback). Always displayed.
- **Error** : operation failed, action required. Always displayed.
- **Debug / per-item** : per block, per validator, per DB line. **To be removed entirely.**

---

## Priority 1 — Immediate Removals (very noisy logs)

These logs generate hundreds or thousands of lines per hour during normal operation.

| File | Current Log | Action | Reason |
|---------|-----------|--------|--------|
| `gnovalidator/gnovalidator_realtime.go` | `✅ Saved participation for %s (%s) at height %d: %v` | **Replace** with `[monitor][%s] synced block %d` every 100 blocks (`if h % 100 == 0`) | Too verbose (1 line/validator/block) but necessary to confirm sync is alive |
| `gnovalidator/gnovalidator_realtime.go` | `⏱️ Skipping resolve alert for %s : already sent` | Remove | Fires for every validator every 20 seconds during normal operation |
| `gnovalidator/gnovalidator_realtime.go` | `==========================Start resolv Alert==========00==` | Remove | Decorative banner with typo, every 20 seconds per chain |
| `database/db_metrics.go` | `📦 Loaded from DB — Addr: %s, Moniker: %s` | Remove | Fires per validator at every moniker refresh (every 5 min) |
| `database/db_metrics.go` | `==========Start Get Participate Rate` | Remove | Every 5 min per chain + each Telegram `/status` command |
| `database/db_metrics.go` | `start %s` and `end %s` in `GetAlertLog` | Remove | Debug query parameters, fires on every API `/alerts` call |
| `gnovalidator/Prometheus.go` | 10x `→ Calculating XYZ...` and `→ XYZ: N validators` | Remove | Step-by-step progress report, every 5 min per chain |
| `gnovalidator/Prometheus.go` | `📈 [%s] PHASE 2: Chain metrics` and `🚨 [%s] PHASE 3: Alert metrics` | Remove | Phase labels, no operational value |
| `gnovalidator/Prometheus.go` | `-> Processing chain: %s` | Remove | Redundant with existing begin/end lines |
| `gnovalidator/gnovalidator_report.go` | `log.Println(msg)` (entire report body) | Remove | Prints full report to console on each send; already delivered via Telegram/webhook |
| `gnovalidator/gnovalidator_report.go` | `fmt.Printf("last block: %d\n", height)` | Remove | `fmt.Printf` without timestamp, forgotten debug log in production |
| `gnovalidator/gnovalidator_report.go` | `[CalculateRate] date=%s chain=%s` | Remove | Function entry trace, called on each report send |
| `gnovalidator/valoper.go` | `🔹 Validator: %s — Moniker: %s` in `InitMonikerMap` | Remove | Per validator at each refresh (20+ lines every 5 min) |
| `gnovalidator/valoper.go` | `✅ Fetched %d valopers from valopers.Render page %d` | Remove | Per page at each refresh; the summary line below is sufficient |
| `govdao/govdao.go` | `log.Println("Message sent:", string(message))` | Remove | Raw WebSocket payload dump on each on-chain event |
| `govdao/govdao.go` | `log.Printf("Title: %s", title)` | Remove | Debug print, title already stored in DB and sent as notification |
| `govdao/govdao.go` | `log.Printf("Block Height: %d", tx.BlockHeight)` | Remove | Debug print |
| `govdao/govdao.go` | `log.Printf("tx URL %s", txurl)` | Remove | Debug print |
| `govdao/govdao.go` | `log.Printf("ID: %d", idInt)` | Remove | Debug print |
| `govdao/govdao.go` | `log.Println(res)` in `ExtractProposalRender` | Remove | Raw Gno Markdown render dump (multi-line) to console on each check |
| `govdao/govdao.go` | `log.Printf("Blocks fetched: %+v\n", respData)` | Remove | Dump of entire GraphQL response |

---

## Priority 2 — Reformatting (useful logs to keep, format to fix)

### `gnovalidator/gnovalidator_realtime.go`

| Current Log | Proposed Log | Reason |
|-----------|------------|--------|
| `⚠️ Database empty get last block: %v` | `[monitor][%s] no stored blocks, starting from genesis` | Misleading message (`%v` displays `<nil>`); no chain context |
| `❌ Failed to get latest block height: %v` | Remove (duplicate with the log in the anti-spam guard just below) | Duplicate: the `sinceRPCErr` guard already logs the same event |
| `Error retrieving last block: %v` | `[monitor][%s] error fetching latest height: %v` | No prefix, inconsistent casing |
| `⚠️ Impossible de récupérer la date du block %d: %v` (x2) | `[monitor][%s] cannot get block time for height %d: %v` | French text, no chain context |
| `⏳ Backfill [%d..%d] (gap=%d)` | `[monitor][%s] backfill [%d..%d] (gap=%d)` | Missing chain context |
| `❌ backfill error: %v` | `[monitor][%s] backfill error: %v` | Missing chain context |
| `✅ Backfill done up to %d, switch to realtime` | `[monitor][%s] backfill complete up to %d, switching to realtime` | Missing chain context |
| `Erreur bloc %d: %v` | `[monitor][%s] error fetching block %d: %v` | French text, no prefix |
| `❌ Failed to save participation at height %d: %v` | `[monitor][%s] failed to save participation at height %d: %v` | Missing chain context |
| `🔁 Refresh MonikerMap...` | `[monitor][%s] refreshing moniker map` | Unnecessary emoji on internal log |
| `🚫 Too many alerte for %s, muting for 1h` | `[validator][%s] muting %s (%s) for 1h — too many alerts` | Typo "alerte", no chain context |

### `gnovalidator/Prometheus.go`

| Current Log | Proposed Log | Reason |
|-----------|------------|--------|
| `🔄 [%s] Starting metrics update...` | `[metrics][%s] updating` | Normalization |
| `✅ [%s] All metrics updated` | `[metrics][%s] update complete` | Normalization |
| `⚠️  [%s] TxContribution: all values are 0 — ...` | `[metrics][%s] TxContribution: all zero — proposer data may be missing` | Keep, normalize prefix |
| `PANIC in StartMetricsUpdater: %v` | `[metrics] panic recovered: %v` | No brackets, no prefix |
| `StartMetricsUpdater started. Enabled chains: %v` | `[metrics] started, chains: %v` | Normalization |
| `TIMEOUT metrics update cycle exceeded %v, ...` | `[metrics] cycle timed out after %v, remaining chains skipped` | Inconsistent casing |
| `ERROR [%s] metrics update: %v` | `[metrics][%s] update failed: %v` | Inconsistent prefix format |

### `gnovalidator/aggregator.go`

Remove emojis only, the structure `[aggregator][chain]` is already correct.

| Current Log | Proposed Log |
|-----------|------------|
| `❌ [aggregator] panic recovered: %v` | `[aggregator] panic recovered: %v` |
| `⚠️  [aggregator] restarting after panic` | `[aggregator] restarting after panic` |
| `❌ [aggregator][%s] aggregation failed: %v` | `[aggregator][%s] aggregation failed: %v` |
| `❌ [aggregator][%s] prune failed: %v` | `[aggregator][%s] prune failed: %v` |
| `✅ [aggregator][%s] aggregated %d rows over %d days` | `[aggregator][%s] aggregated %d rows over %d days` |
| `🗑️  [aggregator][%s] pruned %d raw rows (> %d days old)` | `[aggregator][%s] pruned %d raw rows (older than %d days)` |

### `gnovalidator/valoper.go`

| Current Log | Proposed Log | Reason |
|-----------|------------|--------|
| `🎉 Total valopers fetched: %d\n` | `[valoper][%s] fetched %d valopers` | Remove emoji, add chain |
| `✅ MonikerMap initialized with %d active validators\n` | `[valoper][%s] moniker map initialized: %d validators` | Normalization |
| `✅ addr_monikers synced (%d entries)` | Remove (merge with previous line) | Immediate duplicate with the line above |
| `⚠️ Failed to upsert addr_moniker %s: %v` | `[valoper][%s] failed to upsert moniker for %s: %v` | Missing chain context |
| `❌ Failed to retrieve validators after retries: %v` | `[valoper][%s] failed to retrieve validators after retries: %v` | Missing chain context |
| `🔁 Retry %d/%d after error: %v` | `[valoper] retry %d/%d: %v` | Remove emoji |

### `gnovalidator/gnovalidator_report.go`

| Current Log | Proposed Log | Reason |
|-----------|------------|--------|
| `🕓 Scheduled next report for %s at %s (%s)` | `[report] next for user %s at %s (in %s)` | Normalization |
| `🕓 Scheduled next report for chat %d chain %s at %s (%s)` | `[report][%s] next for chat %d at %s (in %s)` | Normalization |
| `⏰ Sending report for user %s` | `[report] sending for user %s` | Normalization |
| `⏰ Sending report for chat %d chain %s` | `[report][%s] sending for chat %d` | Normalization |
| `♻️ Reloading schedule for user %s` | `[report] reloading schedule for user %s` | Normalization |
| `♻️ Reloading schedule for chat %d chain %s` | `[report][%s] reloading schedule for chat %d` | Normalization |
| `[CalculateRate] Error querying participation: %v` | `[report][%s] error querying participation for %s: %v` | Normalization |

### `database/db_metrics.go`

| Current Log | Proposed Log | Reason |
|-----------|------------|--------|
| `Error invalid period %s` (x2) | `[db] invalid period: %v` | Wrong verb (`%s` on an `error`), no prefix |
| `✅ Loaded %d monikers from DB` | `[db] loaded %d monikers` | Normalization |
| `⚠️ createHourReport: %v` | `[db] createHourReport for user %s: %v` | Missing userID for traceability |
| `Invalid timezone '%s', defaulting to UTC` | `[db] invalid timezone %q, defaulting to UTC` | Use `%q` for strings with unexpected characters |

### `govdao/govdao.go`

| Current Log | Proposed Log | Reason |
|-----------|------------|--------|
| `Read error: ...` | `[govdao][%s] websocket read error: %v` | Missing chain context |
| `WebsocketGovdao dial error: %v — retrying in %s` | `[govdao][%s] dial error: %v — retrying in %s` | Missing chain context |
| `WebsocketGovdao lost connection — retrying in %s` | `[govdao][%s] connection lost — retrying in %s` | Missing chain context |
| `❌ WriteJSON initMsg: %v` | `[govdao][%s] send init message failed: %v` | Missing chain context |
| `Error fetch govdao %s` | `[govdao][%s] init fetch failed: %v` | Missing chain context, wrong verb |
| `✅ Proposal %d (%s) has been ACCEPTED!` | `[govdao][%s] proposal %d (%s) accepted` | Remove emoji and uppercase |
| `⏳ Checking proposal status...` | `[govdao] checking proposal statuses` | Normalization |
| `Error fetching proposals: %v` | `[govdao] error fetching proposals: %v` | Missing prefix |

### `telegram/validator.go`

| Current Log | Proposed Log | Reason |
|-----------|------------|--------|
| `error report activate%s` | `[telegram] report activate error: %v` | Missing space, wrong verb on an `error` |
| `send %s failed: %v` in `/report` handler | `[telegram] send /report failed: %v` | Wrong copy-paste: displays "/missing" instead of "/report" |
| `send %s failed: %v` in wildcard handler | `[telegram] send failed for unknown command chat=%d: %v` | Wrong copy-paste: displays "/status" in fallback |
| `⚠️ UpdateChatChain chat_id=%d: %v` | `[telegram] UpdateChatChain chat=%d: %v` | Remove emoji |

### `main.go`

| Current Log | Proposed Log | Reason |
|-----------|------------|--------|
| `Starting monitoring for chain: %s` | `[main] starting monitoring for chain %s` | Normalization |
| `Monitoring started for chain: %s` | Remove | Misleading: goroutine is launched, not confirmed started |
| `❌ Failed to initialize database: %v` | `[main] failed to initialize database: %v` | Remove emoji |
| `✅ Database connection established successfully` | `[main] database ready` | Remove emoji, shorten |
| `Spawning monitoring loops for %d enabled chains: %v` | `[main] enabled chains (%d): %v` | Normalization |
| `⚠️ Daily report scheduler disabled by flag` | `[main] daily report scheduler disabled` | Remove emoji |

---

## Architectural Issue to Fix

`log.Fatalf` in `ExtractTitle` and `ExtractProposalRender` in `govdao/govdao.go` :
these functions are called from goroutines. A `log.Fatalf` on a temporary RPC error crashes the entire process. These functions must return the error to the caller instead of calling `log.Fatalf`.

---

## Files Without Required Changes

- `gnovalidator/sync.go` — no logs
- `gnovalidator/metric.go` — no logs
- `database/db.go` — no logs
