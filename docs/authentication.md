# Authentication

Gnomonitoring's API can be protected by either of two identity providers,
selected at runtime by the `auth_provider` key in `config.yaml`:

| `auth_provider` | Token verification | Admin check |
| --- | --- | --- |
| `clerk` (default) | JWKS signature/issuer/expiry check against the instance's keys, fetched from Clerk's Backend API with `clerk_secret_key` (`internal/clerkauth`) | live Clerk API call — `GET /users/{id}`, then `publicMetadata.role == "admin"` |
| `keycloak` | JWKS signature/issuer/expiry check against the realm's OIDC discovery document (`internal/keycloakauth`) | the `admin` role on the `gnomonitoring-panel` **client**, read straight out of the validated token |

Both paths ship in the binary. Switching provider — in either direction — is a
config edit plus a restart, never a code change or a revert.

Clerk tokens are verified in-house with go-jose v4, not with clerk-sdk-go: that
SDK parses tokens with go-jose v3, which has no fix for CVE-2026-116587 (a
crafted RSA key in a JWS `jwk` header burns CPU before any signature check).
`internal/clerkauth` keeps the SDK's checks (RS256 signature by a key of the
instance's JWKS named by `kid`, `exp`/`nbf`/`iat` with no leeway, Clerk issuer),
its key cache (a key is trusted for one hour after the JWKS that published it
was fetched; while the JWKS cannot be fetched, 24 hours more, with a log line)
and its answers: `403` without a token or for one that does not
parse (including one over 8 KiB, refused unparsed), `401` for a refused one.
A token naming an unknown key refetches the
JWKS at most once every 10 seconds, so forged tokens cannot exhaust the Backend
API rate limit the admin lookups share.

`dev_mode: true` bypasses all of this, unchanged: the user id comes from the
`X-Debug-UserID` header, or defaults to `local-dev-user`.

## Recorded migration state (2026-11-02)

