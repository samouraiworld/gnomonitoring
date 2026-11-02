# Postgres backup to Scaleway Object Storage

A root cron job on the production server takes a daily logical dump of the
gnomonitoring database, encrypts it with [age](https://age-encryption.org) to
public keys only, uploads it to Scaleway Object Storage and checks the stored
size. A failed run exits non-zero and posts to a Discord webhook; a run that
never starts (cron stopped, server down) cannot, which is what the optional
heartbeat is for. The private keys stay offline with the operators: the server
can write backups but cannot decrypt them.

These files live here, apart from `scripts/backup/` (an operator-run
preparation tool) and `rehearsal/`, because they are installed on the server as
they are.

Relation to `scripts/backup/prepare-postgres-backup.py`: once decrypted, a
backup is the same PostgreSQL custom-format archive as that tool's
`database.dump`, and the restore below follows the same rule (restore only the
dump, `--no-owner --no-privileges`, no global objects). It is not that tool's
bundle: there is no `manifest.json`, `globals.sql` or `toc.list`, so the
`rehearsal/restore` runner does not accept it as is. That tool pins the exact
container and image IDs, which change whenever the container is recreated, so
it is not suited to an unattended daily run.

| File | Role |
| --- | --- |
| `pg-backup-scaleway.sh` | the backup: `pg_dump -Fc` in the container, piped to `age`, upload, size check, notification |
| `pg-backup.env.example` | its configuration, installed as `/etc/gnomonitoring/pg-backup.env` (mode 0600) |
| `gnomonitoring-pg-backup.cron` | the daily schedule, installed as `/etc/cron.d/gnomonitoring-pg-backup` |
| `pg-row-counts.sh` | exact row count of every table, read-only, for the restore check |
| `pg-restore-test.sh` | decrypt, restore into a throwaway `postgres:16` container, compare row counts |
| `e2e-test.sh` | local end-to-end test of all of the above (Docker, MinIO, loopback only) |

What a run does, step by step (the step name appears in the failure message):

1. `config`: reads the env file as data (never executed). It must be owned by
   root with mode 0600. Errors are reported after the webhook is read, so a
   typo in the file is notified.
2. `lock`: `flock` on `/run/gnomonitoring-pg-backup.lock`; an overlapping run fails.
3. `preflight`: tools present, age recipients valid, container running,
   database reachable, bucket reachable, enough free space in `WORK_DIR`
   (database size plus 1 GiB by default).
4. `dump`: `docker exec gnomonitoring-postgres timeout <DUMP_TIMEOUT> pg_dump -Fc | age -R recipients`
   into `WORK_DIR`. Only ciphertext touches the disk. On any failure the dump's
   database session is terminated, so no orphaned `pg_dump` keeps its locks.
5. `upload`: `aws s3 cp` to `s3://gnomonitoring/postgres/gnomonitoring-<UTC timestamp>.dump.age`.
6. `verify`: `aws s3api head-object`; the stored size must equal the local size.
7. `notify`: optional success message and heartbeat. The temporary directory is always removed.

## Install on the server

Run every server command as root (`sudo -i`). Copy the files from a checkout
of the reviewed branch on your workstation; the server needs no repository.

### 1. Read-only checks

```sh
aws --version
aws --endpoint-url https://s3.fr-par.scw.cloud --region fr-par s3 ls s3://gnomonitoring/
```

The first prints `aws-cli/<version> ...`; the second lists the old SQLite
`.tar.gz` backups (no error such as `AccessDenied` or `NoSuchBucket`).

```sh
docker exec gnomonitoring-postgres pg_dump --version
docker exec gnomonitoring-postgres psql -U gnomonitoring -d gnomonitoring -XAtc \
  "SELECT pg_size_pretty(pg_database_size('gnomonitoring'))"
df -h /var/tmp
```

Expect `pg_dump (PostgreSQL) 16.x`, a size such as `5432 MB`, and more free
space on `/var/tmp` than that size plus 1 GiB.

```sh
command -v aws docker age flock curl
timedatectl | grep 'Time zone'
aws --endpoint-url https://s3.fr-par.scw.cloud --region fr-par s3api get-bucket-lifecycle-configuration --bucket gnomonitoring
aws --endpoint-url https://s3.fr-par.scw.cloud --region fr-par s3api get-bucket-versioning --bucket gnomonitoring
aws configure get default.s3.multipart_chunksize
```

- `command -v` prints one path per tool; `age` may be missing (installed in
  step 3). Every path must be under `/usr/local/sbin`, `/usr/local/bin`,
  `/usr/sbin`, `/usr/bin`, `/sbin` or `/bin`, the only directories cron
  searches. A tool under `/snap/bin` or `/root/.local/bin` works in your shell
  but not from cron: say so before going on.
- Note the time zone for the cron line.
- The lifecycle command answers `NoSuchLifecycleConfiguration` when the bucket
  has no rule yet. If it prints rules, keep them for step 8.
- Versioning prints nothing, or `"Status": "Suspended"` / `"Enabled"`.
- Scaleway accepts at most 1,000 parts per multipart upload. The aws CLI
  default part size (8 MB) therefore caps one object at about 8 GB. If the
  database is larger than a few GB, raise it once:
  `aws configure set default.s3.multipart_chunksize 64MB` (with
  `--profile <name>` instead of `default.` if you set `AWS_PROFILE`).

### 2. Key pairs, on each operator's workstation

The private keys never go to the server. Each person who must be able to
restore (today: Lours and zxxma) generates their own pair; only public keys
are shared. Two recipients mean that losing one private key loses nothing.

```sh
sudo dnf install age        # or: apt install age / brew install age
age-keygen -o gnomonitoring-backup-key.txt
```

It prints `Public key: age1...`. Store `gnomonitoring-backup-key.txt` in the
team's Citadelle vault, then delete the local copy; fetch it back only for a
restore. Losing every private key makes every backup unreadable. A
passphrase-protected variant works too:
`age-keygen | age -p -a > gnomonitoring-backup-key.age` (age then asks for the
passphrase at each restore).

### 3. Install the files

On the workstation, in the checkout:

```sh
cd ops/backup
sha256sum pg-backup-scaleway.sh pg-row-counts.sh gnomonitoring-pg-backup.cron > SHA256SUMS
cat SHA256SUMS
ssh <server> 'mkdir -m 0700 pg-backup-install'
scp pg-backup-scaleway.sh pg-row-counts.sh gnomonitoring-pg-backup.cron SHA256SUMS <server>:pg-backup-install/
rm SHA256SUMS
```

`pg-backup-install` is a private directory in your home on the server, not
`/tmp`, so nobody else can swap a file before root installs it. On the
server, as root, with `<user>` your SSH login:

```sh
cd ~<user>/pg-backup-install && sha256sum -c SHA256SUMS && cat SHA256SUMS
```

Each file prints `OK`, and the hashes must equal the workstation's. Then, in
the same directory:

```sh
apt install -y age            # only if step 1 found no age
install -m 0755 -o root -g root pg-backup-scaleway.sh pg-row-counts.sh /usr/local/sbin/
install -d -m 0700 -o root -g root /etc/gnomonitoring
# The two lines below never overwrite existing files (safe on an upgrade).
[ -e /etc/gnomonitoring/pg-backup.env ] || install -m 0600 -o root -g root /dev/null /etc/gnomonitoring/pg-backup.env
[ -e /etc/gnomonitoring/pg-backup.recipients ] || install -m 0644 -o root -g root /dev/null /etc/gnomonitoring/pg-backup.recipients
nano /etc/gnomonitoring/pg-backup.recipients
nano /etc/gnomonitoring/pg-backup.env
```

The cron file stays in `pg-backup-install` until step 7.

- `pg-backup.recipients` holds the `age1...` public keys, one per line (lines
  starting with `#` are comments).
- `pg-backup.env` needs one line,
  `DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/...`. Every other key
  has a working default (see `pg-backup.env.example`). Type the webhook in the
  editor only: on a command line it would stay in the shell history.

### 4. First run

Run the check in the same bare environment as cron, so that a tool cron cannot
find fails now rather than at 03:30:

```sh
env -i HOME=/root PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  /usr/local/sbin/pg-backup-scaleway.sh --check
```

It prints `encrypting to 2 age recipient(s), ...`, then ends with
`pre-flight OK: bucket gnomonitoring reachable, ... MiB free in /var/tmp` and
`check only: no dump taken`. Then the real run, followed at once by the row
counts:

```sh
time env -i HOME=/root PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  /usr/local/sbin/pg-backup-scaleway.sh
pg-row-counts.sh > /root/pg-row-counts-$(date -u +%Y%m%d).tsv
```

The backup ends with `verified: stored object is N bytes` and
`backup OK: s3://gnomonitoring/postgres/gnomonitoring-<timestamp>.dump.age`:
note that key for the restore test. The row counts are the reference for the
restore test; they hold table names and numbers only. Copy them to the
workstation with `scp <server>:/root/pg-row-counts-*.tsv .` (or through your
home directory).

### 5. Force one failure

```sh
install -m 0600 /etc/gnomonitoring/pg-backup.env /root/pg-backup-fail.env
echo 'PG_CONTAINER=gnomonitoring-missing' >> /root/pg-backup-fail.env
pg-backup-scaleway.sh --config /root/pg-backup-fail.env; echo "exit status $?"
rm -f /root/pg-backup-fail.env
```

It prints `ERROR: container gnomonitoring-missing is not running` and
`exit status 1`, and the Discord channel receives
`gnomonitoring Postgres backup FAILED ... step: preflight`.

### 6. Heartbeat (recommended)

The Discord message only covers a run that starts. To be told when no backup
ran at all, create a check on a dead-man's-switch service (for example
healthchecks.io: period 1 day, grace 2 hours, its alert sent to the same
Discord channel), then add its ping URL to `pg-backup.env`:
`HEARTBEAT_URL=https://hc-ping.com/...` (with the editor, it is a credential
too). The next successful run pings it.

