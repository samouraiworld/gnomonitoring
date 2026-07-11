# Reports Page — Score Legend + Header Tooltips Design

**Status:** Approved (design). Ready for implementation plan.
**Scope:** Panel only. No backend change, no new network request.
**Goal:** Help users understand the validator report by adding (1) a collapsible "How is the score calculated?" panel showing the live scoring formula, and (2) a definition tooltip on each table-header column.

## Context

The Reports page (`panel/src/pages/Reports.tsx`) renders the validator report table. It already fetches `/admin/config/thresholds`, which returns the **entire** `admin_config` key/value map into the `thresholds` state. The score weights (`report_score_*`) are **not seeded** — when a key is absent the backend falls back to `score.DefaultWeights()`. The panel therefore reads weights from `thresholds` and applies the same defaults locally; no new endpoint is needed.

## Decisions (locked)

- **Legend form:** a collapsible panel titled "How is the score calculated?", placed above the filters, **closed by default**, toggled by click (`useState`).
- **Weights source:** live from the already-fetched `thresholds` map, with a panel-side `DEFAULT_WEIGHTS` fallback mirroring `score.DefaultWeights()`. Tier bands are constants.
- **Tooltips:** custom styled (themed) tooltips on each `<th>`, immediate on hover; the header keeps its click-to-sort behavior.
- **Language:** English, to match the existing panel UI.
- **No backend change, no new test beyond `npm run build` (typecheck).**

## Global Constraints

- English only for all UI copy, comments, commit messages.
- Frontend-only: `panel/` files exclusively. No API, DB, or Go change.
- Reuse the existing `thresholds` fetch — do not add a network call.
- The two features are additive; existing table behavior (sorting, filtering, CSV, period selector) is unchanged.

---

## Component 1 — "How is the score calculated?" panel

**Placement & behavior.** A collapsible block between the page header and the filters card, closed by default. A clickable row ("▸ How is the score calculated?") toggles a `useState<boolean>`; open state reveals the explanation.

**Content (English), rendered with the live weights:**
- `Score = clamp(presence − penalties, 0, 100)`
- `presence = (sign_weight × Sign% + proposer_weight × Proposer%) / (sign_weight + proposer_weight)` — the proposer term is dropped (→ `presence = Sign%`) when too few proposals are expected to be a reliable signal (`report_score_proposer_min_expected`).
- `penalties = (critical + warning + downtime) × severity`:
  - −`critical_weight` per CRITICAL alert (capped at `critical_cap`)
  - −`warning_weight` per WARNING alert (capped at `warning_cap`)
  - −1 per `downtime_blocks_per_point` downtime blocks (capped at `downtime_cap`)
- `severity = 1 + vp_severity_factor × (VP / max VP)` — higher-stake validators are penalized more heavily.
- **Tiers:** Excellent ≥ 85 · Good ≥ 60 · Watch ≥ 30 · Critical < 30 (constants).

**Weights.** A `DEFAULT_WEIGHTS` const in the panel mirrors `score.DefaultWeights()` (documented as such, pointing at `backend/internal/score/score.go`):

| key | default |
|-----|---------|
| `report_score_critical_weight` | 6 |
| `report_score_critical_cap` | 60 |
| `report_score_warning_weight` | 2 |
| `report_score_warning_cap` | 20 |
| `report_score_downtime_blocks_per_point` | 500 |
| `report_score_downtime_cap` | 20 |
| `report_score_proposer_min_expected` | 5 |
| `report_score_sign_weight` | 0.8 |
| `report_score_proposer_weight` | 0.2 |
| `report_score_vp_severity_factor` | 0.5 |

Each displayed value = `thresholds[key] ?? DEFAULT_WEIGHTS[key]`.

**Structure.** Extract a `ScoreLegend` component (`panel/src/components/ScoreLegend.tsx`) that takes the effective weights as props and owns its open/close state (or receives it from the page). Keeps `Reports.tsx` focused.

---

## Component 2 — Column header tooltips

**Mechanism.** A small reusable header-tip (either a `HeaderTip` component or a CSS-only wrapper using `data-tip` + `:hover::after`), themed to the panel (dark background, rounded, subtle shadow, immediate). It wraps the header label text; the `<th>`'s `onClick` sort handler and the sort indicator are preserved.

**Per-column definitions (English), short:**

| Column | Tooltip |
|--------|---------|
| Moniker | Validator display name (from the monikers table). |
| Address | Validator bech32 address. |
| Last alert (d) | Full days since the validator's most recent WARNING/CRITICAL alert; "—" if it never alerted. |
| Score | Health score 0–100 = presence − VP-weighted penalties, clamped. |
| Tier | Score band: Excellent ≥85, Good ≥60, Watch ≥30, Critical <30. |
| Sign % | Share of blocks the validator signed this period (participated / total). The base of the score. |
| VP | Current voting power (latest snapshot). |
| Proposer % | Proposer reliability: proposed vs expected proposals (by VP share); "—" when too few proposals are expected to be meaningful. |
| Critical | Number of CRITICAL alerts in the period (includes resends). |
| Warning | Number of WARNING alerts in the period (includes resends). |
| Downtime Blocks | Blocks of downtime summed over CRITICAL outages (CRITICAL only). |
| Missed | Blocks not signed this period (total − signed). Already reflected in Sign % and Score. |

---

## Files Touched

- **New:** `panel/src/components/ScoreLegend.tsx` (collapsible panel); a `HeaderTip` (small component file or styles).
- **Modified:** `panel/src/pages/Reports.tsx` (render the legend with effective weights; wrap the 12 headers in `HeaderTip`), and the panel's global stylesheet for the tooltip/panel styling.

## Testing

- `cd panel && npm run build` (typecheck + production build succeeds). No render unit tests — the panel has none.

## Out of Scope

- No change to how the score is computed. The legend is descriptive only.
- No backend endpoint, no persisted weights seeding. If a weight key is absent, the panel shows the code default (same value the backend uses).
- No per-cell tooltips; only the header definitions.
