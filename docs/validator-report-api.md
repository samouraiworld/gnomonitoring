# Validator Report API

The Validator Report API provides health scores and alert metrics for validators across configurable time periods.

## Endpoint

```
GET /api/reports/validators
```

### Availability

This endpoint is **always available** regardless of the per-chain `validator_report_enabled.<chainID>` configuration toggle. The toggle only controls whether the daily summary message includes a link to this report.

## Query Parameters

| Parameter | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `chain`   | string | Yes      | Chain ID (must be an enabled chain). Invalid chain returns HTTP 400. |
| `addr`    | string | No       | Filter to a single validator address. If omitted, returns all validators. |

If the `chain` parameter is omitted, the server falls back to its configured default chain.

## Response

The response is a JSON array of validator reports. Each element contains:

```json
{
  "addr": "gno1...",
  "moniker": "My Validator",
  "periods": {
    "last_24h": {...},
    "current_week": {...},
    "current_month": {...},
    "current_year": {...}
  }
}
```

### Response Schema

**Validator Report Object:**
- `addr` (string): Validator address (bech32 format)
- `moniker` (string): Validator display name from the monikers table, or empty string if not set
- `periods` (object): Map of period key to period score. Every validator always has all four period keys.

**Period Score Object:**
- `score` (integer): Health score from 0–100
- `tier` (string): One of `"Excellent"`, `"Good"`, `"Watch"`, or `"Critical"`
- `critical_count` (integer): Total count of CRITICAL alert rows in the period (includes resends)
- `warning_count` (integer): Total count of WARNING alert rows in the period (includes resends)
- `downtime_blocks` (integer): Sum of downtime blocks for CRITICAL alerts. Ongoing outages with `end_height = 0` contribute 0 blocks.

## Period Definitions

| Period ID | Bounds |
|-----------|--------|
| `last_24h` | Rolling 24 hours: `[now - 24h, now)` |
| `current_week` | Monday 00:00 local time through +7 days: `[Monday 00:00 local, Monday 00:00 local + 7 days)` |
| `current_month` | 1st of month 00:00 UTC through +1 month: `[1st 00:00 UTC, 1st 00:00 UTC + 1 month)` |
| `current_year` | January 1 00:00 UTC through December 31 23:59:59 UTC: `[Jan 1 00:00 UTC, Jan 1 00:00 UTC + 1 year)` |

## Tier Classification

Tiers are assigned based on the health score:

| Tier       | Score Range |
|------------|-------------|
| Excellent  | 85–100      |
| Good       | 60–84       |
| Watch      | 30–59       |
| Critical   | 0–29        |

## Scoring

The score is computed as follows:

**Base score:** 100

**Penalties:**
- **CRITICAL alerts:** −6 per alert (capped at −60 total)
- **Downtime blocks:** −1 for every 500 downtime blocks (capped at −20 total)

**WARNING alerts:** Informational only; they do not affect the score.

**Weights and caps are tunable** via admin configuration keys:
- `report_score_critical_weight` (default: 6)
- `report_score_critical_cap` (default: 60)
- `report_score_downtime_blocks_per_point` (default: 500)
- `report_score_downtime_cap` (default: 20)

The final score is clamped to the range [0, 100].

## Example

### Request

```bash
curl 'http://localhost:8989/api/reports/validators?chain=test12'
```

### Sample Response

```json
[
  {
    "addr": "gno1d3y4yqq92f5wyf4rh2hj5zt5x5z5x5z5x5z5",
    "moniker": "Validator Alpha",
    "periods": {
      "last_24h": {
        "score": 100,
        "tier": "Excellent",
        "critical_count": 0,
        "warning_count": 1,
        "downtime_blocks": 0
      },
      "current_week": {
        "score": 100,
        "tier": "Excellent",
        "critical_count": 0,
        "warning_count": 2,
        "downtime_blocks": 0
      },
      "current_month": {
        "score": 82,
        "tier": "Good",
        "critical_count": 3,
        "warning_count": 5,
        "downtime_blocks": 250
      },
      "current_year": {
        "score": 50,
        "tier": "Watch",
        "critical_count": 8,
        "warning_count": 15,
        "downtime_blocks": 1200
      }
    }
  },
  {
    "addr": "gno1f7u8k2q3r4s5t6u7v8w9x0y1z2a3b4c5d6e7f",
    "moniker": "Validator Beta",
    "periods": {
      "last_24h": {
        "score": 100,
        "tier": "Excellent",
        "critical_count": 0,
        "warning_count": 0,
        "downtime_blocks": 0
      },
      "current_week": {
        "score": 100,
        "tier": "Excellent",
        "critical_count": 0,
        "warning_count": 0,
        "downtime_blocks": 0
      },
      "current_month": {
        "score": 94,
        "tier": "Excellent",
        "critical_count": 1,
        "warning_count": 1,
        "downtime_blocks": 0
      },
      "current_year": {
        "score": 81,
        "tier": "Good",
        "critical_count": 3,
        "warning_count": 4,
        "downtime_blocks": 500
      }
    }
  }
]
```

## Notes

- The response includes every validator that participated on the chain during the current calendar year (the active-validator roster), not only validators that have alerted. Perfectly healthy validators appear with score 100 / tier "Excellent".
- All validators in the response have all four period keys populated, even if they have no alerts in a given period (in which case score = 100, tier = "Excellent").
- The `addr` filter returns only matching validators; if no validators match, the response is an empty array.
- Invalid chain IDs return HTTP 400.
- This is a **public read endpoint**: it is not behind Clerk authentication in either `dev_mode` or production, consistent with the other read-only metric endpoints (`/uptime`, `/Participation`, `/missing_block`, etc.). Do not expose data through it that you would not expose on those endpoints. (The `/admin/*` configuration routes, including the report toggle, remain Clerk-protected.)
