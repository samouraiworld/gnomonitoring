# Authentication

Gnomonitoring's API can be protected by either of two identity providers,
selected at runtime by the `auth_provider` key in `config.yaml`:

| `auth_provider` | Token verification | Admin check |
| --- | --- | --- |
| `clerk` (default) | `clerkhttp.RequireHeaderAuthorization()` | live Clerk API call — `clerkuser.Get`, then `publicMetadata.role == "admin"` |
| `keycloak` | JWKS signature/issuer/expiry check against the realm's OIDC discovery document (`internal/keycloakauth`) | the `admin` role on the `gnomonitoring-panel` **client**, read straight out of the validated token |

Both paths ship in the binary. Switching provider — in either direction — is a
config edit plus a restart, never a code change or a revert.

`dev_mode: true` bypasses all of this, unchanged: the user id comes from the
`X-Debug-UserID` header, or defaults to `local-dev-user`.

## Configuration

```yaml
auth_provider: "keycloak"                                       # or "clerk"
keycloak_issuer: "https://auth.samourai.app/realms/gno-world"   # required in keycloak mode
keycloak_clerk_fallback: true                                   # default true; see below
clerk_secret_key: "sk_live_..."                                 # still needed while the fallback is on
```

The panel is configured separately, at build time (Vite bakes `VITE_*` in — a
`docker compose pull` will **not** pick up a change here, only a rebuild):

```env
VITE_KEYCLOAK_URL=https://auth.samourai.app
VITE_KEYCLOAK_REALM=gno-world
VITE_KEYCLOAK_CLIENT_ID=gnomonitoring-panel
```

Leaving all three unset builds an unauthenticated panel for use against a
`dev_mode: true` backend.

## Why the admin role is client-scoped

The `gno-world` realm is shared with memba and gnolove. A realm-wide `admin`
role would therefore make an admin on any one of the three an admin on all
three. Gnomonitoring's admin role is instead defined on the
`gnomonitoring-panel` client, and `Claims.IsAdmin()` reads only
`resource_access["gnomonitoring-panel"].roles` — a realm role, or another
client's `admin` role, grants nothing here.

To grant it: Keycloak admin console → Clients → `gnomonitoring-panel` → Users in
role, or Users → *user* → Role mapping → **Filter by clients** → Assign role.
The realm-wide role filter is the wrong one.

See `docs/2026-09-02-samourai-lasuite-realm-naming.md` in `samouraiworld/keycloak`.

## Why there is no database migration

Every `user_id` already stored in Postgres (webhooks, alert contacts, report
hours) is a Clerk user id, because it was written from Clerk's `sub` claim.

The realm carries that original value forward as a `clerk_user_id` claim, set on
each migrated user by the Clerk→Keycloak sync and mapped into the token by the
`clerk-user-id` protocol mapper on the `gnomonitoring-panel` client.
`Claims.EffectiveUserID()` returns it in preference to Keycloak's own `sub`, so a
migrated user keeps resolving to the same rows after the cutover. Users created
directly in Keycloak afterwards have no such claim and fall back to `sub` —
correct, since no legacy row references them.

## Dual-accept on the general routes

`/admin/*` is called only by this repo's own panel, which cuts over in the same
deploy as the backend. It is strict: in `keycloak` mode it accepts Keycloak
tokens only.

The general routes — `/webhooks/govdao`, `/webhooks/validator`, `/users`,
`/alert-contacts`, `/usersH` — are also called by **memba's `/alerts` page** and
**gnolove's leaderboard-webhooks route**, using bearer tokens from *those apps'*
own Clerk sessions. Those apps migrate to Keycloak on their own schedule, so in
`keycloak` mode these routes try Keycloak verification first and fall back to
Clerk when the token is not a valid Keycloak one (`dualAccept` in
`internal/api/auth.go`). A valid Keycloak token never reaches the Clerk path, so
the bridge costs an already-migrated caller nothing.

Set `keycloak_clerk_fallback: false` to drop the bridge — but **only** once
memba and gnolove are both issuing Keycloak tokens. Doing it earlier 401s their
live traffic.

## Rollback

Set `auth_provider: "clerk"` in the backend config, restore the panel's Clerk
env vars, rebuild the frontend image, restart. No code change is involved,
because the Clerk path was left intact rather than replaced.
