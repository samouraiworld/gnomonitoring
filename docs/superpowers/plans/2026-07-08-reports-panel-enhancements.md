# Reports Panel & Alert History Enhancements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add sortable columns, a tier filter, and CSV export to the admin Reports page, and a validator-selection filter to Alert History.

**Architecture:** Pure front-end changes in `panel/` (React 19 + TypeScript, Vite). Report data (`ValidatorReport[]`, all four periods per validator) is already fully loaded client-side, and Alert History filters its already-fetched `AlertLog[]`. All new behavior is derived state + event handlers; no backend calls, no new dependencies.

**Tech Stack:** React 19, TypeScript ~5.9, Vite 8, inline styles matching existing `form-input` / `badge` / `card` / `table-container` conventions.

## Global Constraints

- No new npm dependencies.
- No backend changes (`GET /admin/alerts` gains no `addr` param).
- All code, comments, and copy in **English**.
- No JS unit-test harness exists in this repo. The per-task gate is `npm run build` (TypeScript type-check + Vite bundle) run from `panel/`, plus the manual check described in each task.
- Each task ends with a commit on the current feature branch (`feat/validator-report`), staging ONLY the file(s) that task touches — do not stage the pre-existing unrelated working-tree changes (`docker-compose.yml`, `gnoland-test/docker-compose.yml`). Do NOT add a `Co-Authored-By` trailer.
- Follow existing inline-style patterns; do not introduce a CSS framework or data-grid library.
- Preserve existing behavior: chain/period/text-filter selects, the Reports-enabled toggle, and the Alert History purge flow must keep working.

---

## File Structure

- `panel/src/pages/Reports.tsx` — MODIFY. Adds: tier-filter state + select, sort state + clickable headers, CSV export button + handler. Two module-level constants (`TIER_RANK`, `PERIOD_ORDER`) and one module-level helper (`csvEscape`).
- `panel/src/pages/AlertHistory.tsx` — MODIFY. Adds: validator-filter state + select, derived distinct-validator options (`useMemo`), auto-reset effect, and applies the filter to the rendered list + count.

No new files. No `panel/src/types/` changes (`ValidatorReport`, `PeriodScore`, `AlertLog` already cover everything).

---

## Task 1: Tier filter on Reports

**Files:**
- Modify: `panel/src/pages/Reports.tsx`

**Interfaces:**
- Consumes: existing `reports: ValidatorReport[]`, `period: ReportPeriod`, `filter: string` state; `PeriodScore.tier` values `'Excellent' | 'Good' | 'Watch' | 'Critical'`.
- Produces: a `filtered` array (text AND tier filtered) that Task 2 sorts and Task 3 exports. Keeps the same variable name `filtered`.

- [ ] **Step 1: Add tier-filter state**

In `Reports.tsx`, next to the existing `const [filter, setFilter] = useState('')` (around line 27), add:

```tsx
const [tierFilter, setTierFilter] = useState('')
```

- [ ] **Step 2: Replace the `filtered` computation to combine text + tier**

Replace the existing block (lines 74-78):

```tsx
const filtered = reports.filter(r => {
  if (!filter) return true
  const q = filter.toLowerCase()
  return r.addr.toLowerCase().includes(q) || r.moniker.toLowerCase().includes(q)
})
```

with:

```tsx
const filtered = reports.filter(r => {
  const p = r.periods[period]
  if (filter) {
    const q = filter.toLowerCase()
    if (!r.addr.toLowerCase().includes(q) && !r.moniker.toLowerCase().includes(q)) return false
  }
  if (tierFilter) {
    if (!p || p.tier !== tierFilter) return false
  }
  return true
})
```

- [ ] **Step 3: Add the tier `<select>` to the filter bar**

In the filter-bar `flex-gap` div, immediately after the period `<select>` (currently ending at line 102, before the text `<input>`), insert:

```tsx
<select className="form-input" value={tierFilter} onChange={e => setTierFilter(e.target.value)} style={{ width: 150 }}>
  <option value="">All Tiers</option>
  <option value="Excellent">Excellent</option>
  <option value="Good">Good</option>
  <option value="Watch">Watch</option>
  <option value="Critical">Critical</option>
</select>
```

- [ ] **Step 4: Verify the build**

Run from `panel/`:

```bash
npm run build
```

Expected: build succeeds with no TypeScript errors.

- [ ] **Step 5: Manual check**

Run `npm run dev`, open Reports. Confirm: the new "All Tiers" select appears; selecting `Critical` narrows the table to Critical-tier validators for the current period; the "N validators shown" subtitle updates; switching period re-evaluates the tier filter; text filter still works and combines with tier (AND).

