import { Outlet } from 'react-router-dom'
import { Sidebar } from './Sidebar'
import { lazy, Suspense } from 'react'

const CLERK_KEY = import.meta.env.VITE_CLERK_PUBLISHABLE_KEY

const ClerkUserButton = CLERK_KEY
  ? lazy(async () => {
      const { UserButton } = await import('@clerk/clerk-react')
      return {
        default: () => (
          <UserButton
            appearance={{ elements: { avatarBox: { width: 30, height: 30 } } }}
          />
        ),
      }
    })
  : null

function TopBarUser() {
  if (!ClerkUserButton) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span className="dot dot-ok" />
        <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>Dev Mode</span>
      </div>
    )
  }
  return (
    <Suspense fallback={null}>
      <ClerkUserButton />
    </Suspense>
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
