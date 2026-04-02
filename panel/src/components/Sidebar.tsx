import { NavLink } from 'react-router-dom'

const NAV_ITEMS = [
  { section: 'Overview' },
  { to: '/', icon: '◈', label: 'Dashboard' },
  { section: 'Monitoring' },
  { to: '/chains', icon: '⛓', label: 'Chains' },
  { to: '/config', icon: '⚙', label: 'Alert Config' },
  { to: '/alerts', icon: '🔔', label: 'Alert History' },
  { to: '/monikers', icon: '🏷', label: 'Monikers' },
  { section: 'Users & Integrations' },
  { to: '/users', icon: '👤', label: 'Users' },
  { to: '/webhooks', icon: '🔗', label: 'Webhooks' },
  { to: '/telegram', icon: '✈', label: 'Telegram' },
  { to: '/schedules', icon: '🕐', label: 'Schedules' },
  { section: 'Governance' },
  { to: '/govdao', icon: '🏛', label: 'GovDAO' },
] as const

export function Sidebar() {
  return (
    <aside className="sidebar">
      <div className="sidebar-brand">
        <img src="/memba-icon.png" alt="Memba" className="sidebar-brand-icon" />
        <div>
          <div className="sidebar-brand-text">Gnomonitoring</div>
          <div className="sidebar-brand-sub">Admin Panel</div>
        </div>
      </div>
      <nav className="sidebar-nav">
        {NAV_ITEMS.map((item, i) => {
          if ('section' in item) {
            return <div key={i} className="sidebar-section">{item.section}</div>
          }
          return (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === '/'}
              className={({ isActive }) => `sidebar-link ${isActive ? 'active' : ''}`}
            >
              <span className="sidebar-link-icon">{item.icon}</span>
              {item.label}
            </NavLink>
          )
        })}
      </nav>
      <div className="sidebar-footer">
        <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>
          Samouraï Coop © 2026
        </div>
      </div>
    </aside>
  )
}
