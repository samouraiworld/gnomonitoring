import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { ConfirmModal } from '../components/ConfirmModal'
import { truncateAddr, copyToClipboard } from '../lib/format'
import type { WebhookAdmin, ApiStatus } from '../types/api'

export default function Webhooks() {
  const [webhooks, setWebhooks] = useState<WebhookAdmin[]>([])
  const [loading, setLoading] = useState(true)
  const [tab, setTab] = useState<'validator' | 'govdao'>('validator')
  const [delWebhook, setDelWebhook] = useState<WebhookAdmin | null>(null)
  const [resetId, setResetId] = useState<number | null>(null)
  const toast = useToast()

  const fetch = async () => {
    try { setWebhooks(await api.get<WebhookAdmin[]>('/admin/webhooks')) }
    catch { toast.error('Failed to load webhooks') }
    finally { setLoading(false) }
  }
  useEffect(() => { fetch() }, [])

  const filtered = webhooks.filter(w => w.kind === tab)

  const handleDelete = async () => {
    if (!delWebhook) return
    try {
      await api.del<ApiStatus>(`/admin/webhooks/${delWebhook.kind}/${delWebhook.id}`)
      toast.success('Webhook deleted')
      setDelWebhook(null)
      fetch()
    } catch (err: any) { toast.error(err.message) }
  }

  const handleReset = async () => {
    if (resetId === null) return
    try {
      await api.put<ApiStatus>(`/admin/webhooks/govdao/${resetId}/reset`)
      toast.success('GovDAO tracker reset')
      setResetId(null)
    } catch (err: any) { toast.error(err.message) }
  }

  return (
    <>
      <div className="page-header">
        <h1 className="page-title">Webhook Management</h1>
        <p className="page-subtitle">{webhooks.length} total webhooks</p>
      </div>

      <div className="tabs">
        <button className={`tab ${tab === 'validator' ? 'active' : ''}`} onClick={() => setTab('validator')}>Validator ({webhooks.filter(w => w.kind === 'validator').length})</button>
        <button className={`tab ${tab === 'govdao' ? 'active' : ''}`} onClick={() => setTab('govdao')}>GovDAO ({webhooks.filter(w => w.kind === 'govdao').length})</button>
      </div>

      {loading ? <div className="card"><div className="skeleton" style={{ height: 200 }} /></div> : (
        <div className="table-container">
          <table>
            <thead><tr><th>ID</th><th>User</th><th>URL</th><th>Type</th><th>Chain</th><th>Actions</th></tr></thead>
            <tbody>
              {filtered.length === 0 ? (
                <tr><td colSpan={6}><div className="empty-state"><div className="empty-state-title">No {tab} webhooks</div></div></td></tr>
              ) : filtered.map(w => (
                <tr key={`${w.kind}-${w.id}`}>
                  <td className="mono">{w.id}</td>
                  <td className="mono" style={{ fontSize: 11 }}>{truncateAddr(w.user_id, 8)}</td>
                  <td>
                    <span className="mono truncate" style={{ maxWidth: 200, display: 'inline-block' }}>{w.url}</span>
                    <button className="copy-btn" onClick={() => { copyToClipboard(w.url); toast.success('Copied') }}>📋</button>
                  </td>
                  <td><span className="badge badge-info">{w.type}</span></td>
                  <td className="mono">{w.chain_id || '—'}</td>
                  <td>
                    <div className="flex-gap">
                      {w.kind === 'govdao' && <button className="btn btn-ghost btn-sm" onClick={() => setResetId(w.id)}>Reset</button>}
                      <button className="btn btn-ghost btn-sm" onClick={() => setDelWebhook(w)} style={{ color: 'var(--status-critical)' }}>Delete</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {delWebhook && (
        <ConfirmModal title="Delete Webhook?" message={`Remove this ${delWebhook.kind} webhook for user ${truncateAddr(delWebhook.user_id, 8)}?`} confirmLabel="Delete" variant="danger" onConfirm={handleDelete} onCancel={() => setDelWebhook(null)} />
      )}
      {resetId !== null && (
        <ConfirmModal title="Reset GovDAO Tracker?" message="This will reset the last_checked_id to -1, causing the webhook to re-process all proposals." confirmLabel="Reset" variant="warning" onConfirm={handleReset} onCancel={() => setResetId(null)} />
      )}
    </>
  )
}
