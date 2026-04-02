import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { ClerkProvider, SignedIn, SignedOut, RedirectToSignIn } from '@clerk/clerk-react'
import { dark } from '@clerk/themes'
import { useEffect } from 'react'
import { useAuth } from '@clerk/clerk-react'

import { ToastProvider } from './hooks/useToast'
import { setTokenProvider } from './lib/api'
import { Layout } from './components/Layout'

import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import Chains from './pages/Chains'
import AlertConfig from './pages/AlertConfig'
import AlertHistory from './pages/AlertHistory'
import Monikers from './pages/Monikers'
import Users from './pages/Users'
import Webhooks from './pages/Webhooks'
import Telegram from './pages/Telegram'
import Schedules from './pages/Schedules'
import GovDAO from './pages/GovDAO'

const CLERK_KEY = import.meta.env.VITE_CLERK_PUBLISHABLE_KEY

/** Syncs Clerk's getToken into our API module */
function AuthSync() {
  const { getToken } = useAuth()
  useEffect(() => {
    setTokenProvider(getToken)
  }, [getToken])
  return null
}

function ProtectedRoutes() {
  return (
    <>
      <AuthSync />
      <SignedIn>
        <Routes>
          <Route element={<Layout />}>
            <Route index element={<Dashboard />} />
            <Route path="chains" element={<Chains />} />
            <Route path="config" element={<AlertConfig />} />
            <Route path="alerts" element={<AlertHistory />} />
            <Route path="monikers" element={<Monikers />} />
            <Route path="users" element={<Users />} />
            <Route path="webhooks" element={<Webhooks />} />
            <Route path="telegram" element={<Telegram />} />
            <Route path="schedules" element={<Schedules />} />
            <Route path="govdao" element={<GovDAO />} />
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </SignedIn>
      <SignedOut>
        <RedirectToSignIn />
      </SignedOut>
    </>
  )
}

/** Dev mode: no Clerk, direct access */
function DevRoutes() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="chains" element={<Chains />} />
        <Route path="config" element={<AlertConfig />} />
        <Route path="alerts" element={<AlertHistory />} />
        <Route path="monikers" element={<Monikers />} />
        <Route path="users" element={<Users />} />
        <Route path="webhooks" element={<Webhooks />} />
        <Route path="telegram" element={<Telegram />} />
        <Route path="schedules" element={<Schedules />} />
        <Route path="govdao" element={<GovDAO />} />
      </Route>
      <Route path="login" element={<Login />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

export default function App() {
  const hasClerk = !!CLERK_KEY

  return (
    <ToastProvider>
      <BrowserRouter>
        {hasClerk ? (
          <ClerkProvider publishableKey={CLERK_KEY} appearance={{ baseTheme: dark }}>
            <ProtectedRoutes />
          </ClerkProvider>
        ) : (
          <DevRoutes />
        )}
      </BrowserRouter>
    </ToastProvider>
  )
}
