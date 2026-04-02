import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { ConfirmModal } from '../components/ConfirmModal'
import type { TelegramChat, TelegramSub, TelegramSchedule, ApiStatus } from '../types/api'

type Tab = 'chats' | 'subs' | 'schedules'

export default function Telegram() {
  const [tab, setTab] = useState<Tab>('chats')
  const [chats, setChats] = useState<TelegramChat[]>([])
  const [subs, setSubs] = useState<TelegramSub[]>([])
  const [schedules, setSchedules] = useState<TelegramSchedule[]>([])
  const [loading, setLoading] = useState(true)
  const [delChat, setDelChat] = useState<TelegramChat | null>(null)
  const toast = useToast()

  const fetchAll = async () => {
    setLoading(true)
    try {
      const [c, s, sc] = await Promise.all([
        api.get<TelegramChat[]>('/admin/telegram/chats'),
        api.get<TelegramSub[]>('/admin/telegram/subs'),
        api.get<TelegramSchedule[]>('/admin/telegram/schedules'),
      ])
      setChats(c); setSubs(s); setSchedules(sc)
    } catch { toast.error('Failed to load telegram data') }
    finally { setLoading(false) }
  }
  useEffect(() => { fetchAll() }, [])

  const toggleSub = async (id: number, activate: boolean) => {
    try {
      await api.put<ApiStatus>(`/admin/telegram/subs/${id}`, { activate })
      toast.success(activate ? 'Subscription activated' : 'Subscription deactivated')
      fetchAll()
    } catch (err: any) { toast.error(err.message) }
  }

  const handleDeleteChat = async () => {
    if (!delChat) return
    try {
      await api.del<ApiStatus>(`/admin/telegram/chats/${delChat.chat_id}`)
      toast.success('Chat deleted')
      setDelChat(null)
      fetchAll()
    } catch (err: any) { toast.error(err.message) }
  }

  return (
    <>
      <div className="page-header">
        <h1 className="page-title">Telegram Management</h1>
        <p className="page-subtitle">{chats.length} chats, {subs.length} subscriptions, {schedules.length} schedules</p>
      </div>

      <div className="tabs">
        <button className={`tab ${tab === 'chats' ? 'active' : ''}`} onClick={() => setTab('chats')}>Chats ({chats.length})</button>
        <button className={`tab ${tab === 'subs' ? 'active' : ''}`} onClick={() => setTab('subs')}>Subscriptions ({subs.length})</button>
        <button className={`tab ${tab === 'schedules' ? 'active' : ''}`} onClick={() => setTab('schedules')}>Schedules ({schedules.length})</button>
      </div>

      {loading ? <div className="card"><div className="skeleton" style={{ height: 200 }} /></div> : (
        <>
          {tab === 'chats' && (
            <div className="table-container">
              <table>
                <thead><tr><th>Chat ID</th><th>Chain</th><th>Type</th><th>Title</th><th>Actions</th></tr></thead>
                <tbody>
                  {chats.length === 0 ? (
                    <tr><td colSpan={5}><div className="empty-state"><div className="empty-state-title">No chats</div></div></td></tr>
                  ) : chats.map(c => (
                    <tr key={c.chat_id}>
                      <td className="mono">{c.chat_id}</td>
                      <td className="mono">{c.chain_id}</td>
                      <td><span className="badge badge-info">{c.chat_type}</span></td>
                      <td>{c.chat_title || '—'}</td>
                      <td><button className="btn btn-danger btn-sm" onClick={() => setDelChat(c)}>Delete</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {tab === 'subs' && (
            <div className="table-container">
              <table>
                <thead><tr><th>ID</th><th>Chat</th><th>Chain</th><th>Validator</th><th>Active</th></tr></thead>
                <tbody>
                  {subs.length === 0 ? (
                    <tr><td colSpan={5}><div className="empty-state"><div className="empty-state-title">No subscriptions</div></div></td></tr>
                  ) : subs.map(s => (
                    <tr key={s.ID}>
                      <td className="mono">{s.ID}</td>
                      <td className="mono">{s.chat_id}</td>
                      <td className="mono">{s.chain_id}</td>
                      <td>{s.moniker} <span className="mono" style={{ fontSize: 10, color: 'var(--text-muted)' }}>({s.addr.slice(0, 8)}...)</span></td>
                      <td>
                        <button className={`btn btn-sm ${s.activate ? 'btn-primary' : 'btn-secondary'}`} onClick={() => toggleSub(s.ID, !s.activate)}>
                          {s.activate ? 'Active' : 'Inactive'}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {tab === 'schedules' && (
            <div className="table-container">
              <table>
                <thead><tr><th>Chat ID</th><th>Chain</th><th>Time</th><th>Timezone</th><th>Active</th></tr></thead>
                <tbody>
                  {schedules.length === 0 ? (
                    <tr><td colSpan={5}><div className="empty-state"><div className="empty-state-title">No schedules</div></div></td></tr>
                  ) : schedules.map(s => (
                    <tr key={`${s.chat_id}-${s.chain_id}`}>
                      <td className="mono">{s.chat_id}</td>
                      <td className="mono">{s.chain_id}</td>
                      <td className="mono">{String(s.daily_report_hour).padStart(2, '0')}:{String(s.daily_report_minute).padStart(2, '0')}</td>
                      <td>{s.timezone}</td>
                      <td><span className={`badge ${s.activate ? 'badge-ok' : 'badge-muted'}`}>{s.activate ? 'Active' : 'Inactive'}</span></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}

      {delChat && (
        <ConfirmModal title={`Delete chat "${delChat.chat_title || delChat.chat_id}"?`} message="This will cascade delete the chat, all its subscriptions, and schedules." confirmLabel="Delete" variant="danger" onConfirm={handleDeleteChat} onCancel={() => setDelChat(null)} />
      )}
    </>
  )
}