### 7. Schedule

```sh
install -m 0644 -o root -g root ~<user>/pg-backup-install/gnomonitoring-pg-backup.cron \
  /etc/cron.d/gnomonitoring-pg-backup
```

The line runs at 03:30 server time; edit `/etc/cron.d/gnomonitoring-pg-backup`
if step 1 showed a time zone other than UTC. The next day, check the run:

```sh
journalctl -t gnomonitoring-pg-backup --since yesterday
```

### 8. Retention

A lifecycle rule deletes the backups after 30 days and cleans up the parts of
an upload interrupted midway. It only matches the `postgres/` prefix, so the
old SQLite backups at the bucket root are kept.

**Warning:** `put-bucket-lifecycle-configuration` replaces every lifecycle rule
of the bucket. If step 1 printed existing rules, add them to the `Rules` list
first. If step 1 showed versioning `Enabled`, stop: expiring a versioned object
only hides it, and the rule needs a different shape.

```sh
cd ~<user>/pg-backup-install
cat > lifecycle.json <<'EOF'
{"Rules": [{"ID": "gnomonitoring-postgres-30d", "Status": "Enabled",
            "Filter": {"Prefix": "postgres/"}, "Expiration": {"Days": 30},
            "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 1}}]}
EOF
aws --endpoint-url https://s3.fr-par.scw.cloud --region fr-par s3api put-bucket-lifecycle-configuration \
  --bucket gnomonitoring --lifecycle-configuration file://lifecycle.json
aws --endpoint-url https://s3.fr-par.scw.cloud --region fr-par s3api get-bucket-lifecycle-configuration --bucket gnomonitoring
```

