import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { ConfirmModal } from '../components/ConfirmModal'
import { timeAgo, levelBadgeClass, truncateAddr } from '../lib/format'
import type { AlertLog, ChainInfo, ApiStatus } from '../types/api'

export default function AlertHistory() {
  const [alerts, setAlerts] = useState<AlertLog[]>([])
  const [chains, setChains] = useState<ChainInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [chain, setChain] = useState('')
  const [level, setLevel] = useState('')
  const [limit, setLimit] = useState(100)
  const [confirmPurge, setConfirmPurge] = useState(false)
  const [purging, setPurging] = useState(false)
  const toast = useToast()

  const fetchAlerts = async () => {
    try {
      const params = new URLSearchParams()
      if (chain) params.set('chain', chain)
      if (level) params.set('level', level)
      params.set('limit', String(limit))
      const data = await api.get<AlertLog[]>(`/admin/alerts?${params}`)
      setAlerts(data)
    } catch (err: any) {
      toast.error(err.message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    api.get<ChainInfo[]>('/admin/chains').then(setChains).catch(() => {})
  }, [])

  useEffect(() => { fetchAlerts() }, [chain, level, limit])

  const handlePurge = async () => {
    if (!chain) return
    setPurging(true)
    try {
      await api.del<ApiStatus>(`/admin/alerts?chain=${chain}`)
      toast.success(`Alerts purged for "${chain}"`)
      setConfirmPurge(false)
      fetchAlerts()
    } catch (err: any) {
      toast.error(err.message)
    } finally {
      setPurging(false)
    }
  }

  return (
    <>
      <div className="page-header flex-between">
        <div>
          <h1 className="page-title">Alert History</h1>
          <p className="page-subtitle">{alerts.length} alerts shown</p>
        </div>
        {chain && (
          <button className="btn btn-danger" onClick={() => setConfirmPurge(true)}>
            🗑 Purge {chain}
          </button>
        )}
      </div>

      {/* Filters */}
      <div className="card" style={{ marginBottom: 20, padding: 16 }}>
        <div className="flex-gap" style={{ flexWrap: 'wrap', gap: 12 }}>
          <select className="form-input" value={chain} onChange={e => setChain(e.target.value)} style={{ width: 160 }}>
            <option value="">All Chains</option>
            {chains.map(c => <option key={c.id} value={c.id}>{c.id}</option>)}
          </select>
          <select className="form-input" value={level} onChange={e => setLevel(e.target.value)} style={{ width: 140 }}>
            <option value="">All Levels</option>
            <option value="WARNING">Warning</option>
            <option value="CRITICAL">Critical</option>
            <option value="RESOLVED">Resolved</option>
            <option value="MUTED">Muted</option>
          </select>
          <select className="form-input" value={limit} onChange={e => setLimit(Number(e.target.value))} style={{ width: 120 }}>
            <option value={50}>50</option>
            <option value={100}>100</option>
            <option value={200}>200</option>
            <option value={500}>500</option>
          </select>
        </div>
      </div>

      {loading ? (
        <div className="card"><div className="skeleton" style={{ height: 300 }} /></div>
      ) : (
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>Level</th>
                <th>Chain</th>
                <th>Validator</th>
                <th>Height</th>
                <th>Message</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              {alerts.length === 0 ? (
                <tr><td colSpan={6}><div className="empty-state"><div className="empty-state-title">No alerts found</div></div></td></tr>
              ) : alerts.map(a => (
                <tr key={a.ID}>
                  <td><span className={`badge ${levelBadgeClass(a.level)}`}>{a.level}</span></td>
                  <td className="mono">{a.chain_id}</td>
                  <td>
                    <div>{a.moniker === 'all' ? 'System' : a.moniker}</div>
                    {a.addr !== 'all' && <div className="mono" style={{ fontSize: 11, color: 'var(--text-muted)' }}>{truncateAddr(a.addr)}</div>}
                  </td>
                  <td className="mono">{a.start_height.toLocaleString()} → {a.end_height.toLocaleString()}</td>
                  <td className="truncate" style={{ maxWidth: 220 }} title={a.msg}>{a.msg || '—'}</td>
                  <td style={{ fontSize: 12, color: 'var(--text-muted)', whiteSpace: 'nowrap' }}>{timeAgo(a.sent_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {confirmPurge && chain && (
        <ConfirmModal
          title={`Purge all alerts for "${chain}"?`}
          message="This will permanently delete all alert logs for this chain. This cannot be undone."
          confirmLabel="Purge Alerts"
          variant="danger"
          onConfirm={handlePurge}
          onCancel={() => setConfirmPurge(false)}
          loading={purging}
        />
      )}
    </>
  )
}
