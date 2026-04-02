import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { formatThresholdLabel, getThresholdUnit } from '../lib/format'
import type { ApiStatus } from '../types/api'

export default function AlertConfig() {
  const [thresholds, setThresholds] = useState<Record<string, string>>({})
  const [original, setOriginal] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const toast = useToast()

  useEffect(() => {
    api.get<Record<string, string>>('/admin/config/thresholds')
      .then(data => { setThresholds(data); setOriginal(data) })
      .catch(() => toast.error('Failed to load thresholds'))
      .finally(() => setLoading(false))
  }, [])

  const changed = Object.keys(thresholds).some(k => thresholds[k] !== original[k])

  const handleSave = async () => {
    const diff: Record<string, string> = {}
    for (const k of Object.keys(thresholds)) {
      if (thresholds[k] !== original[k]) diff[k] = thresholds[k]
    }
    if (Object.keys(diff).length === 0) return
    setSaving(true)
    try {
      await api.put<ApiStatus>('/admin/config/thresholds', diff)
      setOriginal({ ...thresholds })
      toast.success('Thresholds updated')
    } catch (err: any) {
      toast.error(err.message)
    } finally {
      setSaving(false)
    }
  }

  const handleReset = () => setThresholds({ ...original })

  // Group thresholds logically
  const groups = [
    { title: 'Alert Thresholds', keys: ['warning_threshold', 'critical_threshold'] },
    { title: 'Mute Configuration', keys: ['mute_after_n_alerts', 'mute_duration_minutes', 'resolve_mute_after_n'] },
    { title: 'Stagnation Detection', keys: ['stagnation_first_alert_seconds', 'stagnation_repeat_minutes'] },
    { title: 'Monitoring Intervals', keys: ['rpc_error_cooldown_minutes', 'new_validator_scan_minutes', 'alert_check_interval_seconds'] },
    { title: 'Data Retention', keys: ['raw_retention_days', 'aggregator_period_minutes'] },
  ]

  if (loading) {
    return (
      <>
        <div className="page-header"><h1 className="page-title">Alert Configuration</h1></div>
        <div className="card"><div className="skeleton" style={{ height: 300 }} /></div>
      </>
    )
  }

  return (
    <>
      <div className="page-header flex-between">
        <div>
          <h1 className="page-title">Alert Configuration</h1>
          <p className="page-subtitle">Adjust monitoring thresholds and intervals</p>
        </div>
        <div className="flex-gap">
          {changed && <button className="btn btn-secondary" onClick={handleReset}>Reset</button>}
          <button className="btn btn-primary" onClick={handleSave} disabled={!changed || saving}>
            {saving ? <span className="spinner" /> : null} Save Changes
          </button>
        </div>
      </div>

      {groups.map(group => (
        <div key={group.title} className="card" style={{ marginBottom: 16 }}>
          <div className="card-title" style={{ marginBottom: 16 }}>{group.title}</div>
          <div className="form-row">
            {group.keys.filter(k => k in thresholds).map(key => (
              <div key={key} className="form-group">
                <label className="form-label">
                  {formatThresholdLabel(key)}
                  {getThresholdUnit(key) && (
                    <span style={{ color: 'var(--text-muted)', marginLeft: 4 }}>({getThresholdUnit(key)})</span>
                  )}
                </label>
                <input
                  className="form-input"
                  type="number"
                  min={0}
                  value={thresholds[key] || ''}
                  onChange={e => setThresholds(prev => ({ ...prev, [key]: e.target.value }))}
                  style={{
                    borderColor: thresholds[key] !== original[key] ? 'var(--accent-primary)' : undefined,
                  }}
                />
              </div>
            ))}
          </div>
        </div>
      ))}
    </>
  )
}
