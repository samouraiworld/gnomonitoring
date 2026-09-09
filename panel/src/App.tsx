import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { useEffect, useState } from 'react'
import type Keycloak from 'keycloak-js'

import { ToastProvider } from './hooks/useToast'
import { setTokenProvider } from './lib/api'
import { createKeycloak, makeGetToken, hasAdminRole, displayName, KeycloakContext } from './lib/auth'
import { Layout } from './components/Layout'

import Dashboard from './pages/Dashboard'
import Chains from './pages/Chains'
import AlertConfig from './pages/AlertConfig'
import AlertHistory from './pages/AlertHistory'
import Monikers from './pages/Monikers'
import Reports from './pages/Reports'
import Users from './pages/Users'
import Webhooks from './pages/Webhooks'
import Telegram from './pages/Telegram'
import Schedules from './pages/Schedules'
import GovDAO from './pages/GovDAO'

function AppRoutes() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="chains" element={<Chains />} />
        <Route path="config" element={<AlertConfig />} />
        <Route path="alerts" element={<AlertHistory />} />
        <Route path="monikers" element={<Monikers />} />
        <Route path="reports" element={<Reports />} />
        <Route path="users" element={<Users />} />
        <Route path="webhooks" element={<Webhooks />} />
        <Route path="telegram" element={<Telegram />} />
        <Route path="schedules" element={<Schedules />} />
        <Route path="govdao" element={<GovDAO />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

/** Full-page message, styled like the old sign-in card. */
function Notice({
  title,
  children,
  action,
}: {
  title: string
  children: React.ReactNode
  action?: React.ReactNode
}) {
  return (
    <div className="login-container">
      <div className="login-card fade-in">
        <div className="login-title">Gnomonitoring</div>
        <div className="login-subtitle">{title}</div>
        <p style={{ fontSize: 12, color: 'var(--text-muted)' }}>{children}</p>
        {action}
      </div>
    </div>
  )
}

export default function App() {
  // Created once: keycloak-js keeps its session state on the instance, and a
  // second instance would re-run the redirect dance on every render.
  const [kc] = useState<Keycloak | null>(() => createKeycloak())
  // Without a Keycloak client this is a dev-mode build talking to a
  // `dev_mode: true` backend, so there is nothing to wait for and no session.
  const [ready, setReady] = useState(!kc)
  const [authenticated, setAuthenticated] = useState(false)
  const [initError, setInitError] = useState<string | null>(null)

  useEffect(() => {
    if (!kc) return

    // `login-required` reproduces the old <SignedOut><RedirectToSignIn />:
    // an unauthenticated visitor is sent straight to Keycloak's hosted login.
    kc.init({ onLoad: 'login-required', pkceMethod: 'S256' })
      .then((auth) => {
        if (auth) setTokenProvider(makeGetToken(kc))
        setAuthenticated(auth)
        setReady(true)
      })
      .catch((err: unknown) => {
        setInitError(err instanceof Error ? err.message : 'Keycloak initialization failed')
        setReady(true)
      })
  }, [kc])

  if (initError) {
    return <Notice title="Sign-in unavailable">{initError}</Notice>
  }

  // Brief blank frame while keycloak-js checks the session; when the user is
  // not signed in it navigates away to Keycloak rather than rendering at all.
  if (!ready) return null
  if (kc && !authenticated) return null

  // Authenticated but not an admin. `login-required` only proves the visitor
  // belongs to the realm, which is shared with memba and gnolove, so this is
  // reachable by design. Rendering the panel anyway would just produce a
  // "Forbidden" error on every page — the backend rejects each /admin/* call
  // with 403 regardless of what is shown here, which remains the actual
  // security boundary.
  if (kc && !hasAdminRole(kc)) {
    return (
      <Notice
        title="Access denied"
        action={
          <button className="btn btn-sm" onClick={() => void kc.logout()}>
            Sign out
          </button>
        }
      >
        Signed in as {displayName(kc)}, but this account does not have the admin
        role for the Gnomonitoring panel. Ask an administrator to grant it, then
        sign in again.
      </Notice>
    )
  }

  return (
    <KeycloakContext.Provider value={kc}>
      <ToastProvider>
        <BrowserRouter>
          <AppRoutes />
        </BrowserRouter>
      </ToastProvider>
    </KeycloakContext.Provider>
  )
}
