# Validator latency and chain BFT API

`GET /api/reports/consensus?chain=<configured-chain-id>[&addr=<exact-validator-address>]`

This public, rate-limited endpoint supplies observations for the Memba validator
window without changing `GET /api/reports/validators`. The health endpoint keeps
its array response, all four period keys and all existing values. Existing
Memba and Katana clients incur no latency query or schema change.

## Response

```json
{
  "chain_id": "gnoland-1",
  "computed_at": "2026-10-27T08:00:00Z",
  "cache_ttl_seconds": 30,
  "bft": {
    "available": true,
    "window_blocks": 100,
    "from_height": 999901,
    "to_height": 1000000,
    "collected_heights": 100,
    "last_block_at": "2026-10-27T07:59:58Z",
    "margin_percent": 19.047619047619047,
    "online_voting_power": 6,
    "total_voting_power": 7
  },
  "validators": [
    {
      "addr": "g1example",
      "moniker": "example",
      "precommit_lag": {
        "last_24h": {
          "p50_ms": 20,
          "p90_ms": 45,
          "sample_count": 1000,
          "last_sample_at": "2026-10-27T07:59:58Z"
        },
        "last_7d": {
          "p50_ms": 21,
          "p90_ms": 51,
          "sample_count": 7000,
          "last_sample_at": "2026-10-27T07:59:58Z"
        }
      }
    }
  ]
}
```

`chain` is the monitoring configuration key, which can differ from the signing
network ID (`gnoland-1`). Omission uses the configured default chain. The result
always echoes the resolved configuration key. `addr` is an exact, case-sensitive
address filter; monikers and substrings do not match. The filter affects only
`validators`, never the chain-level `bft` observation.

The roster follows the health API's current voting-power snapshot: departed
validators are excluded once positive voting-power data exists; current members
with no participation history remain present with empty latency windows. Before
any positive voting-power snapshot exists, historical roster entries remain
visible, and BFT is unavailable. Snapshot membership reflects collection state,
not an independently queried live node; the existing collector refreshes it.

## Latency and missing data

Lag is **milliseconds relative to the earliest precommit timestamp in the same
commit**, based on validator-local clocks. It is not network arrival latency or
time after quorum. Compare trends; clock skew can affect these values.
`late_for_quorum` is a separate existing metric and is not a percentile input.

The endpoint reuses `GetPrecommitLatencyMultiWindow`, also used by Telegram
`/validator`. Each rolling window uses the raw records with a non-null recorded
lag, with PostgreSQL `percentile_cont(0.5/0.9)`. `sample_count` counts exactly
those percentile input records, and `last_sample_at` is their maximum stored
block date. Missed blocks without lag do not become zero-lag samples. The raw
retention policy must retain at least seven days to cover the complete 7-day
window; daily aggregated percentiles cannot reconstruct a raw percentile. Counts
and dates describe the retained observations, not a guarantee of full coverage.

An empty window is always present as:

```json
{"p50_ms":null,"p90_ms":null,"sample_count":0,"last_sample_at":null}
```

A measured zero remains `0`, not null. A window can hold old samples during a
collection interruption; clients must inspect `last_sample_at` and avoid
presenting them as current. `computed_at` records when this response was built,
**not** when the chain was collected. The two timestamps serve different purposes.

## BFT semantics

The margin uses exactly the live block-window evaluator behind
`gnoland_chain_bft_margin_percent` and the quorum-loss alert, including its
trailing-miss rule and voting-power weights. It is not the daily report's 24-hour
summary, and it is not part of an individual validator's health score.

`margin_percent = 100 * (online_voting_power / total_voting_power - 2/3)`.
Zero or a negative value is a measured loss of quorum margin and is preserved.
The endpoint calculates observations even when BFT alert dispatch is disabled;
it neither sends alerts nor changes alert hysteresis.

The window size comes from the existing bounded `bft_alert_window_blocks`
threshold (default 100, valid range 1..10000). `last_block_at` is the stored date
of its highest collected height. The observation is unavailable if no rows were
collected, fewer than 80% of its heights were collected, the latest block is
older than two BFT check intervals (minimum ten minutes), or no positive
voting-power snapshot exists. These match the alert evaluator's gates.

An unavailable observation sets `available:false`, includes one stable `reason`
value in the following evaluation order, and uses null for `margin_percent`,
`online_voting_power` and `total_voting_power`:

| reason | Meaning |
| --- | --- |
| `no_data` | No block heights collected for the chain |
| `insufficient_coverage` | Fewer than 80% of the bounded height span collected |
| `stale` | Latest collected block exceeds the evaluator's freshness limit |
| `no_voting_power` | No usable positive voting-power snapshot |

Window/height metadata remains available where known. `last_block_at` is null
when no tip was collected. Unavailability is not a zero margin or a recovery.

## Errors, caching and compatibility

- GET succeeds with 200, including `validators:[]` for an unknown exact address,
  a departed validator, or an empty chain. A chain-wide BFT result is retained.
- Unknown chain keys, address filters longer than 128 bytes, surrounding
  whitespace or control characters return 400. Other nonmatching exact strings
  return an empty list.
- POST/other unsupported methods return 405 with `Allow: GET, OPTIONS`.
  OPTIONS is unauthenticated and returns 204 with the configured CORS headers.
- Database/timeout failures return 503, not a successful empty report, and do not
  populate the cache. Database details are not returned to the client.
- The existing public-route per-IP limiter applies, including 429/Retry-After;
  no authentication is required.
- A handler/DB-local cache shares each chain snapshot for at most 30 seconds.
  Different address filters reuse it; concurrent cold requests are coalesced.
  Failed loads are not cached. At most 128 chains are cached and expired entries
  are pruned. Each load passes a ten-second context deadline to the SQL queries.
  A shared load survives one reader disconnecting; each reader can cancel its
  own wait immediately without canceling the other readers' SQL work.
- Responses send `Cache-Control: no-store`; intermediary caching must not extend
  the snapshot lifetime. `computed_at` is unchanged on an internal cache hit.
- The health report remains unchanged. Keep the legacy four-period response for
  Memba/Katana; consumers may fetch this new endpoint independently and retain
  their health card if the observability request fails.

Tests exercise a real PostgreSQL instance, including chain separation, absent
samples versus measured zero, negative BFT margin, stale/partial/no-VP windows,
cache expiry/DB isolation/concurrent scans, cancellation/retry, CORS and methods.
