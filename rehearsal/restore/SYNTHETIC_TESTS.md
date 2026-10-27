# Synthetic archive validation

These tests create their archives at runtime from invented SQL fixtures. They
never consume production data, configuration, credentials or backups. No binary
archive is committed and no image is published.

The fixture producer creates a new PostgreSQL 16.15 container using the pinned
image already used by the isolated rehearsal. It pins the local Docker Unix
socket and uses an empty private Docker credential configuration. The container
is created, inspected and then started with `network:none`, UID 999, all
capabilities dropped, no new privileges, no published port, and temporary data
and socket filesystems. No host data or existing Docker volume is mounted.

The real backup helper from #177 then produces the custom archive, globals,
TOC, checksums and manifest. The source fixture is removed and its absence is
verified before restoration tests begin. All generated files remain in private
local directories; **archives and SQL/private logs must never be uploaded as
GitHub artifacts or pasted into CI output**.

The fixture has two chain IDs sharing a validator address and height, plus an
`archive_probe` table outside InitDB's eight counted tables. That table includes
a named-chain pair, a peer-chain row, an empty chain ID and a NULL chain ID,
check/unique constraints, an index, a view and a sequence. The source globals
contain the artificial `restore_globals_canary` superuser role. The target must
not contain that role or the source owner: globals are not applied and objects
are restored as the isolated non-admin owner.

## Commands

The ordinary command performs three pure fixture-boundary checks and skips
Docker integration without an explicit opt-in:

```sh
python3 -B -W error::ResourceWarning -m unittest discover \
  -s rehearsal/restore -p 'test_synthetic_restore.py' -v
```

To execute the disposable PostgreSQL tests:

```sh
RUN_SYNTHETIC_RESTORE_TESTS=1 \
  python3 -B -W error::ResourceWarning -m unittest discover \
  -s rehearsal/restore -p 'test_synthetic_restore.py' -v
```

Docker must target the known local Unix daemon on a Linux engine. The
`--run-initdb` scenario also builds the existing Go 1.26.10 helper locally; this
is not an application deployment or registry publication. Each restore uses a
fresh private directory and fresh clone identity. No source container or volume
is reused as a destination.

For a local investigation, `SYNTHETIC_RESTORE_PRIVATE_OUTPUT` may name an existing,
empty, canonical, user-owned `0700` directory outside Git. The tests preserve
archives and diagnostics there for private review instead of removing the
ordinary temporary directory. This variable selects only a private test output
location; it never selects an input archive or existing database.

## Expected evidence

- Successful custom-format restore: aggregate rows and chain buckets match the
  invented fixture, including NULL versus empty; constraints, index, view,
  sequence state and adapted ownership are recorded. The marker is independently
  owned and the DSN is private. Target globals canary/source role are absent.
- InitDB is a separate opt-in after restore verification. The first pass changes
  chain A from two daily rows to one, preserving the peer chain, and requires
  deletion review with exit 3. The second pass is idempotent; it does not approve
  the first pass's change.
- A mismatched archive hash, truncated archive with recomputed hashes, reserved
  clone-marker schema and a function requiring superuser restore privileges
  cannot gain a completed restore summary. Negative tests require the expected
  failure stage, so an unrelated earlier failure cannot make them pass.
- A CHECK function sleeps only when running as the isolated restore owner. The
  source fixture remains fast; the 30-second restore process budget must stop
  it before the 60-second sleep finishes. No marker/completed report is allowed.
- Each exercised clone independently verifies that its exact project has no
  remaining containers, volumes or local image tags, including failed restores.
- Successful reports require owned-resource cleanup verification. Failures keep
  diagnostics private and must never be counted as successful restores.

A successful synthetic restore does not prove equivalence to a production
snapshot, production performance, a full application rehearsal or compatibility
with a historical rollback image. The report therefore retains
`source_snapshot_equality_verified:false`.
