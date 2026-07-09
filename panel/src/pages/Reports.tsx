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

const TIER_RANK: Record<string, number> = {
  Excellent: 3,
  Good: 2,
  Watch: 1,
  Critical: 0,
}

const PERIOD_ORDER: ReportPeriod[] = ['last_24h', 'current_week', 'current_month', 'current_year']

function csvEscape(value: string): string {
  return /[",\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value
}

export default function Reports() {
  const [chains, setChains] = useState<ChainInfo[]>([])
  const [chain, setChain] = useState('')
  const [period, setPeriod] = useState<ReportPeriod>('current_month')
  const [reports, setReports] = useState<ValidatorReport[]>([])
  const [filter, setFilter] = useState('')
  const [tierFilter, setTierFilter] = useState('')
  const [sortKey, setSortKey] = useState<string | null>(null)
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc')
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

  const handleExportCsv = () => {
    const headers = ['moniker', 'address']
    for (const per of PERIOD_ORDER) {
      headers.push(`${per}_score`, `${per}_tier`, `${per}_sign`, `${per}_vp`, `${per}_proposer`, `${per}_critical`, `${per}_warning`, `${per}_downtime`)
    }
    const lines = sorted.map(r => {
      const cells = [r.moniker, r.addr]
      for (const per of PERIOD_ORDER) {
        const p = r.periods[per]
        if (p) {
          cells.push(String(p.score), p.tier, p.sign_rate.toFixed(1), String(p.voting_power), p.proposer_reliability != null ? p.proposer_reliability.toFixed(1) : '', String(p.critical_count), String(p.warning_count), String(p.downtime_blocks))
        } else {
          cells.push('', '', '', '', '', '', '', '')
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
      case 'sign':
        cmp = pa.sign_rate - pb.sign_rate
        break
      case 'vp':
        cmp = pa.voting_power - pb.voting_power
        break
      case 'proposer':
        cmp = (pa.proposer_reliability ?? -1) - (pb.proposer_reliability ?? -1)
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

  return (
    <>
      <div className="page-header flex-between">
        <div>
          <h1 className="page-title">Reports</h1>
          <p className="page-subtitle">{filtered.length} validators shown</p>
        </div>
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
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('moniker')}>Moniker{sortIndicator('moniker')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('addr')}>Address{sortIndicator('addr')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('score')}>Score{sortIndicator('score')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('tier')}>Tier{sortIndicator('tier')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('sign')}>Sign %{sortIndicator('sign')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('vp')}>VP{sortIndicator('vp')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('proposer')}>Proposer %{sortIndicator('proposer')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('critical')}>Critical{sortIndicator('critical')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('warning')}>Warning{sortIndicator('warning')}</th>
                <th style={{ cursor: 'pointer' }} onClick={() => handleSort('downtime')}>Downtime Blocks{sortIndicator('downtime')}</th>
              </tr>
            </thead>
            <tbody>
              {filtered.length === 0 ? (
                <tr><td colSpan={10}><div className="empty-state"><div className="empty-state-title">No data</div></div></td></tr>
              ) : sorted.map(r => {
                const p = r.periods[period]
                return (
                  <tr key={r.addr}>
                    <td>{r.moniker || '—'}</td>
                    <td className="mono">{truncateAddr(r.addr)}</td>
                    <td>{p ? p.score : '—'}</td>
                    <td>{p ? <span className={`badge ${TIER_BADGE_CLASS[p.tier] || 'badge-muted'}`}>{p.tier}</span> : '—'}</td>
                    <td>{p ? `${p.sign_rate.toFixed(1)}%` : '—'}</td>
                    <td>{p ? p.voting_power.toLocaleString() : '—'}</td>
                    <td>{p && p.proposer_reliability != null ? `${p.proposer_reliability.toFixed(1)}%` : '—'}</td>
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