The last command prints the rule back. The `pg-backup-install` directory can
then be removed.

## Restore test, on a workstation

Needs Docker, age, and either the aws CLI with read access to the bucket or a
`.dump.age` file downloaded from the Scaleway console. Downloads go to
`$TMPDIR` (default `/tmp`, a small tmpfs on Fedora): set `TMPDIR` to a disk
with room for one backup. Run from a checkout, with the key noted in step 4:

```sh
ops/backup/pg-restore-test.sh --identity gnomonitoring-backup-key.txt \
  --s3-key postgres/gnomonitoring-<timestamp>.dump.age \
  --source-counts pg-row-counts-<date>.tsv --allow-drift
# or, with a downloaded file:
ops/backup/pg-restore-test.sh --identity gnomonitoring-backup-key.txt \
  --file gnomonitoring-<timestamp>.dump.age --source-counts pg-row-counts-<date>.tsv --allow-drift
```

It starts a `postgres:16` container with no network, streams `age -d` into
`pg_restore --exit-on-error`, prints one line per table (source, restored,
delta, status) and ends with `restore OK: <tables> tables, <rows> rows, ...`.
The container and its data volume are removed at the end (`--keep` leaves it
running for inspection). `--latest` instead of `--s3-key` picks the newest
backup, which no longer matches counts taken on an earlier day.

`--allow-drift` is needed against production: the counts were taken right
after the backup run while the backend kept writing, so tables such as
`daily_participations` and `alert_logs` can differ slightly (`drift`). A
difference is accepted up to 10 rows or 1% of the source count, whichever is
larger (`--max-drift-pct` widens it). A larger difference, a table missing on
either side, or an empty restored table fails. Without `--allow-drift` every
count must match exactly.

The restored database holds personal data (users, alert contacts, webhook
URLs): run the test on a trusted machine and let the script remove the
container. If the removal fails the script says so and exits 1.

## Disaster recovery

