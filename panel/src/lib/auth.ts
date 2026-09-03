/**
 * Keycloak wiring for the admin panel.
 *
 * The only contract the rest of the panel depends on is `lib/api.ts`'s
 * `setTokenProvider(fn)` seam — `makeGetToken` below fills it with exactly the
 * shape Clerk's `useAuth().getToken` used to, so every `api.get/post/put/del`
 * call site is unchanged by the migration.
 */
import { createContext, useContext } from 'react'
import Keycloak from 'keycloak-js'

const KEYCLOAK_URL = import.meta.env.VITE_KEYCLOAK_URL
const KEYCLOAK_REALM = import.meta.env.VITE_KEYCLOAK_REALM
const KEYCLOAK_CLIENT_ID = import.meta.env.VITE_KEYCLOAK_CLIENT_ID

/**
 * Whether this build was given a Keycloak client to talk to. When it wasn't,
 * the panel runs unauthenticated against a `dev_mode: true` backend — the same
 * role `VITE_CLERK_PUBLISHABLE_KEY`'s absence used to play.
 */
export const isKeycloakConfigured = Boolean(
  KEYCLOAK_URL && KEYCLOAK_REALM && KEYCLOAK_CLIENT_ID,
)

export function createKeycloak(): Keycloak | null {
  if (!isKeycloakConfigured) return null
  return new Keycloak({
    url: KEYCLOAK_URL,
    realm: KEYCLOAK_REALM,
    clientId: KEYCLOAK_CLIENT_ID,
  })
}

/**
 * Returns a token provider matching `setTokenProvider`'s `() => Promise<string
 * | null>` signature. It refreshes the access token whenever it is within 30s
 * of expiry, so callers always get a live token without caching one themselves
 * — mirroring Clerk's per-request `getToken()` behaviour.
 *
 * A refresh failure means the session is gone (refresh token expired, or the
 * user was logged out in Keycloak). Returning null lets the request go out
 * unauthenticated and come back 401, which the panel already surfaces as an
 * ApiError; forcing a redirect from inside a data fetch would lose whatever the
 * user was doing.
 */
export function makeGetToken(kc: Keycloak) {
  return async (): Promise<string | null> => {
    try {
      await kc.updateToken(30)
    } catch {
      return null
    }
    return kc.token ?? null
  }
}

/** Display name for the signed-in user, best-effort across realm setups. */
export function displayName(kc: Keycloak): string {
  const claims = kc.tokenParsed as
    | { preferred_username?: string; email?: string; name?: string }
    | undefined
  return claims?.preferred_username ?? claims?.email ?? claims?.name ?? 'Signed in'
}

/**
 * The single Keycloak instance, shared with components that need it (the top
 * bar's sign-out button). Null in dev mode, where there is no instance.
 */
export const KeycloakContext = createContext<Keycloak | null>(null)

export function useKeycloak(): Keycloak | null {
  return useContext(KeycloakContext)
}
