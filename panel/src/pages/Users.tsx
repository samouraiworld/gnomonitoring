import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import { ConfirmModal } from '../components/ConfirmModal'
import { formatDate } from '../lib/format'
import type { User, ApiStatus } from '../types/api'

export default function Users() {
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [delUser, setDelUser] = useState<User | null>(null)
  const toast = useToast()

  const fetchUsers = async () => {
    try { setUsers(await api.get<User[]>('/admin/users')) }
    catch { toast.error('Failed to load users') }
    finally { setLoading(false) }
  }
  useEffect(() => { fetchUsers() }, [])

  const handleDelete = async () => {
    if (!delUser) return
    try {
      await api.del<ApiStatus>(`/admin/users/${delUser.user_id}`)
      toast.success(`User "${delUser.name || delUser.user_id}" deleted`)
      setDelUser(null)
      fetchUsers()
    } catch (err: any) { toast.error(err.message) }
  }

  return (
    <>
      <div className="page-header">
        <h1 className="page-title">User Management</h1>
        <p className="page-subtitle">{users.length} registered users</p>
      </div>
      {loading ? <div className="card"><div className="skeleton" style={{ height: 200 }} /></div> : (
        <div className="table-container">
          <table>
            <thead><tr><th>ID</th><th>Name</th><th>Email</th><th>Created</th><th>Actions</th></tr></thead>
            <tbody>
              {users.length === 0 ? (
                <tr><td colSpan={5}><div className="empty-state"><div className="empty-state-title">No users</div></div></td></tr>
              ) : users.map(u => (
                <tr key={u.user_id}>
                  <td className="mono" style={{ fontSize: 12 }}>{u.user_id}</td>
                  <td>{u.name || '—'}</td>
                  <td style={{ color: 'var(--text-muted)' }}>{u.email || '—'}</td>
                  <td style={{ fontSize: 12, color: 'var(--text-muted)' }}>{formatDate(u.created_at)}</td>
                  <td>
                    <button className="btn btn-danger btn-sm" onClick={() => setDelUser(u)}>Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {delUser && (
        <ConfirmModal title={`Delete "${delUser.name || delUser.user_id}"?`} message="Deleting this user will cascade to their webhooks, alert contacts, and report schedules." confirmLabel="Delete User" variant="danger" onConfirm={handleDelete} onCancel={() => setDelUser(null)} />
      )}
    </>
  )
}