Not rehearsed against production; follow it step by step. The current
database is kept under another name until the restored one is checked, and
the plaintext dump never touches a disk: it flows from `age` on the
workstation through SSH into the container.

1. Run the restore test above on the exact backup file first. A backup is only
   as trustworthy as the bucket: anyone holding the server's aws credentials
   could also have written a file there.
2. On the server, find and stop the backend so that nothing writes:

   ```sh
   docker ps --format '{{.Names}}' | grep gnomonitoring
   docker stop <backend container>     # gnomonitoring-backend or gnomonitoring-backend-2
   ```

   If the Postgres volume itself was lost, start the `postgres` service from
   `docker-compose-prod.yml` first: it creates an empty `gnomonitoring`
   database.
3. Create an empty database for the restore, on the server:

   ```sh
   docker exec gnomonitoring-postgres createdb -U gnomonitoring gnomonitoring_restore
   ```

4. Restore into it from the workstation (SSH as root, or as a user in the
   `docker` group: `sudo` cannot prompt here, stdin carries the dump):

   ```sh
   set -o pipefail
   age -d -i gnomonitoring-backup-key.txt gnomonitoring-<timestamp>.dump.age |
     ssh root@<server> 'docker exec -i gnomonitoring-postgres pg_restore -U gnomonitoring \
       -d gnomonitoring_restore --no-owner --no-privileges --single-transaction --exit-on-error' &&
     echo RESTORE OK
   ```

   `--single-transaction` makes it all or nothing: without `RESTORE OK`,
   `gnomonitoring_restore` is left empty; drop it and start again.
5. Check it on the server: `pg-row-counts.sh --db gnomonitoring_restore`.
6. Swap the databases, keeping the old one:

   ```sh
   docker exec gnomonitoring-postgres psql -U gnomonitoring -d postgres -v ON_ERROR_STOP=1 \
     -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'gnomonitoring'" \
     -c 'ALTER DATABASE gnomonitoring RENAME TO gnomonitoring_before_restore' \
     -c 'ALTER DATABASE gnomonitoring_restore RENAME TO gnomonitoring'
   ```

7. Start the backend again (`docker start <backend container>`) and check its
   logs. Data written after the backup's timestamp is lost; the backend
   re-syncs blocks from the chain. Once everything works, drop the old copy:
   `docker exec gnomonitoring-postgres dropdb -U gnomonitoring gnomonitoring_before_restore`.

## Hardening, later

- Give the server a dedicated Scaleway IAM application whose policy can write
  and read `postgres/` objects but not delete them, and consider object lock:
  today root's aws credentials could also delete or replace the backups.
- The heartbeat of step 6 is the only alert when the server or cron is down.

## Local end-to-end test

```sh
ops/backup/e2e-test.sh               # 1,000,000 synthetic daily_participations rows
E2E_ROWS=10000000 ops/backup/e2e-test.sh
```

It needs Docker, Go, age, the aws CLI, curl, python3 and flock. Docker may
pull `postgres:16` and `minio/minio`, and Go may download modules; the test
itself only talks to the loopback and reads no production data. It uses a
`postgres:16` schema created by the backend's own `database.InitDB`, MinIO as
the object store and a local listener as the Discord webhook. It checks the
pre-flight, a backup run, the ciphertext and its size, the empty work
directory, the notifications, an exact restore, the drift tolerance, a wrong
key, and that these fail and notify: a missing container, an overlapping run,
a world-readable config, an unknown config key, a malformed recipient, and a
dump timeout (which must leave no `pg_dump` session behind). Run shellcheck
with:

```sh
docker run --rm -v "$PWD/ops/backup:/mnt:ro" koalaman/shellcheck:stable \
  /mnt/pg-backup-scaleway.sh /mnt/pg-row-counts.sh /mnt/pg-restore-test.sh /mnt/e2e-test.sh
```

## Troubleshooting

- `upload` fails with a checksum or `x-amz-checksum` error after an aws CLI
  upgrade: aws CLI 2.23+ adds integrity checksums by default. Set
  `request_checksum_calculation = when_required` and
  `response_checksum_validation = when_required` in root's `~/.aws/config`.
- `upload` fails on a large object with a part-count error: raise
  `s3.multipart_chunksize` (see the read-only checks).
- `lock` fails: an earlier run is still going (`pgrep -af pg-backup-scaleway`).
- `dump` fails with `exceeded DUMP_TIMEOUT` or a lock timeout: a schema change
  or a long transaction blocked `pg_dump`; the next run retries.
- A warning about stale `gnomonitoring-pg-backup.*` directories: a run was
  killed before its cleanup; remove those directories from `WORK_DIR`.
