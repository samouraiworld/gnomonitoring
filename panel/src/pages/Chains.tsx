import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { ConfirmModal } from '../components/ConfirmModal'
import type { ChainInfo, ChainCreatePayload, ApiStatus } from '../types/api'

export default function Chains() {
  const [chains, setChains] = useState<ChainInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [showAdd, setShowAdd] = useState(false)
  const [editChain, setEditChain] = useState<ChainInfo | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [confirmReinit, setConfirmReinit] = useState<string | null>(null)
  const [actionLoading, setActionLoading] = useState(false)
  const toast = useToast()

  const [form, setForm] = useState<ChainCreatePayload>({ id: '', rpc_endpoint: '', graphql: '', gnoweb: '', enabled: true })

  const fetchChains = async () => {
    try {
      const data = await api.get<ChainInfo[]>('/admin/chains')
      setChains(data)
    } catch (err) {
      toast.error('Failed to load chains')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { fetchChains() }, [])

  const handleAdd = async () => {
    setActionLoading(true)
    try {
      await api.post<ApiStatus>('/admin/chains', form)
      toast.success(`Chain "${form.id}" created`)
      setShowAdd(false)
      setForm({ id: '', rpc_endpoint: '', graphql: '', gnoweb: '', enabled: true })
      fetchChains()
    } catch (err: any) {
      toast.error(err.message || 'Failed to create chain')
    } finally {
      setActionLoading(false)
    }
  }

  const handleToggle = async (chainId: string, enabled: boolean) => {
    try {
      await api.put<ApiStatus>(`/admin/chains/${chainId}`, { enabled })
      toast.success(`${chainId} ${enabled ? 'enabled' : 'disabled'}`)
      fetchChains()
    } catch (err: any) {
      toast.error(err.message)
    }
  }

  const handleDelete = async () => {
    if (!confirmDelete) return
    setActionLoading(true)
    try {
      await api.del<ApiStatus>(`/admin/chains/${confirmDelete}`)
      toast.success(`Chain "${confirmDelete}" deleted`)
      setConfirmDelete(null)
      fetchChains()
    } catch (err: any) {
      toast.error(err.message)
    } finally {
      setActionLoading(false)
    }
  }

  const handleReinit = async () => {
    if (!confirmReinit) return
    setActionLoading(true)
    try {
      await api.post<ApiStatus>(`/admin/chains/${confirmReinit}/reinit`)
      toast.success(`Chain "${confirmReinit}" reinitialized`)
      setConfirmReinit(null)
      fetchChains()
    } catch (err: any) {
      toast.error(err.message)
    } finally {
      setActionLoading(false)
    }
  }

  const handleReload = async () => {
    try {
      await api.post<ApiStatus>('/admin/config/reload')
      toast.success('Config reloaded from disk')
      fetchChains()
    } catch (err: any) {
      toast.error(err.message)
    }
  }

  const handleUpdateEndpoints = async () => {
    if (!editChain) return
    setActionLoading(true)
    try {
      await api.put<ApiStatus>(`/admin/chains/${editChain.id}`, {
        rpc_endpoint: editChain.rpc_endpoint,
        graphql: editChain.graphql,
        gnoweb: editChain.gnoweb,
      })
      toast.success(`Chain "${editChain.id}" updated`)
      setEditChain(null)
      fetchChains()
    } catch (err: any) {
      toast.error(err.message)
    } finally {
      setActionLoading(false)
    }
  }

  return (
    <>
      <div className="page-header flex-between">
        <div>
          <h1 className="page-title">Chain Management</h1>
          <p className="page-subtitle">Configure monitored blockchain networks</p>
        </div>
        <div className="flex-gap">
          <button className="btn btn-secondary" onClick={handleReload}>↻ Reload Config</button>
          <button className="btn btn-primary" onClick={() => setShowAdd(true)}>+ Add Chain</button>
        </div>
      </div>

      {loading ? (
        <div className="card"><div className="skeleton" style={{ height: 200 }} /></div>
      ) : (
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>Status</th>
                <th>Chain ID</th>
                <th>RPC Endpoint</th>
                <th>Height</th>
                <th>Goroutine</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {chains.map(c => (
                <tr key={c.id}>
                  <td>
                    <span className={`dot ${c.enabled ? (c.goroutine_active ? 'dot-ok' : 'dot-warn') : 'dot-muted'}`} />
                  </td>
                  <td className="mono" style={{ fontWeight: 600 }}>{c.id}</td>
                  <td className="mono truncate" style={{ maxWidth: 250 }}>{c.rpc_endpoint}</td>
                  <td className="mono">{c.height.toLocaleString()}</td>
                  <td>
                    <span className={`badge ${c.goroutine_active ? 'badge-ok' : 'badge-muted'}`}>
                      {c.goroutine_active ? 'Running' : 'Stopped'}
                    </span>
                  </td>
                  <td>
                    <div className="flex-gap">
                      <button className="btn btn-ghost btn-sm" onClick={() => handleToggle(c.id, !c.enabled)}>
                        {c.enabled ? 'Disable' : 'Enable'}
                      </button>
                      <button className="btn btn-ghost btn-sm" onClick={() => setEditChain({ ...c })}>Edit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => setConfirmReinit(c.id)} style={{ color: 'var(--status-warn)' }}>Reinit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => setConfirmDelete(c.id)} style={{ color: 'var(--status-critical)' }}>Delete</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Add Chain Modal */}
      {showAdd && (
        <div className="modal-overlay" onClick={() => setShowAdd(false)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-title">Add Chain</div>
            <div className="modal-body" style={{ display: 'grid', gap: 12 }}>
              <div className="form-group">
                <label className="form-label">Chain ID</label>
                <input className="form-input" placeholder="e.g. test12" value={form.id} onChange={e => setForm(f => ({ ...f, id: e.target.value }))} />
              </div>
              <div className="form-group">
                <label className="form-label">RPC Endpoint</label>
                <input className="form-input" placeholder="https://rpc..." value={form.rpc_endpoint} onChange={e => setForm(f => ({ ...f, rpc_endpoint: e.target.value }))} />
              </div>
              <div className="form-group">
                <label className="form-label">GraphQL</label>
                <input className="form-input" placeholder="https://..." value={form.graphql} onChange={e => setForm(f => ({ ...f, graphql: e.target.value }))} />
              </div>
              <div className="form-group">
                <label className="form-label">GnoWeb</label>
                <input className="form-input" placeholder="https://..." value={form.gnoweb} onChange={e => setForm(f => ({ ...f, gnoweb: e.target.value }))} />
              </div>
              <label className="flex-gap" style={{ cursor: 'pointer' }}>
                <input type="checkbox" checked={form.enabled} onChange={e => setForm(f => ({ ...f, enabled: e.target.checked }))} />
                <span className="form-label" style={{ margin: 0 }}>Enable monitoring immediately</span>
              </label>
            </div>
            <div className="modal-actions">
              <button className="btn btn-secondary" onClick={() => setShowAdd(false)}>Cancel</button>
              <button className="btn btn-primary" onClick={handleAdd} disabled={!form.id || !form.rpc_endpoint || actionLoading}>
                {actionLoading ? <span className="spinner" /> : null} Create
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Edit Chain Modal */}
      {editChain && (
        <div className="modal-overlay" onClick={() => setEditChain(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-title">Edit {editChain.id}</div>
            <div className="modal-body" style={{ display: 'grid', gap: 12 }}>
              <div className="form-group">
                <label className="form-label">RPC Endpoint</label>
                <input className="form-input" value={editChain.rpc_endpoint} onChange={e => setEditChain(c => c ? { ...c, rpc_endpoint: e.target.value } : null)} />
              </div>
              <div className="form-group">
                <label className="form-label">GraphQL</label>
                <input className="form-input" value={editChain.graphql} onChange={e => setEditChain(c => c ? { ...c, graphql: e.target.value } : null)} />
              </div>
              <div className="form-group">
                <label className="form-label">GnoWeb</label>
                <input className="form-input" value={editChain.gnoweb} onChange={e => setEditChain(c => c ? { ...c, gnoweb: e.target.value } : null)} />
              </div>
            </div>
            <div className="modal-actions">
              <button className="btn btn-secondary" onClick={() => setEditChain(null)}>Cancel</button>
              <button className="btn btn-primary" onClick={handleUpdateEndpoints} disabled={actionLoading}>Save</button>
            </div>
          </div>
        </div>
      )}

      {/* Delete Confirmation */}
      {confirmDelete && (
        <ConfirmModal
          title={`Delete "${confirmDelete}"?`}
          message="All monitoring data, participation history, alert logs, monikers, and telegram subscriptions for this chain will be permanently deleted."
          confirmLabel="Delete Chain"
          variant="danger"
          onConfirm={handleDelete}
          onCancel={() => setConfirmDelete(null)}
          loading={actionLoading}
        />
      )}

      {/* Reinit Confirmation */}
      {confirmReinit && (
        <ConfirmModal
          title={`Reinitialize "${confirmReinit}"?`}
          message="This will purge all participation data, aggregates, and alert logs for this chain. Monikers and Telegram subscriptions will be preserved."
          confirmLabel="Reinitialize"
          variant="warning"
          onConfirm={handleReinit}
          onCancel={() => setConfirmReinit(null)}
          loading={actionLoading}
        />
      )}
    </>
  )
}
