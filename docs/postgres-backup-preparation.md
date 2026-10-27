# PostgreSQL backup preparation

This tool prepares a future operator-run backup. It does not approve a storage
destination, deploy an image, stop a service, restore data, or upload files. No
production export belongs in a GitHub Actions artifact, this repository, or chat.

The existing `backend/backup_chain.sh` defaults to `./backups` and also creates
per-chain CSV exports. That relative default is not an approved production
destination. The deployment-provenance runbook requires a verified backup but
does not select storage. The targeted historical metadata search on 2026-10-27
did not establish an approved backup location.

## Before a real export

The operator must separately confirm the exact destination, available capacity,
encryption at rest, access, retention and transfer policy. Filesystem permissions
cannot establish those approvals or prove encryption. Do not execute while that
decision is pending.

Obtain the full confirmed PostgreSQL container ID, its immutable image ID,
Compose project, database, role, server major, socket directory and port from
the read-only inventory.
Also confirm the Docker daemon's local Unix socket. The tool requires that
explicit socket, resolves and verifies its file type/owner, passes `--host
unix://...` on every Docker command and strips all inherited `DOCKER_*`
variables. TCP daemon addresses and context fallback are not supported.
There are no container/database defaults. The tools run inside that container
using its existing local authentication; the script never reads application
configuration, Docker environment, connection passwords or private keys. It
passes no password on argv and does not set `PGPASSWORD`.

The absolute destination must already exist, be canonical with no symlink
components, be owned by the executing user, have mode `0700`, and sit outside a
Git checkout. It must have at least twice the database's physical size plus
1 GiB free. This conservative check is not a guarantee of archive size or a
substitute for monitoring storage during export.

Ancestors must belong to the executing user or root and must not permit
untrusted group/world replacement (a sticky temporary-directory ancestor is
allowed). Storage ACLs, remote mounts and encryption still need operator review.

## Check-only is the default

Fill these parameters only from confirmed inventory and the separately approved
storage decision. This command checks selected metadata; it produces no dump:

```sh
python3 scripts/backup/prepare-postgres-backup.py \
  --docker-socket "$CONFIRMED_LOCAL_DOCKER_SOCKET" \
  --container-id "$CONFIRMED_PG_CONTAINER_ID" \
  --expected-image-id "$CONFIRMED_PG_IMAGE_ID" \
  --compose-project "$CONFIRMED_COMPOSE_PROJECT" \
  --database "$CONFIRMED_DATABASE" \
  --user "$CONFIRMED_DATABASE_ROLE" \
  --postgres-major "$CONFIRMED_PG_MAJOR" \
  --socket-directory "$CONFIRMED_CONTAINER_SOCKET_DIRECTORY" \
  --port "$CONFIRMED_PG_PORT" \
  --destination "$APPROVED_PRIVATE_BACKUP_DIRECTORY"
```

It verifies the container's exact ID/image/project/running state, tool majors,
current database/user/server major, read-only session guard, database size and
host filesystem capacity. It reads no application table rows. It requires
`docker`, Python 3 and PostgreSQL client tools plus `env -u` and GNU `timeout` inside the
confirmed PostgreSQL container. Missing tools, malformed responses, failed
authentication and insufficient privileges fail closed. The database role must
be able to dump the database and global objects; a globals failure does not
produce a complete manifest.

Default stdout contains only a success summary, not internal database metadata.

Every database connection pins the container-local Unix socket directory and
port explicitly. Inherited `PGHOSTADDR` and service-file selection are cleared,
so an unrelated PostgreSQL server cannot be selected by those environment
defaults. The helper does not discover or guess socket paths.
Only a single simple absolute socket path is accepted, never a comma-separated
libpq host list.

## Explicit future execution

Only after the storage decision and operator authorization, add `--execute` to
the same command. There is no workflow that invokes it automatically. A unique
`0700` bundle directory is created under the selected destination. Targets and
space are checked again immediately before export.

The tool writes these `0600` files:

- `database.dump`: the whole selected database, PostgreSQL custom format.
- `globals.sql`: cluster roles/tablespaces and other global definitions. This
  can contain password hashes and infrastructure metadata. Treat it as secret.
- `toc.list`: `pg_restore --list` output from that exact archive.
- `SHA256SUMS`: checksums of the three files above.
- `manifest.json`: source identifiers/versions, sizes, timestamps and checksums;
  its private temporary file is flushed and synced before an atomic rename,
  with `complete:true` and **`restore_verified:false`**. Directory durability
  checks follow publication; detected failures/interruption remove the marker.
- Bounded `command-NN.stderr` diagnostics. These can contain sensitive error
  details, remain private, are never echoed, and retain at most 64 KiB each.

The nonverbose dump/global-export and TOC commands reject any stderr output,
even with exit status zero: PostgreSQL can warn about omitted objects rather
than fail. Any diagnostic/captured-output overflow also fails closed. The
bounded diagnostic remains private; a warning is never treated as a full export.

The archive is streamed directly to a private file; no database contents pass
through terminal output. No raw dump is uploaded or copied elsewhere. Each dump
has a 30-minute default timeout (explicitly adjustable from 30 seconds to one
day), enforced inside the container against that dump process only. Lock waits
are bounded; no other PostgreSQL session or service is stopped or killed.

An incomplete directory is retained privately for diagnosis and never receives
a complete manifest. Do not treat its files as a usable bundle. Do not print its
stderr into public CI or chat. The tool does not claim that a valid TOC or hash
proves restorability, authenticity, or a recovery-time objective.

## Restore-rehearsal handoff

Verify `format_version:1`, `kind:gnomonitoring-postgres-backup`, `complete:true`,
the source major and each size/hash in `manifest.json`. `files` is keyed by
`database.dump`, `globals.sql` and `toc.list`, each with `sha256` and `bytes`.
Hashing detects accidental changes; it does not authenticate an untrusted bundle.

The rehearsal owner restores **only** `database.dump` into a fresh isolated
database with a dedicated rehearsal role, using `--no-owner --no-privileges`
where appropriate. Never automatically apply `globals.sql` to the rehearsal
cluster: it may recreate production roles or filesystem mappings. Keep it for
reviewed disaster recovery only. Confirm extensions, schema, row counts, heights,
permissions and query results, run `ANALYZE`, then exercise the candidate and
rollback binaries in an environment unable to reach production or notification
services. Only a successful, recorded restore and compatibility rehearsal can
change the operational status to a verified backup.

`started_at_utc` and `completed_at_utc` bracket the export; the helper does not
record an exact exported snapshot or a WAL/PITR recovery point. A DB restore can
lose post-snapshot writes. Record the accepted RPO and measured dump/restore
times separately; never infer zero loss from a successful dump.

PostgreSQL documents consistent database dumps, separate global-object backup,
custom archives and client/server compatibility in [pg_dump](https://www.postgresql.org/docs/16/app-pgdump.html),
[pg_dumpall](https://www.postgresql.org/docs/16/app-pg-dumpall.html) and
[pg_restore](https://www.postgresql.org/docs/16/app-pgrestore.html).

## Local validation

Run the fail-closed fixtures without Docker:

```sh
python3 -W error::ResourceWarning -m unittest discover -s scripts/backup -p 'test_*.py' -v
```

The opt-in integration creates only a new synthetic PostgreSQL 16 container with
`--network none`, no published port, an ephemeral data filesystem and a unique
name. It inserts one synthetic table, exercises custom dump/globals/TOC, and
removes that container and the temporary bundle. It reads no production data:

```sh
RUN_SYNTHETIC_POSTGRES_BACKUP_TESTS=1 \
  python3 -W error::ResourceWarning -m unittest discover -s scripts/backup -p 'test_*.py' -v
```

This integration validates archive generation, metadata, hashing and permissions;
it deliberately does not claim a restore rehearsal.
