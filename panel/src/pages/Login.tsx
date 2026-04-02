import { SignIn } from '@clerk/clerk-react'

export default function Login() {
  return (
    <div className="login-container">
      <div className="login-card fade-in">
        <div className="login-title">Gnomonitoring</div>
        <div className="login-subtitle">Admin Panel — Samouraï Crew Only</div>
        <SignIn routing="hash" />
      </div>
    </div>
  )
}
