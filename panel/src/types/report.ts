/* ═══════════════════════════════════════════════════════════════════
   TypeScript interfaces — exact match to Go backend structs
   Source: internal/api/api_report.go, backend validator report endpoint
   ═══════════════════════════════════════════════════════════════════ */

// ── Period score ────────────────────────────────────────────────────
export interface PeriodScore {
  score: number
  tier: 'Excellent' | 'Good' | 'Watch' | 'Critical'
  critical_count: number
  warning_count: number
  downtime_blocks: number
}

// ── Report period constants ─────────────────────────────────────────
export const REPORT_PERIODS = ['last_24h', 'current_week', 'current_month', 'current_year'] as const
export type ReportPeriod = (typeof REPORT_PERIODS)[number]

// ── Validator report ────────────────────────────────────────────────
export interface ValidatorReport {
  addr: string
  moniker: string
  periods: Record<ReportPeriod, PeriodScore>
}
