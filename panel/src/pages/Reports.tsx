import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { truncateAddr } from '../lib/format'
import type { ChainInfo, ApiStatus } from '../types/api'
import { REPORT_PERIODS, type ReportPeriod, type ValidatorReport } from '../types/report'

const PERIOD_LABELS: Record<ReportPeriod, string> = {
  last_24h: 'Last 24h',
  current_week: 'Current Week',
  current_month: 'Current Month',
  current_year: 'Current Year',
}

const TIER_BADGE_CLASS: Record<string, string> = {
  Excellent: 'badge-ok',
  Good: 'badge-info',
  Watch: 'badge-warn',
  Critical: 'badge-critical',
}

export default function Reports() {
  const [chains, setChains] = useState<ChainInfo[]>([])
  const [chain, setChain] = useState('')
  const [period, setPeriod] = useState<ReportPeriod>('current_month')
  const [reports, setReports] = useState<ValidatorReport[]>([])
  const [filter, setFilter] = useState('')
  const [tierFilter, setTierFilter] = useState('')
  const [loading, setLoading] = useState(true)
  const [thresholds, setThresholds] = useState<Record<string, string>>({})
  const [toggling, setToggling] = useState(false)
  const toast = useToast()

  useEffect(() => {
    api.get<ChainInfo[]>('/admin/chains').then(data => {
      setChains(data)
      if (data.length > 0) setChain(prev => prev || data[0].id)
    }).catch(() => {})
  }, [])

  const fetchThresholds = () => {
    api.get<Record<string, string>>('/admin/config/thresholds')
      .then(setThresholds)
      .catch(() => {})
  }

  useEffect(() => { fetchThresholds() }, [])

  useEffect(() => {
    if (!chain) return
    setLoading(true)
    api.get<ValidatorReport[]>(`/api/reports/validators?chain=${chain}`)
      .then(setReports)
      .catch((err: any) => toast.error(err.message))
      .finally(() => setLoading(false))
  }, [chain])

  const reportEnabled = thresholds[`validator_report_enabled.${chain}`] === 'true'

  const handleToggle = async () => {
    if (!chain) return
    const next = !reportEnabled
    setToggling(true)
    try {
      await api.put<ApiStatus>('/admin/config/thresholds', { [`validator_report_enabled.${chain}`]: next ? 'true' : 'false' })
      toast.success(`Reports ${next ? 'enabled' : 'disabled'} for ${chain}`)
      fetchThresholds()
    } catch (err: any) {
      toast.error(err.message)
    } finally {
      setToggling(false)
    }
  }

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

  return (
    <>
      <div className="page-header flex-between">
        <div>
          <h1 className="page-title">Reports</h1>
          <p className="page-subtitle">{filtered.length} validators shown</p>
        </div>
        {chain && (
          <button className={`btn ${reportEnabled ? 'btn-primary' : 'btn-secondary'}`} onClick={handleToggle} disabled={toggling}>
            {toggling ? <span className="spinner" /> : null} Reports {reportEnabled ? 'Enabled' : 'Disabled'} for {chain}
          </button>
        )}
      </div>

      {/* Filters */}
      <div className="card" style={{ marginBottom: 20, padding: 16 }}>
        <div className="flex-gap" style={{ flexWrap: 'wrap', gap: 12 }}>
          <select className="form-input" value={chain} onChange={e => setChain(e.target.value)} style={{ width: 160 }}>
            {chains.map(c => <option key={c.id} value={c.id}>{c.id}</option>)}
          </select>
          <select className="form-input" value={period} onChange={e => setPeriod(e.target.value as ReportPeriod)} style={{ width: 160 }}>
            {REPORT_PERIODS.map(p => <option key={p} value={p}>{PERIOD_LABELS[p]}</option>)}
          </select>
          <select className="form-input" value={tierFilter} onChange={e => setTierFilter(e.target.value)} style={{ width: 150 }}>
            <option value="">All Tiers</option>
            <option value="Excellent">Excellent</option>
            <option value="Good">Good</option>
            <option value="Watch">Watch</option>
            <option value="Critical">Critical</option>
          </select>
          <input
            className="form-input"
            type="text"
            placeholder="Filter by address or moniker..."
            value={filter}
            onChange={e => setFilter(e.target.value)}
            style={{ minWidth: 220 }}
          />
        </div>
      </div>

      {loading ? (
        <div className="card"><div className="skeleton" style={{ height: 300 }} /></div>
      ) : (
        <div className="table-container">
          <table>
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
            <tbody>
              {filtered.length === 0 ? (
                <tr><td colSpan={7}><div className="empty-state"><div className="empty-state-title">No data</div></div></td></tr>
              ) : filtered.map(r => {
                const p = r.periods[period]
                return (
                  <tr key={r.addr}>
                    <td>{r.moniker || '—'}</td>
                    <td className="mono">{truncateAddr(r.addr)}</td>
                    <td>{p ? p.score : '—'}</td>
                    <td>{p ? <span className={`badge ${TIER_BADGE_CLASS[p.tier] || 'badge-muted'}`}>{p.tier}</span> : '—'}</td>
                    <td>{p ? p.critical_count : '—'}</td>
                    <td>{p ? p.warning_count : '—'}</td>
                    <td>{p ? p.downtime_blocks : '—'}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}
