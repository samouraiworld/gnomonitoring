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
- `days_since_last_alert` (integer \| null): Global (period-independent) recency signal — full days elapsed since the validator's most recent WARNING or CRITICAL alert. **`null`** when the validator has never alerted. Chain-wide "blockchain stuck" rows (`addr = 'all'`) are excluded.
- `periods` (object): Map of period key to period score. Every validator always has all four period keys.

**Period Score Object:**
- `score` (integer): Health score from 0–100
- `tier` (string): One of `"Excellent"`, `"Good"`, `"Watch"`, or `"Critical"`
- `sign_rate` (number): Block-signing availability over the period, `0–100` (= `100 × participated_blocks / total_blocks`). This is the base of the score.
- `proposer_reliability` (number \| null): Proposer liveness over the period, `0–100` (= `clamp(proposed_blocks / expected_proposals, 0, 1) × 100`, where `expected_proposals = voting_power_share × chain_blocks`). **`null`** when the validator's expected proposal count is below `report_score_proposer_min_expected` (too few expected proposals to be a reliable signal), in which case the proposer component is dropped from the score.
- `voting_power` (integer): The validator's current voting power (latest snapshot). `0` until the first snapshot is captured.
- `critical_count` (integer): Total count of CRITICAL alert rows in the period (includes resends)
- `warning_count` (integer): Total count of WARNING alert rows in the period (includes resends)
- `incident_count` (integer): Number of *distinct* WARNING/CRITICAL incidents in the period — consecutive alert rows not separated by a RESOLVED collapse into one incident (an escalation from WARNING to CRITICAL is one incident, not two). Unlike `critical_count`/`warning_count`, this isn't inflated by alert resends, so it's a better signal for a validator that flaps in and out repeatedly.
- `downtime_blocks` (integer): Sum of downtime blocks for CRITICAL alerts. Ongoing outages with `end_height = 0` contribute 0 blocks.
- `missed_blocks` (integer): Blocks the validator failed to sign over the period (= `total_blocks − participated_blocks`). Display-only — already reflected in `sign_rate`/`score`, not an additional penalty.

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

The score is a **layered model** combining real availability, proposer liveness, incident history, and network-impact weighting. It is computed per validator per period:

**1. Availability base (0–100):**
```
base = 100 × (participated_blocks / total_blocks)
```
Read from the durable `daily_participation_agregas` rollup for complete past days, unioned with current-day raw `daily_participations` rows (partitioned at today 00:00 UTC to avoid double counting). A validator that chronically misses blocks — even below the alert threshold — scores below 100 here.

**2. Proposer reliability (0–100):**
```
expected_proposals   = (voting_power / Σ voting_power) × chain_blocks
proposer_reliability = clamp(proposed_blocks / expected_proposals, 0, 1) × 100
```
Dropped (component removed, `proposer_reliability = null`) when `expected_proposals < report_score_proposer_min_expected`.

**3. Presence** — weighted blend of base and proposer reliability:
```
presence = (sign_weight × base + proposer_weight × proposer_reliability) / (sign_weight + proposer_weight)
```
When the proposer component is dropped, `presence = base`.

**4. Incident penalties (modifier):**
- **CRITICAL alerts:** −`critical_weight` per alert (capped at `critical_cap`)
- **WARNING alerts:** −`warning_weight` per alert (capped at `warning_cap`)
- **Distinct incidents:** −`freq_weight` per distinct WARNING/CRITICAL incident (capped at `freq_cap`) — see `incident_count` above
- **Downtime blocks:** −1 for every `downtime_blocks_per_point` blocks (capped at `downtime_cap`)

**5. Voting-power severity** — high-VP validators are penalized harder because their failures weigh more on consensus:
```
severity      = 1 + vp_severity_factor × (voting_power / max_voting_power)
total_penalty = (critical_penalty + warning_penalty + incident_penalty + downtime_penalty) × severity
```

**Final:**
```
score = clamp(presence − total_penalty, 0, 100)
```

**Weights and caps are tunable** via admin configuration keys:

| Key | Default | Meaning |
|-----|---------|---------|
| `report_score_critical_weight` | 6 | points per CRITICAL alert |
| `report_score_critical_cap` | 60 | max points lost to criticals |
| `report_score_warning_weight` | 2 | points per WARNING alert |
| `report_score_warning_cap` | 20 | max points lost to warnings |
| `report_score_freq_weight` | 3 | points per distinct incident |
| `report_score_freq_cap` | 30 | max points lost to incident frequency |
| `report_score_downtime_blocks_per_point` | 500 | downtime blocks that cost 1 point |
| `report_score_downtime_cap` | 20 | max points lost to downtime |
| `report_score_sign_weight` | 0.8 | availability weight in the presence blend |
| `report_score_proposer_weight` | 0.2 | proposer weight in the presence blend |
| `report_score_proposer_min_expected` | 5 | drop proposer component below this expected count |
| `report_score_vp_severity_factor` | 0.5 | severity ramp (top-VP validator → ×1.5) |

