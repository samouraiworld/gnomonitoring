import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { ConfirmModal } from '../components/ConfirmModal'
import { truncateAddr, copyToClipboard } from '../lib/format'
import type { AddrMoniker, ChainInfo, ApiStatus } from '../types/api'

export default function Monikers() {
  const [monikers, setMonikers] = useState<AddrMoniker[]>([])
  const [chains, setChains] = useState<ChainInfo[]>([])
  const [chain, setChain] = useState('')
  const [loading, setLoading] = useState(true)
  const [showAdd, setShowAdd] = useState(false)
  const [editKey, setEditKey] = useState<string | null>(null)
  const [editValue, setEditValue] = useState('')
  const [delTarget, setDelTarget] = useState<{ chain: string; addr: string } | null>(null)
  const [form, setForm] = useState({ chain_id: '', addr: '', moniker: '' })
  const toast = useToast()

  useEffect(() => { api.get<ChainInfo[]>('/admin/chains').then(setChains).catch(() => {}) }, [])

  const fetchMonikers = async () => {
    try {
      const params = chain ? `?chain=${chain}` : ''
      setMonikers(await api.get<AddrMoniker[]>(`/admin/monikers${params}`))
    } catch { toast.error('Failed to load monikers') }
    finally { setLoading(false) }
  }
  useEffect(() => { fetchMonikers() }, [chain])

  const handleAdd = async () => {
    try {
      await api.post<ApiStatus>('/admin/monikers', form)
      toast.success('Moniker added')
      setShowAdd(false)
      setForm({ chain_id: '', addr: '', moniker: '' })
      fetchMonikers()
    } catch (err: any) { toast.error(err.message) }
  }

  const handleEdit = async (chainId: string, addr: string) => {
    try {
      await api.put<ApiStatus>(`/admin/monikers/${chainId}/${addr}`, { moniker: editValue })
      toast.success('Moniker updated')
      setEditKey(null)
      fetchMonikers()
    } catch (err: any) { toast.error(err.message) }
  }

  const handleDelete = async () => {
    if (!delTarget) return
    try {
      await api.del<ApiStatus>(`/admin/monikers/${delTarget.chain}/${delTarget.addr}`)
      toast.success('Moniker deleted')
      setDelTarget(null)
      fetchMonikers()
    } catch (err: any) { toast.error(err.message) }
  }

  return (
    <>
      <div className="page-header flex-between">
        <div>
          <h1 className="page-title">Moniker Management</h1>
          <p className="page-subtitle">{monikers.length} validator monikers</p>
        </div>
        <div className="flex-gap">
          <select className="form-input" value={chain} onChange={e => setChain(e.target.value)} style={{ width: 160 }}>
            <option value="">All Chains</option>
            {chains.map(c => <option key={c.id} value={c.id}>{c.id}</option>)}
          </select>
          <button className="btn btn-primary" onClick={() => setShowAdd(true)}>+ Add</button>
        </div>
      </div>

      {loading ? <div className="card"><div className="skeleton" style={{ height: 300 }} /></div> : (
        <div className="table-container">
          <table>
            <thead><tr><th>Chain</th><th>Address</th><th>Moniker</th><th>First Active</th><th>Actions</th></tr></thead>
            <tbody>
              {monikers.length === 0 ? (
                <tr><td colSpan={5}><div className="empty-state"><div className="empty-state-title">No monikers</div></div></td></tr>
              ) : monikers.map(m => {
                const key = `${m.chain_id}/${m.addr}`
                const isEditing = editKey === key
                return (
                  <tr key={key}>
                    <td className="mono">{m.chain_id}</td>
                    <td>
                      <span className="mono">{truncateAddr(m.addr, 8)}</span>
                      <button className="copy-btn" onClick={() => { copyToClipboard(m.addr); toast.success('Copied') }}>📋</button>
                    </td>
                    <td>
                      {isEditing ? (
                        <div className="flex-gap">
                          <input className="form-input" value={editValue} onChange={e => setEditValue(e.target.value)} style={{ width: 160 }} autoFocus onKeyDown={e => e.key === 'Enter' && handleEdit(m.chain_id, m.addr)} />
                          <button className="btn btn-primary btn-sm" onClick={() => handleEdit(m.chain_id, m.addr)}>Save</button>
                          <button className="btn btn-ghost btn-sm" onClick={() => setEditKey(null)}>✕</button>
                        </div>
                      ) : (
                        <span style={{ cursor: 'pointer' }} onClick={() => { setEditKey(key); setEditValue(m.moniker) }}>{m.moniker}</span>
                      )}
                    </td>
                    <td className="mono">{m.first_active_block >= 0 ? m.first_active_block.toLocaleString() : '—'}</td>
                    <td>
                      <button className="btn btn-ghost btn-sm" onClick={() => { setEditKey(key); setEditValue(m.moniker) }}>Edit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => setDelTarget({ chain: m.chain_id, addr: m.addr })} style={{ color: 'var(--status-critical)' }}>Delete</button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {showAdd && (
        <div className="modal-overlay" onClick={() => setShowAdd(false)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-title">Add Moniker</div>
            <div className="modal-body" style={{ display: 'grid', gap: 12 }}>
              <div className="form-group">
                <label className="form-label">Chain ID</label>
                <select className="form-input" value={form.chain_id} onChange={e => setForm(f => ({ ...f, chain_id: e.target.value }))}>
                  <option value="">Select chain</option>
                  {chains.map(c => <option key={c.id} value={c.id}>{c.id}</option>)}
                </select>
              </div>
              <div className="form-group">
                <label className="form-label">Address</label>
                <input className="form-input" value={form.addr} onChange={e => setForm(f => ({ ...f, addr: e.target.value }))} placeholder="g1..." />
              </div>
              <div className="form-group">
                <label className="form-label">Moniker</label>
                <input className="form-input" value={form.moniker} onChange={e => setForm(f => ({ ...f, moniker: e.target.value }))} />
              </div>
            </div>
            <div className="modal-actions">
              <button className="btn btn-secondary" onClick={() => setShowAdd(false)}>Cancel</button>
              <button className="btn btn-primary" onClick={handleAdd} disabled={!form.chain_id || !form.addr || !form.moniker}>Create</button>
            </div>
          </div>
        </div>
      )}

      {delTarget && (
        <ConfirmModal title="Delete Moniker?" message={`Remove moniker for ${truncateAddr(delTarget.addr)} on ${delTarget.chain}?`} confirmLabel="Delete" variant="danger" onConfirm={handleDelete} onCancel={() => setDelTarget(null)} />
      )}
    </>
  )
}