---

## Task 2: Sortable columns on Reports

**Files:**
- Modify: `panel/src/pages/Reports.tsx`

**Interfaces:**
- Consumes: `filtered` from Task 1, `period: ReportPeriod`, `PeriodScore` fields (`score`, `tier`, `critical_count`, `warning_count`, `downtime_blocks`).
- Produces: a `sorted: ValidatorReport[]` array rendered by the table and consumed by Task 3's CSV export. Sort keys are the string literals `'moniker' | 'addr' | 'score' | 'tier' | 'critical' | 'warning' | 'downtime'`.

- [ ] **Step 1: Add a module-level tier-rank constant**

Near the top of `Reports.tsx`, after `TIER_BADGE_CLASS` (line 20), add:

```tsx
const TIER_RANK: Record<string, number> = {
  Excellent: 3,
  Good: 2,
  Watch: 1,
  Critical: 0,
}
```

- [ ] **Step 2: Add sort state**

Next to the `tierFilter` state added in Task 1, add:

```tsx
const [sortKey, setSortKey] = useState<string | null>(null)
const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc')
```

- [ ] **Step 3: Add the sort handler and indicator helper**

After the `filtered` computation (from Task 1), add:

```tsx
const handleSort = (key: string) => {
  if (sortKey !== key) {
    setSortKey(key)
    setSortDir('asc')
    return
  }
  if (sortDir === 'asc') {
    setSortDir('desc')
    return
  }
  setSortKey(null) // third click clears back to default (score desc)
}

const sortIndicator = (key: string) => (sortKey === key ? (sortDir === 'asc' ? ' ▲' : ' ▼') : '')
```

- [ ] **Step 4: Compute the `sorted` array**

Immediately after `handleSort` / `sortIndicator`, add:

```tsx
const sorted = [...filtered].sort((a, b) => {
  const pa = a.periods[period]
  const pb = b.periods[period]
  // Validators without data for the selected period always sort to the end.
  if (!pa && !pb) return 0
  if (!pa) return 1
  if (!pb) return -1

  const key = sortKey ?? 'score'
  const dir = sortKey === null ? 'desc' : sortDir
  let cmp = 0
  switch (key) {
    case 'moniker':
      cmp = a.moniker.localeCompare(b.moniker, undefined, { sensitivity: 'base' })
      break
    case 'addr':
      cmp = a.addr.localeCompare(b.addr)
      break
    case 'tier':
      cmp = (TIER_RANK[pa.tier] ?? -1) - (TIER_RANK[pb.tier] ?? -1)
      break
    case 'score':
      cmp = pa.score - pb.score
      break
    case 'critical':
      cmp = pa.critical_count - pb.critical_count
      break
    case 'warning':
      cmp = pa.warning_count - pb.warning_count
      break
    case 'downtime':
      cmp = pa.downtime_blocks - pb.downtime_blocks
      break
  }
  return dir === 'asc' ? cmp : -cmp
})
```

- [ ] **Step 5: Make the table headers clickable**

Replace the `<thead>` block (lines 119-129):

```tsx
<thead>
  <tr>
    <th>Moniker</th>
    <th>Address</th>
    <th>Score</th>
    <th>Tier</th>
    <th>Critical</th>
    <th>Warning</th>
    <th>Downtime Blocks</th>
  </tr>
</thead>
```

with:

```tsx
<thead>
  <tr>
    <th style={{ cursor: 'pointer' }} onClick={() => handleSort('moniker')}>Moniker{sortIndicator('moniker')}</th>
    <th style={{ cursor: 'pointer' }} onClick={() => handleSort('addr')}>Address{sortIndicator('addr')}</th>
    <th style={{ cursor: 'pointer' }} onClick={() => handleSort('score')}>Score{sortIndicator('score')}</th>
    <th style={{ cursor: 'pointer' }} onClick={() => handleSort('tier')}>Tier{sortIndicator('tier')}</th>
    <th style={{ cursor: 'pointer' }} onClick={() => handleSort('critical')}>Critical{sortIndicator('critical')}</th>
    <th style={{ cursor: 'pointer' }} onClick={() => handleSort('warning')}>Warning{sortIndicator('warning')}</th>
    <th style={{ cursor: 'pointer' }} onClick={() => handleSort('downtime')}>Downtime Blocks{sortIndicator('downtime')}</th>
  </tr>
</thead>
```

- [ ] **Step 6: Render `sorted` instead of `filtered`**

