import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { copyToClipboard } from '../lib/format'
import type { GovdaoProposal, ChainInfo } from '../types/api'

export default function GovDAO() {
  const [proposals, setProposals] = useState<GovdaoProposal[]>([])
  const [chains, setChains] = useState<ChainInfo[]>([])
  const [chain, setChain] = useState('')
  const [loading, setLoading] = useState(true)
  const toast = useToast()

  useEffect(() => { api.get<ChainInfo[]>('/admin/chains').then(setChains).catch(() => {}) }, [])

  useEffect(() => {
    setLoading(true)
    const params = chain ? `?chain=${chain}` : ''
    api.get<GovdaoProposal[]>(`/admin/govdao/proposals${params}`)
      .then(data => setProposals(data ?? []))
      .catch(() => toast.error('Failed to load proposals'))
      .finally(() => setLoading(false))
  }, [chain])

  const statusBadge = (status: string) => {
    switch (status.toLowerCase()) {
      case 'accepted': return 'badge-ok'
      case 'rejected': return 'badge-critical'
      case 'active': case 'pending': return 'badge-warn'
      default: return 'badge-muted'
    }
  }

  return (
    <>
      <div className="page-header flex-between">
        <div>
          <h1 className="page-title">GovDAO Proposals</h1>
          <p className="page-subtitle">{proposals.length} proposals</p>
        </div>
        <select className="form-input" value={chain} onChange={e => setChain(e.target.value)} style={{ width: 160 }}>
          <option value="">Default Chain</option>
          {chains.map(c => <option key={c.id} value={c.id}>{c.id}</option>)}
        </select>
      </div>

      {loading ? <div className="card"><div className="skeleton" style={{ height: 300 }} /></div> : (
        <div className="table-container">
          <table>
            <thead><tr><th>ID</th><th>Title</th><th>Status</th><th>TX</th><th>Link</th></tr></thead>
            <tbody>
              {proposals.length === 0 ? (
                <tr><td colSpan={5}><div className="empty-state"><div className="empty-state-icon">🏛</div><div className="empty-state-title">No proposals</div></div></td></tr>
              ) : proposals.map(p => (
                <tr key={p.Id}>
                  <td className="mono">{p.Id}</td>
                  <td>{p.Title}</td>
                  <td><span className={`badge ${statusBadge(p.Status)}`}>{p.Status}</span></td>
                  <td>
                    {p.Tx ? (
                      <span className="mono" style={{ fontSize: 11 }}>
                        {p.Tx.slice(0, 10)}...
                        <button className="copy-btn" onClick={() => { copyToClipboard(p.Tx); toast.success('TX copied') }}>📋</button>
                      </span>
                    ) : '—'}
                  </td>
                  <td>
                    {p.Url ? <a href={p.Url} target="_blank" rel="noopener noreferrer">View ↗</a> : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}
