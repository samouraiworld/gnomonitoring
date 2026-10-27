# Isolated PostgreSQL restore runner

This runner prepares a disposable restore enclave. Development and CI use invented archives only; no production data has been exported, transferred or restored for this preparation. It does not deploy the application, start collectors or bots, publish images, or change the backup bundle's `restore_verified:false` marker.

The default invocation prints `tools_only` and runs no Docker command:

```sh
python3 rehearsal/restore/runner.py
```

## Inputs and explicit execution

Python 3.10 or later and the Docker CLI with Compose are required. Optional InitDB execution also builds the existing pinned Go 1.26.10 helper. The runner accepts an absolute private bundle directory produced by the [backup preparation helper](../../docs/postgres-backup-preparation.md). It validates the complete version-1 manifest, PostgreSQL major 16, exact file sizes and SHA-256 values, `SHA256SUMS`, file types and private ownership/permissions before accepting an archive. Hashes detect changed bytes; they do not authenticate the archive's creator or make its SQL trustworthy.

Restoring an archive executes SQL chosen by its source. Accept only a separately reviewed, trusted bundle. PostgreSQL describes that trust boundary in [pg_restore](https://www.postgresql.org/docs/16/app-pgrestore.html) and the archive and version constraints in [pg_dump](https://www.postgresql.org/docs/16/app-pgdump.html). Runtime network isolation restricts connectivity; it does not turn hostile SQL into safe input.

Execution is an explicit opt-in. The private output directory must already exist, be absolute, empty, owned by the executing user and mode `0700`. These placeholders require a trusted fixture or separately approved input; the example does not authorize a production export or transfer:

```sh
python3 rehearsal/restore/runner.py \
  --execute-restore \
  --bundle /absolute/private/trusted-bundle \
  --private-output-dir /absolute/private/empty-output \
  --timeout-seconds 1800
```

`--docker-socket` may explicitly select the known local Docker Unix socket. TCP Docker endpoints and inherited remote Docker/BuildKit contexts are refused. The runner generates its own project identity and uses a private empty Docker configuration, without registry credentials. It never selects a production container or accepts a target database DSN.

## Disposable restore boundary

The runner uses only fresh, individually named local volumes. Resolved Compose mounts are validated before creation; existing project resources, host binds, external volumes and remote volume drivers are refused. After creation and before startup, actual containers and volumes are inspected against the expected mounts, local driver, user, network and privilege settings. Runtime PostgreSQL and InitDB helpers use `network_mode: none`, no published port, a Unix socket, UID 999, dropped capabilities and `no-new-privileges`.

Only `database.dump` is restored. The archive's `globals.sql` is checked as a bundle member but **never applied**. Roles, tablespaces, original ownership and original grants are not recreated. Restore uses fresh rehearsal ownership with `--no-owner --no-privileges` and stops on SQL errors. The empty-cluster bootstrap establishes independent marker ownership and a restricted application role; the runner does not repurpose the synthetic seeding bootstrap or reset an existing database.

Restore-only execution is distinct from a migration rehearsal. Adding `--run-initdb` invokes the existing source-pinned InitDB helper only after restore and marker validation. Its source provenance, role/marker guards, per-chain aggregate comparisons and reduction semantics are documented in the [InitDB rehearsal preparation](../README.md). The restored baseline is measured before InitDB; the runner does not infer equality with the source database at export time.

## Time, capacity and failure review

`--timeout-seconds` defaults to 1800 and accepts 30 through 86400 seconds for restore and `ANALYZE` operations; it is not an end-to-end recovery time objective. Catalogue/count sessions use a fixed catalogue search path, five-second lock waits and 30-second statement limits. PostgreSQL's restore utility resets its SQL timeouts, so the restore itself is bounded by a timeout process inside the container and a separate client timeout. A regular empty mode-0600 password file prevents implicit password-file access without suppressing libpq warnings. A timeout, interrupted restore, SQL diagnostic, failed guard or unverified cleanup is a failure, not evidence of a valid restore or rollback. Private diagnostics can contain SQL or archive metadata and must remain private. Normal output uses stable stage/error classes and aggregate results; CI uploads no archives, SQL, manifests or private command logs.

The PostgreSQL container is limited to 1 GiB RAM, two CPUs, 128 processes and 128 MiB shared memory; InitDB is limited to 512 MiB RAM, two CPUs and 64 processes. PostgreSQL temporary files are capped at 1 GiB. Its 1 GiB `max_wal_size` is a checkpoint target, **not a hard WAL quota**. After starting the empty server and before importing the archive, the runner checks free space on the actual Docker data volume: at least twice `manifest.source.database_bytes` plus 1 GiB is required. A 1 GiB free-space margin is checked before and after restore. These checks do not reserve capacity or impose a hard volume quota. Per-command private logs are bounded to 1 MiB and metadata capture to 4 MiB.

Budget the restore sandbox separately from backup storage. The compressed dump's size is not a bound on restored data: heap, indexes, temporary files, WAL and logs consume additional Docker storage. The private host output directory and Docker's data filesystem are separate capacity domains. CPU, memory, available Docker disk space and elapsed-time limits must fit the intended rehearsal; a small synthetic pass establishes none of those budgets for a real archive. Review capacity and retention before any separately authorized real-data rehearsal.

The runner attempts cleanup of only the resources it created and verifies their removal. A cleanup failure remains a failure even after successful SQL execution. The private output directory is retained for review and is not deleted as part of Docker cleanup. Review any cleanup failure before retrying; do not broaden deletion to unrelated resources.

| Exit | Meaning |
| --- | --- |
| `0` | Default tools-only mode; or, after explicit execution, restore checks completed and owned-resource cleanup was verified. With `--run-initdb`, no aggregate scope reduction was reported. |
| `3` | InitDB completed with a table or chain/bucket count reduction requiring explicit review. This does not approve the change. |
| `1` | Refusal, execution failure, timeout or unverified cleanup. |
| `130` | Interrupted execution; no completed restore is claimed. |

A restored database is not evidence of full application compatibility, source-snapshot parity, successful rollback, an accepted recovery point objective, or a recovery time objective. It does not prove that writes after the backup snapshot can be recovered. Full application and exact-image rollback rehearsals remain separate work.

## Core validation

The core PR runs only the targeted boundary suite and the default tools-only invocation; it does not require the separate archive-fixture PR:

```sh
python3 -B -W error::ResourceWarning -m unittest discover \
  -s rehearsal/restore -p 'test_runner_boundary.py' -v
python3 -B rehearsal/restore/runner.py
bash -n rehearsal/restore/bootstrap-empty.sh
shellcheck rehearsal/restore/bootstrap-empty.sh
```

Additional opt-in integration validation is delivered separately with invented PostgreSQL archives, rejection fixtures and disposable local resources. It does not use production metadata, credentials or data. The boundary workflow requires only read access to repository contents and has no deployment, registry login, workflow-dispatch or artifact-upload step.