In the `<tbody>`, change the row source (line 133) from `filtered.map(r => {` to `sorted.map(r => {`. Leave the `filtered.length === 0` empty-state check (line 131) as-is — `sorted` and `filtered` always have the same length, and the subtitle already uses `filtered.length`.

- [ ] **Step 7: Verify the build**

Run from `panel/`:

```bash
npm run build
```

Expected: build succeeds with no TypeScript errors.

- [ ] **Step 8: Manual check**

In `npm run dev`: click each header — 1st click sorts ascending (▲), 2nd descending (▼), 3rd clears the indicator and returns to default score-descending order. Confirm Tier sorts by rank (Excellent → Critical when descending), not alphabetically. Confirm sorting respects the active text + tier filters. If any validator lacks data for the period (dash cells), it stays at the bottom in both directions.

---

## Task 3: CSV export on Reports (all four periods)

**Files:**
- Modify: `panel/src/pages/Reports.tsx`

**Interfaces:**
- Consumes: `sorted: ValidatorReport[]` from Task 2, `chain: string`, `REPORT_PERIODS` / `ReportPeriod` from `../types/report`, `PeriodScore` fields.
- Produces: a browser file download; no return value exposed to other tasks.

- [ ] **Step 1: Add a module-level CSV helper and period order**

Near the top of `Reports.tsx`, after `TIER_RANK` (from Task 2), add:

```tsx
const PERIOD_ORDER: ReportPeriod[] = ['last_24h', 'current_week', 'current_month', 'current_year']

function csvEscape(value: string): string {
  return /[",\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value
}
```

Note: `ReportPeriod` is already imported at the top of the file (line 6). `PERIOD_ORDER` intentionally fixes column order and matches `REPORT_PERIODS`.

- [ ] **Step 2: Add the export handler**

Inside the `Reports` component, after `handleToggle` (around line 72), add:

```tsx
const handleExportCsv = () => {
  const headers = ['moniker', 'address']
  for (const per of PERIOD_ORDER) {
    headers.push(`${per}_score`, `${per}_tier`, `${per}_critical`, `${per}_warning`, `${per}_downtime`)
  }
  const lines = sorted.map(r => {
    const cells = [r.moniker, r.addr]
    for (const per of PERIOD_ORDER) {
      const p = r.periods[per]
      if (p) {
        cells.push(String(p.score), p.tier, String(p.critical_count), String(p.warning_count), String(p.downtime_blocks))
      } else {
        cells.push('', '', '', '', '')
      }
    }
    return cells.map(csvEscape).join(',')
  })
  const csv = [headers.join(','), ...lines].join('\n')
  const blob = new Blob([csv], { type: 'text/csv;charset=utf-8;' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `validator-report-${chain}-${new Date().toISOString().slice(0, 10)}.csv`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
```

- [ ] **Step 3: Add the Export CSV button to the page header**

Replace the header's right-side conditional (lines 87-91):

```tsx
{chain && (
  <button className={`btn ${reportEnabled ? 'btn-primary' : 'btn-secondary'}`} onClick={handleToggle} disabled={toggling}>
    {toggling ? <span className="spinner" /> : null} Reports {reportEnabled ? 'Enabled' : 'Disabled'} for {chain}
  </button>
)}
```

with:

```tsx
{chain && (
  <div className="flex-gap" style={{ gap: 12 }}>
    <button className="btn btn-secondary" onClick={handleExportCsv} disabled={loading || sorted.length === 0}>
      Export CSV
    </button>
    <button className={`btn ${reportEnabled ? 'btn-primary' : 'btn-secondary'}`} onClick={handleToggle} disabled={toggling}>
      {toggling ? <span className="spinner" /> : null} Reports {reportEnabled ? 'Enabled' : 'Disabled'} for {chain}
    </button>
  </div>
)}
```

- [ ] **Step 4: Verify the build**

Run from `panel/`:

```bash
npm run build
```

Expected: build succeeds with no TypeScript errors.

- [ ] **Step 5: Manual check**

In `npm run dev`: click "Export CSV". Confirm a file `validator-report-<chain>-<today>.csv` downloads. Open it: header row has 22 columns (`moniker`, `address`, then 5 per period × 4 periods); rows match the currently filtered + sorted table; a validator with a comma or quote in its moniker is correctly quoted/escaped; validators missing a period show empty cells there. Apply a tier filter, re-export, and confirm only visible rows are included. Confirm the button is disabled while loading or when zero rows are shown.

---

## Task 4: Validator selection on Alert History

**Files:**
- Modify: `panel/src/pages/AlertHistory.tsx`

