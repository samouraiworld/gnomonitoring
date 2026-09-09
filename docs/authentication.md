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
keycloak_allowed_clients: ["gnomonitoring-panel"]               # audience boundary; see below
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

## Which clients' tokens are accepted

`gno-world` is shared with memba and gnolove, and a token signed by the realm is
valid whichever client obtained it. `keycloak_allowed_clients` is the audience
boundary: the verifier checks the token's `azp` claim (Keycloak sets it to the
client that obtained the token) against this list, falling back to `aud` only
for a token carrying no `azp` — Keycloak access tokens normally have
`aud: "account"`, which is why go-oidc's own single-audience check is disabled
in favour of this one.

It defaults to `["gnomonitoring-panel"]`. Add `memba-web` / `gnolove-web` when
those apps cut over and start calling the general routes with Keycloak tokens.
An empty list disables the check and accepts every client in the realm.

The client-scoped admin role limits what a foreign token can *do*; the allowlist
is what stops it being accepted at all. Both matter.

## ⚠️ Realm prerequisite: `clerk_user_id` must not be user-writable

Because `EffectiveUserID()` treats `clerk_user_id` as the account identity (see
below), **whoever can write that attribute can take over the gnomonitoring rows
of the user it names** — their webhooks, alert contacts and report hours.

`clerk_user_id` is an *unmanaged* attribute in the `gno-world` realm's
declarative user profile. As of 2026-09-03 that profile has
`"unmanagedAttributePolicy": "ENABLED"`, which makes unmanaged attributes
readable **and writable by users themselves**, not only by admins and the sync
tool. Under that setting an authenticated realm user can set their own
`clerk_user_id` to a victim's Clerk id and inherit their rows.

**Before flipping `auth_provider: "keycloak"` in production**, change the policy
to `ADMIN_EDIT` (admins and the sync can write it, users cannot) in
`samouraiworld/keycloak`'s `deploy/keycloak/import/gno-world-realm.json` *and*
on the live prod and staging realms — the import file only applies at realm
creation, so an existing realm must also be changed by hand. Verify with:

```bash
kcadm.sh get users/<some-user-id> -r gno-world --fields attributes
```

after attempting a self-service edit as a non-admin user.

This cannot be fixed in this repository: the claim arrives inside a validly
signed token, and nothing in that token distinguishes a sync-set value from a
self-set one.

## Panel access is gated twice, and only one gate matters

The panel initializes with `onLoad: 'login-required'`, which proves only that
the visitor belongs to the realm — and `gno-world` is shared with memba and
gnolove, so that is a much wider set than "gnomonitoring admins". Worse, the
realm's GitHub and Google identity providers create an account on first broker
login, so `registrationAllowed: false` does **not** stop a stranger from
obtaining a `gno-world` identity: anyone with a GitHub account can.

So the panel additionally checks `hasAdminRole()` (`panel/src/lib/auth.ts`)
after init and renders an "Access denied" page instead of the app when the token
carries no `admin` role on the panel's own client.

**That check is UX only.** It reads a token the browser already holds and could
be bypassed by anyone willing to edit their own JavaScript. The real boundary is
the backend, which re-derives the same role from the token *it* validates and
answers 403 to every `/admin/*` request from a non-admin regardless. Never move
an authorization decision into the panel on the strength of this gate.

Note that a stranger holding a `gno-world` token is still *authenticated* for
the general routes (`/users`, `/webhooks/*`, `/alert-contacts`, `/usersH`) and
can create rows there under their own user id — those routes are per-user by
design and have never required the admin role, under Clerk or Keycloak. Whether
social-login self-provisioning into `gno-world` should be allowed at all is a
realm-level decision (`samouraiworld/keycloak`, first broker login flow), not
something this backend can settle.

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

**Backend:** set `auth_provider: "clerk"` and restart. No code change is
involved — the Clerk path was left intact rather than replaced, so this is a
complete rollback for the API and for any caller holding a Clerk token (memba,
gnolove).

**Panel:** *not* config-only. This branch removes `@clerk/clerk-react` from the
panel entirely, so restoring `VITE_CLERK_PUBLISHABLE_KEY` does nothing — the
bundle has no Clerk code left to mint a token with. Rolling the panel back means
redeploying the **previous frontend image** (the last one built before this
change), which still contains the Clerk integration.

So plan the cutover accordingly: keep the pre-cutover frontend image tag
available and note it before deploying, because `docker compose build frontend`
overwrites the local image.
