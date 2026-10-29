# Isolated InitDB rehearsal preparation

These tools prepare a future clone rehearsal. They do not back up, copy or restore production data, start the production application, send bot messages, or publish images. Without `--execute-synthetic`, the runner prints its tools-only state and executes no Docker command.

## Prepared boundary

`backend/cmd/rehearse-initdb` calls the real `database.InitDB` from base revision `92ed63fe7eb5ec54327aff36dc2bcaf2e6f24968`. The Docker build verifies `db_init.go` SHA-256 `b89ab62f218255680065eaca18db81bfe86c53ded9667bb4c839e59eeb43eff9`. Before building, the runner compares the actual bytes of every baseline backend file with its Git blob, and refuses extra tracked, untracked or ignored files anywhere in that build context. Only the five explicitly enumerated rehearsal-command files may be added; their working-tree SHA-256 values are recorded separately in the report. This permits review of an uncommitted helper candidate without representing it as the main application binary. Go is 1.26.10. Changing the database source requires explicitly reviewing and updating the pin.

Before any database connection, the command requires all of:

- `--allow-initdb-on-isolated-clone`, a 32-hex clone nonce and the explicitly supplied private DSN file `/run/rehearsal-postgres/clone.dsn`.
- Linux UID 999, zero effective/permitted capabilities and `NoNewPrivs=1`, with loopback as the only active interface. The closed list of kernel fallback tunnels (`tunl0`, `sit0`, `gre0`, `gretap0`, `erspan0`, `ip6tnl0`, `ip_vti0`, `ip6_vti0`, `ip6gre0`) is permitted only DOWN, with no address and no route. Unknown interfaces, including eth/veth even DOWN, are refused. The provided Compose services both use `network_mode: none`; no TCP port is published and PostgreSQL has `listen_addresses=''`.
- A mode-0600/0400 DSN file owned by that user, fixed non-symlink socket/log mounts and a mode-0700 private log directory.
- An explicit PostgreSQL URL for only `/run/rehearsal-postgres`, database `gnomonitoring_rehearsal_<nonce>` and non-superuser `rehearsal_owner`. TCP, service files, PostgreSQL environment fallbacks, alternate hosts, duplicate/unknown URL options and alternate search paths are refused. The validated target is reconstructed once and passed to InitDB.

The first connection performs only identity/catalog/marker reads. Before reading marker contents, both the ordinary marker table and its schema must be owned by the independent bootstrap role `rehearsal_admin`; the application role may neither own them nor inherit/member/SET ROLE into their owner. It must also lack superuser, create-role, create-database, replication and bypass-RLS flags, marker writes and schema-create privileges. The marker must contain exactly one matching nonce/database/base revision. Revoking an owner's own ACL is insufficient and is explicitly refused. The command never creates a marker itself; only the independently bootstrapped fixture does. It never uses `testoutils.NewTestDB`, whose schema-reset lifecycle is inappropriate for a restored clone.

The host runner requires the known local Docker Unix socket (`~/.docker/run/docker.sock` on macOS or `/var/run/docker.sock` on Linux). It refuses inherited remote Docker/BuildKit contexts, pins `docker --host unix://...` on every command, and uses an empty private Docker/Buildx configuration without user registry credentials. Its project nonce is generated internally. It refuses existing project volumes/containers and validates every resolved mount against the exact fresh-volume source/target/read-only allowlist before creating either container. No bind mounts or external volume drivers are accepted. It then creates PostgreSQL without starting it, inspects the actual network/user/capability/mount settings, starts it and waits for the final PID-1 server's healthcheck; the temporary bootstrap server cannot satisfy readiness. InitDB is likewise created and inspected before starting. Only newly created, explicitly named volumes are eligible for cleanup.

## Synthetic validation

Run unit and boundary tests without a database:

```sh
cd backend
GOTOOLCHAIN=go1.26.10 go test -race ./cmd/rehearse-initdb
cd ..
python3 -B -m unittest discover -s rehearsal -p 'test_*.py' -v
```

To execute only the synthetic fixture, first create an empty absolute directory owned by your user with mode 0700, then explicitly invoke:

```sh
python3 rehearsal/run-synthetic.py --execute-synthetic --private-output-dir /absolute/private/empty-directory
```

The fixture builds local-only helper images, starts a fresh PostgreSQL 16.15 enclave, and seeds invented rows on two chains that share a validator address and block height. It deliberately supplies one pre-activation missed row on `synthetic-chain`; the peer chain's signed row must remain. InitDB backfills the activation height and removes that missed row: the first pass must report exactly one removed row and **exit 3**, requiring deletion review. The second pass must exit 0 with unchanged per-chain counts. A subsequent mismatched synthetic marker must be refused before migration. The runner records these expectations and removes only its own containers, volumes and local image tags.

The fixture PostgreSQL manifest is pinned to the official multi-platform digest `postgres:16@sha256:9e2cf6287f25c3151f4c9421ccce2b17f3e60061c111a29165e0d2cdd9ff0a97`. This is a synthetic test image, not evidence that a previous production application image can restart against a migrated database.

## Private evidence and failure semantics

Only stable error classes and aggregate table counts are written to normal stdout. InitDB/GORM SQL errors and details are captured at file-descriptor level into mode-0600 files in the private clone volume, including loggers initialized before main. The synthetic runner copies these into the requested private output directory before removing its volumes. Do not commit or publish these logs; a future real clone could contain sensitive SQL details.

Exit 1 means refusal or failure. Exit 3 means migration completed but a table or scope count decreased and requires explicit review. Every counted table is reported as present/absent. The seven known multi-chain tables are grouped by `chain_id`; their total is derived by summing those groups. Only `admin_configs` is global according to its model. The `scopes` entries distinguish a named chain (including the empty string), `null_chain_id`, and a legacy table whose `chain_id` column is absent (`legacy_without_chain_id`). No legacy chain assignment is guessed before migration.

`rows_removed` reports net table-count decreases, while `scope_reductions` reports every chain/bucket decrease even if growth on another chain keeps the total unchanged. A bucket change can be chain reclassification rather than a SQL DELETE, so it requires investigation rather than an automatic data-loss conclusion. Only chain identifiers and aggregate counts are reported, never row payloads. A second successful run does not retroactively approve changes reported by the first. Snapshot queries and lock/statement waits are bounded; a timeout does not mean rollback or compatibility succeeded.

## Handoff for a future approved clone

The backup helper's custom-format dump, private manifest, TOC and checksums are inputs to a separately approved restore step. Verify their hashes and PostgreSQL version compatibility, restore into a new fenced database with fresh ownership (`--no-owner --no-privileges`), and establish the marker/DSN only inside that enclave. **Do not apply the archived globals.sql automatically**: it can carry production roles and password hashes. The synthetic bootstrap is not a restore script and must never be pointed at a running or restored database.

No real dump is consumed by this harness today. A full application rehearsal remains a separate phase: use a second disposable clone, an internal-only network, mock RPCs for every copied chain ID, no production tokens/webhooks and no external egress. That phase must observe startup cleanup, retention workers, alerts and API behavior, not merely InitDB. Exact previously deployed image digests must be available for any rollback test; rebuilding an old Git revision is not an equivalent rollback artifact. No full application or old-image rollback result is claimed here.