**Interfaces:**
- Consumes: existing `alerts: AlertLog[]` state, `AlertLog.addr` / `AlertLog.moniker`, `truncateAddr` (already imported).
- Produces: a filtered `visibleAlerts: AlertLog[]` rendered by the table and reflected in the count. Self-contained; no other task depends on it.

- [ ] **Step 1: Import `useMemo`**

Change the React import at the top of `AlertHistory.tsx` (line 1) from:

```tsx
import { useState, useEffect } from 'react'
```

to:

```tsx
import { useState, useEffect, useMemo } from 'react'
```

- [ ] **Step 2: Add validator-filter state**

Next to the existing filter state (after `const [limit, setLimit] = useState(100)`, line 14), add:

```tsx
const [validatorFilter, setValidatorFilter] = useState('')
```

- [ ] **Step 3: Derive distinct validator options and the reset effect**

After the existing `useEffect(() => { fetchAlerts() }, [chain, level, limit])` (line 38), add:

```tsx
const validatorOptions = useMemo(() => {
  const seen = new Map<string, string>() // addr -> moniker
  for (const a of alerts) {
    if (!seen.has(a.addr)) seen.set(a.addr, a.moniker)
  }
  return Array.from(seen.entries())
    .map(([addr, moniker]) => ({
      addr,
      label: addr === 'all' ? 'System' : moniker || truncateAddr(addr),
    }))
    .sort((x, y) => x.label.localeCompare(y.label, undefined, { sensitivity: 'base' }))
}, [alerts])

// Reset the validator filter if the selected validator is absent from the new result set.
useEffect(() => {
  if (validatorFilter && !alerts.some(a => a.addr === validatorFilter)) {
    setValidatorFilter('')
  }
}, [alerts, validatorFilter])

const visibleAlerts = validatorFilter ? alerts.filter(a => a.addr === validatorFilter) : alerts
```

- [ ] **Step 4: Add the validator `<select>` to the filter bar**

In the filter-bar `flex-gap` div, after the limit `<select>` (ending at line 88), insert:

```tsx
<select className="form-input" value={validatorFilter} onChange={e => setValidatorFilter(e.target.value)} style={{ width: 200 }}>
  <option value="">All Validators</option>
  {validatorOptions.map(v => <option key={v.addr} value={v.addr}>{v.label}</option>)}
</select>
```

- [ ] **Step 5: Render `visibleAlerts` and update the count**

- Change the subtitle (line 60) from `{alerts.length} alerts shown` to `{visibleAlerts.length} alerts shown`.
- Change the empty-state check (line 108) from `alerts.length === 0 ? (` to `visibleAlerts.length === 0 ? (`.
- Change the row map (line 110) from `alerts.map(a => (` to `visibleAlerts.map(a => (`.

Leave the purge flow, `fetchAlerts`, and the chain/level/limit selects unchanged.

- [ ] **Step 6: Verify the build**

Run from `panel/`:

```bash
npm run build
```

Expected: build succeeds with no TypeScript errors.

- [ ] **Step 7: Manual check**

In `npm run dev`, open Alert History. Confirm: the "All Validators" select lists distinct validators from the loaded alerts (a `System` entry appears when system alerts are present); selecting one narrows the table to that validator; the "N alerts shown" count updates; changing chain/level/limit refetches, and if the selected validator no longer appears the filter resets to "All Validators" (table not silently empty).

---

## Self-Review

**Spec coverage:**
- Feature 1 (sortable columns) → Task 2. ✔ (asc→desc→default, tier by rank, no-period rows last, post-filter)
- Feature 2 (tier filter, selected period, AND with text) → Task 1. ✔
- Feature 3 (CSV export, 4 periods, 22 cols, filtered+sorted set, escaping, filename) → Task 3. ✔
- Feature 4 (validator select, distinct from loaded alerts, `all`→System, front filter, auto-reset) → Task 4. ✔
- "No backend changes / no new deps / English / build+manual gate" → Global Constraints. ✔

**Placeholder scan:** No TBD/TODO; every code step shows full code. ✔

**Type consistency:** `filtered` (Task 1) → `sorted` (Task 2) → CSV `sorted` (Task 3) names match. Sort keys `'moniker'|'addr'|'score'|'tier'|'critical'|'warning'|'downtime'` are consistent between `handleSort`, `sortIndicator`, the `switch`, and the header `onClick`s. `PERIOD_ORDER` uses `ReportPeriod` (imported) and matches `REPORT_PERIODS`. `validatorOptions`/`visibleAlerts`/`validatorFilter` names consistent across Task 4 steps. ✔
