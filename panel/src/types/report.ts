/* ═══════════════════════════════════════════════════════════════════
   TypeScript interfaces — exact match to Go backend structs
   Source: internal/api/api_report.go, backend validator report endpoint
   ═══════════════════════════════════════════════════════════════════ */

// ── Period score ────────────────────────────────────────────────────
export interface PeriodScore {
  score: number
  tier: 'Excellent' | 'Good' | 'Watch' | 'Critical'
  sign_rate: number
  proposer_reliability: number | null
  voting_power: number
  critical_count: number
  warning_count: number
  incident_count: number
  incident_rate_per_week: number
  downtime_blocks: number
  missed_blocks: number
}

// ── Report period constants ─────────────────────────────────────────
export const REPORT_PERIODS = ['last_24h', 'current_week', 'current_month', 'current_year'] as const
export type ReportPeriod = (typeof REPORT_PERIODS)[number]

// ── Validator report ────────────────────────────────────────────────
export interface ValidatorReport {
  addr: string
  moniker: string
  // Global (period-independent): full days since the last WARNING/CRITICAL
  // alert, null when the validator never alerted.
  days_since_last_alert: number | null
  periods: Record<ReportPeriod, PeriodScore>
}
