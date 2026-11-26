# Complete monitoring account erasure

`POST /users/erase` is the opt-in complete-account operation for Memba. It uses the existing general authentication middleware, accepts no required body, and returns **204 only after commit**, including repeat calls and identities never provisioned here. Errors and an absent endpoint (404) are failures: a client must not advance to deleting its sign-in identity until it receives 204.

`DELETE /users` retains its historical reset behavior: remove monitoring rows, then allow the identity to configure alerts again. It neither creates nor clears a complete-erasure marker. An erased identity cannot be restored by calling reset.

After complete erasure, user mutations return 410 JSON `{"code":"account_erased","error":"This monitoring account was erased."}` with `Cache-Control: no-store`. Existing reads can return empty data; they do not provision users. Admin operations for other identities remain unaffected. The erasure endpoint never calls Clerk, Keycloak, email or webhook providers.

## Durable boundary and retention

`account_erasures` contains the full SHA-256 digest of a versioned domain separator plus the **effective user ID**, and `erased_at`. EffectiveUserID already maps a migrated Keycloak account to its former Clerk subject, so changing provider cannot bypass the same marker. No email, name, consent history or raw user ID is retained in this table. The digest is pseudonymized, not anonymous. There is no automatic expiry or purge; changing retention requires an explicit reviewed policy/migration.

Every persistent monitoring write for one identity takes the same PostgreSQL transaction-scoped advisory lock, then checks the full digest. Erasure takes that lock, inserts the marker and deletes all five user tables atomically. The lock uses 64 digest bits; a collision only serializes unrelated identities, while the marker always uses all 256 bits. Read-committed snapshots are required and inherited stronger isolation is refused. SQL values are parameterized. Each operation locks one identity; a background batch releases one transaction before processing another identity.

The five explicitly removed tables are `users`, `hour_reports`, `alert_contacts`, `webhook_gov_daos`, and `webhook_validators`. Child rows are removed even without a parent `users` row. Schedule provisioning uses `ON CONFLICT DO NOTHING` inside its owner's transaction, so an existing schedule does not abort a PostgreSQL transaction or prevent webhook creation.

## Write inventory

- `database/db.go`: user create/update, GovDAO and validator webhook create/update/delete, alert-contact create/update/delete, report-schedule updates. The private schedule creator is only called by guarded user/webhook insertion.
- `UpdateLastCheckedID`: background update-only cursor writes resolve owner IDs, process one guarded transaction at a time, and skip an identity erased after the initial read without stopping other owners.
- `database/db_admin.go`: schedule updates, webhook deletion and GovDAO cursor reset use the same owner guard. Admin reset/delete-user uses historical `DeleteUser`, which holds the owner lock and cannot touch the marker.
- Other scheduler/report code reads rows and may hold a timer or a previously fetched webhook destination. It does not recreate these SQL rows. A notification already in flight is not recalled by this patch; cancellation of external delivery is a separate boundary. Telegram subscriptions are a different identity/data model and are not deleted by a Memba web-account request.
- There is no guard against manual SQL or old deployed binaries that bypass these functions. Activation must wait until all request-serving monitoring instances use this version and old instances are drained. Restore/rollback must preserve markers; reverting to an unguarded binary after erasure reopens provisioning and is unsafe.

## Migration and activation gate

This patch adds `AccountErasure` to the existing `InitDB` AutoMigrate sequence. Startup therefore creates a new persistent table. **Production migration and deployment require the owner's explicit go.** No migration or deployment is performed by this change's preparation. Verify backups, retention wording and rollback compatibility first. Do not drop the marker table while erased identities may still authenticate.

The repository builds/pushes a backend image after merging main; its production deployment workflow is manual `workflow_dispatch`. Building an image is not proof that production has deployed it. Before enabling Memba account deletion, verify the live monitoring version provides the strict 204 contract and that every writer is upgraded. Memba remains feature-flagged off until its own activation gate is satisfied.

## Validation

The PostgreSQL tests require `TEST_DATABASE_DSN` pointing to a disposable database. They create and drop isolated schemas and use separate pools for both race directions, plus rollback, pool-reopen persistence, orphan cleanup, legacy reset, unrelated admin/background work and Clerk/Keycloak effective-ID equivalence. The existing Go Tests workflow already supplies PostgreSQL 16 and runs `go test -race ./...`; no SQLite fallback is accepted as concurrency evidence.