**Graceful degradation:** before the first voting-power snapshot is captured, `voting_power = 0` → `severity = 1` and the proposer component is dropped, so the score reduces to `100 × sign_rate − alert_penalties`.

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
        "score": 99,
        "tier": "Excellent",
        "sign_rate": 99.8,
        "proposer_reliability": 97.2,
        "voting_power": 1000,
        "critical_count": 0,
        "warning_count": 1,
        "incident_count": 1,
        "downtime_blocks": 0
      },
      "current_week": {
        "score": 98,
        "tier": "Excellent",
        "sign_rate": 99.1,
        "proposer_reliability": 95.0,
        "voting_power": 1000,
        "critical_count": 0,
        "warning_count": 2,
        "incident_count": 1,
        "downtime_blocks": 0
      },
      "current_month": {
        "score": 74,
        "tier": "Good",
        "sign_rate": 96.4,
        "proposer_reliability": 88.0,
        "voting_power": 1000,
        "critical_count": 3,
        "warning_count": 5,
        "incident_count": 4,
        "downtime_blocks": 250
      },
      "current_year": {
        "score": 41,
        "tier": "Watch",
        "sign_rate": 92.0,
        "proposer_reliability": 80.5,
        "voting_power": 1000,
        "critical_count": 8,
        "warning_count": 15,
        "incident_count": 9,
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
        "sign_rate": 100.0,
        "proposer_reliability": null,
        "voting_power": 12,
        "critical_count": 0,
        "warning_count": 0,
        "incident_count": 0,
        "downtime_blocks": 0
      },
      "current_week": {
        "score": 100,
        "tier": "Excellent",
        "sign_rate": 100.0,
        "proposer_reliability": null,
        "voting_power": 12,
        "critical_count": 0,
        "warning_count": 0,
        "incident_count": 0,
        "downtime_blocks": 0
      },
      "current_month": {
        "score": 93,
        "tier": "Excellent",
        "sign_rate": 99.5,
        "proposer_reliability": null,
        "voting_power": 12,
        "critical_count": 1,
        "warning_count": 1,
        "incident_count": 1,
        "downtime_blocks": 0
      },
      "current_year": {
        "score": 80,
        "tier": "Good",
        "sign_rate": 98.0,
        "proposer_reliability": null,
        "voting_power": 12,
        "critical_count": 3,
        "warning_count": 4,
        "incident_count": 3,
        "downtime_blocks": 500
      }
    }
  }
]
```

## Notes

- The response includes every validator **currently in the valset** (`voting_power > 0`) that participated on the chain during the current calendar year, not only validators that have alerted. A validator that signs every block with no alerts scores at or near 100 / tier "Excellent".
- **Validators that have left the valset are excluded entirely, from every period** (including `current_year`), even under an explicit `?addr=` match for their exact address. Membership is judged by `voting_power > 0` in `addr_monikers`, which is zeroed out (not just left stale) as soon as an address stops appearing in the live `/validators` response, refreshed every ~5 minutes. A validator that just left may still appear for up to that ~5-minute window before the next refresh excludes it.
- All remaining validators in the response have all four period keys populated. Under the sign-base model, a validator with **no participation data at all** in a period scores 0 / tier "Critical" for that period — the base is `100 × signed/total` and `total = 0` yields a sign rate of 0. This is intentional: absence of signing is treated as unavailability, not health.
- `voting_power` reflects the latest snapshot captured by the 5-minute moniker refresh. `proposer_reliability` is `null` for low-voting-power validators whose expected proposal count falls below `report_score_proposer_min_expected`.
- The `addr` filter returns only matching validators; if no validators match (including a departed validator's exact address), the response is an empty array.
- Invalid chain IDs return HTTP 400.
- This is a **public read endpoint**: it is not behind Clerk authentication in either `dev_mode` or production, consistent with the other read-only metric endpoints (`/uptime`, `/Participation`, `/missing_block`, etc.). Do not expose data through it that you would not expose on those endpoints. (The `/admin/*` configuration routes, including the report toggle, remain Clerk-protected.)
