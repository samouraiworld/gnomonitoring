import { Outlet } from 'react-router-dom'
import { Sidebar } from './Sidebar'
import { useKeycloak, displayName } from '../lib/auth'

function TopBarUser() {
  const kc = useKeycloak()

  // No Keycloak instance means this build has no VITE_KEYCLOAK_* config and is
  // running against a dev_mode backend — there is no session to show.
  if (!kc) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span className="dot dot-ok" />
        <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>Dev Mode</span>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>{displayName(kc)}</span>
      <button className="btn btn-sm" onClick={() => void kc.logout()}>
        Sign out
      </button>
    </div>
  )
}

export function Layout() {
  return (
    <div className="app-layout">
      <Sidebar />
      <main className="main-content">
        <header className="topbar">
          <div className="topbar-title">Admin Panel</div>
          <div className="topbar-actions">
            <TopBarUser />
          </div>
        </header>
        <div className="page-content">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
