import { useState, useEffect } from 'react'
import { api } from '../lib/api'
import { useToast } from '../hooks/useToast'
import type { HourReport, ApiStatus } from '../types/api'

export default function Schedules() {
  const [reports, setReports] = useState<HourReport[]>([])
  const [loading, setLoading] = useState(true)
  const [editId, setEditId] = useState<string | null>(null)
  const [editForm, setEditForm] = useState({ hour: 0, minute: 0, timezone: 'Europe/Paris' })
  const toast = useToast()

  const fetchReports = async () => {
    try { setReports(await api.get<HourReport[]>('/admin/schedules')) }
    catch { toast.error('Failed to load schedules') }
    finally { setLoading(false) }
  }
  useEffect(() => { fetchReports() }, [])

  const handleSave = async (userId: string) => {
    try {
      await api.put<ApiStatus>(`/admin/schedules/${userId}`, editForm)
      toast.success('Schedule updated')
      setEditId(null)
      fetchReports()
    } catch (err: any) { toast.error(err.message) }
  }

  return (
    <>
      <div className="page-header">
        <h1 className="page-title">Report Schedules</h1>
        <p className="page-subtitle">{reports.length} web user report schedules</p>
      </div>
      {loading ? <div className="card"><div className="skeleton" style={{ height: 200 }} /></div> : (
        <div className="table-container">
          <table>
            <thead><tr><th>User ID</th><th>Time</th><th>Timezone</th><th>Actions</th></tr></thead>
            <tbody>
              {reports.length === 0 ? (
                <tr><td colSpan={4}><div className="empty-state"><div className="empty-state-title">No schedules configured</div></div></td></tr>
              ) : reports.map(r => {
                const isEditing = editId === r.user_id
                return (
                  <tr key={r.user_id}>
                    <td className="mono" style={{ fontSize: 11 }}>{r.user_id}</td>
                    <td>
                      {isEditing ? (
                        <div className="flex-gap">
                          <input className="form-input" type="number" min={0} max={23} value={editForm.hour} onChange={e => setEditForm(f => ({ ...f, hour: parseInt(e.target.value) || 0 }))} style={{ width: 60 }} />
                          <span>:</span>
                          <input className="form-input" type="number" min={0} max={59} value={editForm.minute} onChange={e => setEditForm(f => ({ ...f, minute: parseInt(e.target.value) || 0 }))} style={{ width: 60 }} />
                        </div>
                      ) : (
                        <span className="mono">{String(r.daily_report_hour).padStart(2, '0')}:{String(r.daily_report_minute).padStart(2, '0')}</span>
                      )}
                    </td>
                    <td>
                      {isEditing ? (
                        <input className="form-input" value={editForm.timezone} onChange={e => setEditForm(f => ({ ...f, timezone: e.target.value }))} style={{ width: 160 }} />
                      ) : (
                        r.timezone
                      )}
                    </td>
                    <td>
                      {isEditing ? (
                        <div className="flex-gap">
                          <button className="btn btn-primary btn-sm" onClick={() => handleSave(r.user_id)}>Save</button>
                          <button className="btn btn-ghost btn-sm" onClick={() => setEditId(null)}>Cancel</button>
                        </div>
                      ) : (
                        <button className="btn btn-ghost btn-sm" onClick={() => { setEditId(r.user_id); setEditForm({ hour: r.daily_report_hour, minute: r.daily_report_minute, timezone: r.timezone }) }}>Edit</button>
                      )}
                    </td>
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
