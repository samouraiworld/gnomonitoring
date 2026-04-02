import { useState, useEffect, useCallback } from 'react'
import { api } from '../lib/api'
import { useAutoRefresh } from '../hooks/useAutoRefresh'
import { timeAgo, levelBadgeClass, truncateAddr } from '../lib/format'
import type { StatusResponse, AlertLog } from '../types/api'

export default function Dashboard() {
  const [status, setStatus] = useState<StatusResponse | null>(null)
  const [alerts, setAlerts] = useState<AlertLog[]>([])
  const [loading, setLoading] = useState(true)

  const fetchData = useCallback(async () => {
    try {
      const [s, a] = await Promise.all([
        api.get<StatusResponse>('/admin/status'),
        api.get<AlertLog[]>('/admin/alerts?limit=10'),
      ])
      setStatus(s)
      setAlerts(a)
    } catch (err) {
      console.error('[Dashboard] fetch error:', err)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { fetchData() }, [fetchData])
  useAutoRefresh(fetchData, 10_000)

  if (loading) {
    return (
      <div className="page-content">
        <div className="page-header"><h1 className="page-title">Dashboard</h1></div>
        <div className="card-grid">
          {[1, 2, 3].map(i => (
            <div key={i} className="card"><div className="skeleton" style={{ height: 80 }} /></div>
          ))}
        </div>
      </div>
    )
  }

  const totalAlerts = status?.chains.reduce((sum, c) => sum + c.active_alerts + c.critical_alerts, 0) ?? 0

  return (
    <>
      <div className="page-header">
        <h1 className="page-title">Dashboard</h1>
        <p className="page-subtitle">Live monitoring overview — auto-refreshes every 10s</p>
      </div>

      {/* Global Stats */}
      <div className="card-grid" style={{ marginBottom: 24 }}>
        <div className="card">
          <div className="stat-label">Total Users</div>
          <div className="stat-value">{status?.total_users ?? 0}</div>
        </div>
        <div className="card">
          <div className="stat-label">Total Webhooks</div>
          <div className="stat-value">{status?.total_webhooks ?? 0}</div>
        </div>
        <div className="card">
          <div className="stat-label">Telegram Chats</div>
          <div className="stat-value">{status?.total_telegram_chats ?? 0}</div>
        </div>
        <div className="card">
          <div className="stat-label">Active Alerts</div>
          <div className="stat-value" style={{ color: totalAlerts > 0 ? 'var(--status-critical)' : 'var(--status-ok)' }}>
            {totalAlerts}
          </div>
        </div>
      </div>

      {/* Per-Chain Status */}
      <div className="card-header" style={{ marginBottom: 12 }}>
        <h2 className="card-title">Chain Status</h2>
      </div>
      <div className="card-grid" style={{ marginBottom: 24 }}>
        {status?.chains.map(chain => (
          <div key={chain.chain_id} className="card" style={{ opacity: chain.goroutine_active ? 1 : 0.5 }}>
            <div className="flex-between" style={{ marginBottom: 12 }}>
              <div className="flex-gap">
                <span className={`dot ${chain.goroutine_active ? 'dot-ok' : 'dot-muted'}`} />
                <span style={{ fontWeight: 600, fontFamily: 'var(--font-mono)', fontSize: 13 }}>
                  {chain.chain_id}
                </span>
              </div>
              <span className={`badge ${chain.goroutine_active ? 'badge-ok' : 'badge-muted'}`}>
                {chain.goroutine_active ? 'Active' : 'Inactive'}
              </span>
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 8 }}>
              <div>
                <div className="stat-label">Height</div>
                <div style={{ fontFamily: 'var(--font-mono)', fontWeight: 600, fontSize: 15 }}>
                  {chain.height.toLocaleString()}
                </div>
              </div>
              <div>
                <div className="stat-label">⚠ Warnings</div>
                <div style={{ fontFamily: 'var(--font-mono)', fontWeight: 600, fontSize: 15, color: chain.active_alerts > 0 ? 'var(--status-warn)' : 'var(--text-secondary)' }}>
                  {chain.active_alerts}
                </div>
              </div>
              <div>
                <div className="stat-label">🔴 Critical</div>
                <div style={{ fontFamily: 'var(--font-mono)', fontWeight: 600, fontSize: 15, color: chain.critical_alerts > 0 ? 'var(--status-critical)' : 'var(--text-secondary)' }}>
                  {chain.critical_alerts}
                </div>
              </div>
            </div>
          </div>
        ))}
      </div>

      {/* Recent Alerts */}
      <div className="card" style={{ padding: 0 }}>
        <div className="card-header" style={{ padding: '16px 20px' }}>
          <h2 className="card-title">Recent Alerts</h2>
        </div>
        {alerts.length === 0 ? (
          <div className="empty-state">
            <div className="empty-state-icon">✓</div>
            <div className="empty-state-title">No recent alerts</div>
            <div className="empty-state-text">All systems operational</div>
          </div>
        ) : (
          <div className="table-container" style={{ border: 'none', borderRadius: 0 }}>
            <table>
              <thead>
                <tr>
                  <th>Level</th>
                  <th>Chain</th>
                  <th>Validator</th>
                  <th>Height Range</th>
                  <th>Time</th>
                </tr>
              </thead>
              <tbody>
                {alerts.map(a => (
                  <tr key={a.ID}>
                    <td><span className={`badge ${levelBadgeClass(a.level)}`}>{a.level}</span></td>
                    <td className="mono">{a.chain_id}</td>
                    <td>
                      <div>{a.moniker === 'all' ? '—' : a.moniker}</div>
                      {a.addr !== 'all' && (
                        <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}>
                          {truncateAddr(a.addr)}
                        </div>
                      )}
                    </td>
                    <td className="mono">{a.start_height.toLocaleString()} → {a.end_height.toLocaleString()}</td>
                    <td style={{ color: 'var(--text-muted)', fontSize: 12 }}>{timeAgo(a.sent_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  )
}
