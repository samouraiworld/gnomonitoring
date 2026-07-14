import { useState } from 'react'

/**
 * Mirror of score.DefaultWeights() in backend/internal/score/score.go.
 * The scoring weights are NOT seeded in admin_config; when a key is absent the
 * backend falls back to these defaults, so the panel shows the same values.
 * Keep in sync with the Go defaults.
 */
const DEFAULT_WEIGHTS: Record<string, number> = {
  report_score_critical_weight: 6,
  report_score_critical_cap: 60,
  report_score_warning_weight: 2,
  report_score_warning_cap: 20,
  report_score_freq_weight: 0.43,
  report_score_freq_cap: 30,
  report_score_downtime_blocks_per_point: 500,
  report_score_downtime_cap: 20,
  report_score_proposer_min_expected: 5,
  report_score_sign_weight: 0.8,
  report_score_proposer_weight: 0.2,
  report_score_vp_severity_factor: 0.5,
}

/** Effective weight = configured value (admin_config) or the code default. */
function weight(thresholds: Record<string, string>, key: string): number {
  const raw = thresholds[key]
  const n = raw != null ? Number(raw) : NaN
  return Number.isFinite(n) ? n : DEFAULT_WEIGHTS[key]
}

export default function ScoreLegend({ thresholds }: { thresholds: Record<string, string> }) {
  const [open, setOpen] = useState(false)

  const criticalWeight = weight(thresholds, 'report_score_critical_weight')
  const criticalCap = weight(thresholds, 'report_score_critical_cap')
  const warningWeight = weight(thresholds, 'report_score_warning_weight')
  const warningCap = weight(thresholds, 'report_score_warning_cap')
  const freqWeight = weight(thresholds, 'report_score_freq_weight')
  const freqCap = weight(thresholds, 'report_score_freq_cap')
  const downtimePerPoint = weight(thresholds, 'report_score_downtime_blocks_per_point')
  const downtimeCap = weight(thresholds, 'report_score_downtime_cap')
  const minExpected = weight(thresholds, 'report_score_proposer_min_expected')
  const signW = weight(thresholds, 'report_score_sign_weight')
  const propW = weight(thresholds, 'report_score_proposer_weight')
  const vpSeverity = weight(thresholds, 'report_score_vp_severity_factor')

  return (
    <div className="score-legend">
      <button
        className="score-legend-toggle"
        onClick={() => setOpen(o => !o)}
        aria-expanded={open}
      >
        <span className="score-legend-caret">{open ? '▾' : '▸'}</span>
        How is the score calculated?
      </button>

      {open && (
        <div className="score-legend-body">
          <p>The score rewards availability and penalizes incidents, weighted by stake.</p>
          <code className="score-legend-formula">Score = clamp(presence − penalties, 0, 100)</code>

          <p><strong>Presence</strong> blends signing and proposing:</p>
          <code className="score-legend-formula">{`presence = (${signW} × Sign% + ${propW} × Proposer%) / ${(signW + propW).toFixed(1)}
Proposer% is dropped when fewer than ${minExpected} proposals are expected → presence = Sign%`}</code>

          <p><strong>Penalties</strong> grow with incidents and stake:</p>
          <code className="score-legend-formula">{`penalties = (critical + warning + freq + downtime) × severity
  • −${criticalWeight} per CRITICAL alert (max ${criticalCap})
  • −${warningWeight} per WARNING alert (max ${warningCap})
  • −${freqWeight} per incident/week-equivalent (max ${freqCap})
  • −1 per ${downtimePerPoint} downtime blocks (max ${downtimeCap})
severity = 1 + ${vpSeverity} × (VP / max VP)`}</code>

          <div className="score-legend-tiers">
            <span>Tiers:</span>
            <span className="badge badge-ok">Excellent ≥ 85</span>
            <span className="badge badge-info">Good ≥ 60</span>
            <span className="badge badge-warn">Watch ≥ 30</span>
            <span className="badge badge-critical">Critical &lt; 30</span>
          </div>
        </div>
      )}
    </div>
  )
}