The panel's [published bundle](https://panel.memba.samourai.app/assets/index-B1QQTGnr.js)
uses Keycloak at `https://auth.kodera.io`, realm
`samourai-app`, client `gnomonitoring-panel`. The realm was renamed from
`gno-world` on October 6; see the
[Keycloak infrastructure record](https://github.com/samouraiworld/kodera-keycloak/blob/9070481be3eedba04b7f9c07b8afc17d3fcc5474/README.md).
These public observations do not disclose or establish the backend's private
`auth_provider` or fallback settings.

Memba's optional account and alerts still obtain Clerk sessions; a connected Gno
wallet is a separate identity. [Gnolove #236](https://github.com/samouraiworld/gnolove/pull/236)
removed its Clerk integration and protected leaderboard-webhook API on November 2.
That removal does not migrate Memba to Keycloak and is not a reason to disable
Gnomonitoring's Clerk fallback while Memba still uses it.

[Security PR #189](https://github.com/samouraiworld/gnomonitoring/pull/189)
replaces the Clerk SDK verifier. Its integration with the alert fix is
[#191](https://github.com/samouraiworld/gnomonitoring/pull/191), kept in draft
pending a real Memba session test. The automated fixture tests do not establish
that compatibility. See the [release handoff](monitoring-release-handoff.md).

## Configuration

```yaml
auth_provider: "keycloak"                                       # or "clerk"
keycloak_issuer: "https://auth.kodera.io/realms/samourai-app"   # required in keycloak mode
keycloak_allowed_clients: ["gnomonitoring-panel"]               # audience boundary; see below
keycloak_clerk_fallback: true                                   # default true; see below
clerk_secret_key: "sk_live_..."                                 # still needed while the fallback is on
```

The panel is configured separately, at build time (Vite bakes `VITE_*` in — a
`docker compose pull` will **not** pick up a change here, only a rebuild):

```env
VITE_KEYCLOAK_URL=https://auth.kodera.io
VITE_KEYCLOAK_REALM=samourai-app
VITE_KEYCLOAK_CLIENT_ID=gnomonitoring-panel
```

Leaving all three unset builds an unauthenticated panel for use against a
`dev_mode: true` backend.

## When the issuer is unreachable

An unreachable `keycloak_issuer` does not stop the backend. Monitoring,
alerting, the Telegram bots, Prometheus and the public routes start normally.
The backend logs `[auth] ERROR: Keycloak issuer discovery failed` and retries
in the background, from 5 s and doubling up to 5 min between attempts. An
attempt succeeds only when both the OIDC discovery document and the JWKS (at
least one key) are fetched. Until one attempt succeeds:

- `/admin/*` and, with the Clerk fallback off, the general routes answer
  `503` with `Retry-After`, not `401`: the token was never checked.
- With the Clerk fallback on, the general routes answer `503` to a token whose
  `iss` claim names the Keycloak issuer, and hand every other token to Clerk,
  so Clerk callers keep working. The `iss` claim is read without checking the
  signature: it only picks between `503` and Clerk, it authenticates nothing.

Every request to the issuer (discovery and JWKS) times out after 15 s. The log
line `Keycloak issuer discovery succeeded on attempt N` marks the return to
normal. An empty or malformed `keycloak_issuer` (not an `http(s)://host` URL)
in keycloak mode is still a configuration error that stops startup.

## Why the admin role is client-scoped

The `samourai-app` realm is shared by multiple applications. Accepting a
realm-wide `admin` role here would let an unrelated application grant access
to Gnomonitoring. Gnomonitoring's admin role is instead defined on the
`gnomonitoring-panel` client, and `Claims.IsAdmin()` reads only
`resource_access["gnomonitoring-panel"].roles` — a realm role, or another
client's `admin` role, grants nothing here.

To grant it: Keycloak admin console → Clients → `gnomonitoring-panel` → Users in
role, or Users → *user* → Role mapping → **Filter by clients** → Assign role.
The realm-wide role filter is the wrong one.

See the current [Keycloak infrastructure repository](https://github.com/samouraiworld/kodera-keycloak).

## Which clients' tokens are accepted

`samourai-app` is shared by multiple clients, and a token signed by the realm is
valid whichever client obtained it. `keycloak_allowed_clients` is the audience
boundary: the verifier checks the token's `azp` claim (Keycloak sets it to the
client that obtained the token) against this list, falling back to `aud` only
for a token carrying no `azp` — Keycloak access tokens normally have
`aud: "account"`, which is why go-oidc's own single-audience check is disabled
in favour of this one.

It defaults to `["gnomonitoring-panel"]`. Add another application only after its
Keycloak migration is implemented and its actual client ID and intended API
access have been reviewed. Gnolove removing its Clerk routes is not such a migration.
An empty list disables the check and accepts every client in the realm.

The client-scoped admin role limits what a foreign token can *do*; the allowlist
is what stops it being accepted at all. Both matter.

## ⚠️ Realm prerequisite: `clerk_user_id` must not be user-writable

Because `EffectiveUserID()` treats `clerk_user_id` as the account identity (see
below), **whoever can write that attribute can take over the gnomonitoring rows
of the user it names** — their webhooks, alert contacts and report hours.

The old September instructions described an import profile with
`unmanagedAttributePolicy: ENABLED`. That is historical evidence, not the current
live setting. [Keycloak #61](https://github.com/samouraiworld/kodera-keycloak/pull/61),
merged October 29, sets the import profile to `ADMIN_EDIT` and records GET-only
readbacks on October 15 from production and staging showing `ADMIN_EDIT` already
applied and no explicit declaration of `clerk_user_id` in either user profile.
The recorded attribute counts do not establish that its stored identity values
are correct; #61 explicitly leaves that audit and other sync findings open.

For a future migration or a freshly restored realm, verify the effective user
profile still prevents self-service writes and review any explicit permissions
on `clerk_user_id`. Keep the realm import in
`samouraiworld/kodera-keycloak` aligned with that policy. Editing an import file
does not update an existing live realm. Do not reapply a live setting just
because an old runbook says it is pending, or copy user attributes or tokens into
a PR or chat as evidence.

This cannot be fixed in this repository: the claim arrives inside a validly
signed token, and nothing in that token distinguishes a sync-set value from a
self-set one.

## Panel access is gated twice, and only one gate matters

The panel initializes with `onLoad: 'login-required'`, which proves only that
the visitor belongs to the realm, which is a wider set than "gnomonitoring
admins". Do not treat `registrationAllowed: false` as an authorization boundary:
first-broker-login provisioning has its own realm policy.

So the panel additionally checks `hasAdminRole()` (`panel/src/lib/auth.ts`)
after init and renders an "Access denied" page instead of the app when the token
carries no `admin` role on the panel's own client.

**That check is UX only.** It reads a token the browser already holds and could
be bypassed by anyone willing to edit their own JavaScript. The real boundary is
the backend, which re-derives the same role from the token *it* validates and
answers 403 to every `/admin/*` request from a non-admin regardless. Never move
an authorization decision into the panel on the strength of this gate.

A holder of a valid `samourai-app` token from an allowed client is *authenticated* for
the general routes (`/users`, `/webhooks/*`, `/alert-contacts`, `/usersH`) and
can create rows there under their own user id — those routes are per-user by
design and have never required the admin role, under Clerk or Keycloak. Whether
social-login self-provisioning into `samourai-app` should be allowed at all is a
realm-level decision (`samouraiworld/kodera-keycloak`, first broker login flow), not
something this backend can settle.

## Why there is no database migration

Historical `user_id` values in Postgres (webhooks, alert contacts, report hours)
were written from Clerk's `sub` claim. Preserve those identities during migration.

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
`/alert-contacts`, `/usersH` — are also used by Memba's optional account and alerts
with its Clerk sessions. Gnolove's former leaderboard-webhook route was another
caller; it was removed in #236. In `keycloak` mode, these routes try Keycloak
verification first and fall back to Clerk when the token is not a valid Keycloak
one (`dualAccept` in `internal/api/auth.go`). A valid Keycloak token never reaches
the Clerk path.

Set `keycloak_clerk_fallback: false` only after every remaining consumer has
migrated or been retired, and that result has been checked with real sessions.
Memba still needs the bridge at the recorded state above. Keep the existing Clerk
instance key while this path is used; do not generate a replacement to perform a
compatibility test.

## Rollback

For the verifier/alert release, restore the previously recorded backend image
and preserve the deployed authentication configuration. See the
[backend release and return procedure](monitoring-release-handoff.md).

Reversing an identity-provider migration is a different operation. Setting
`auth_provider: "clerk"` rejects Keycloak tokens, including those from the current
panel. The panel contains no Clerk integration, so setting its old
`VITE_CLERK_PUBLISHABLE_KEY` does not restore one. Any planned provider reversal
must coordinate the backend and a compatible panel build and retain the
client-scoped authorization boundary. A backend image rollback does not itself
require an identity-provider change.
