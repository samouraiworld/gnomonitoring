#!/usr/bin/env bash
# Local end-to-end test of pg-backup-scaleway.sh and pg-restore-test.sh.
#
# Everything is disposable and local: a postgres:16 container whose schema is
# created by the backend's own database.InitDB and filled with synthetic rows,
# a MinIO container standing in for Scaleway Object Storage, a throwaway age
# key pair and a local HTTP listener standing in for the Discord webhook.
# The test itself only talks to the loopback and reads no production data;
# Docker may pull PostgreSQL/build base images; MinIO is built from pinned
# official source and Go may download modules. No production image is published.
#
# Checks: a pre-flight check, a real backup run, the object is age ciphertext
# with the verified size, no file is left in the work directory, the success
# notification, an exact restore (row counts equal the source) that ignores a
# foreign object name, the drift tolerance, a wrong key fails. These failures
# all notify and exit non-zero: a missing container, an overlapping run (lock),
# a world-readable config file, an unknown config key, a malformed recipient,
# and a dump timeout, which must leave no pg_dump session behind.
#
# Usage:
#   ops/backup/e2e-test.sh
#
#   E2E_ROWS  synthetic daily_participations rows (default 1000000)
#
# Needs docker, go, age, age-keygen, aws, curl, python3 and flock.
set -euo pipefail
umask 077

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
backend=$(cd -- "$here/../../backend" && pwd)
rows=${E2E_ROWS:-1000000}
[[ $rows =~ ^[0-9]+$ ]] || {
	echo "E2E_ROWS must be an integer" >&2
	exit 2
}

run_id="w17e2e$$"
pg="gnomonitoring-backup-e2e-pg-$$"
s3="gnomonitoring-backup-e2e-s3-$$"
minio_image="gnomonitoring-backup-minio-e2e:$run_id"
tmp=$(mktemp -d)
stub_pid=
lock_pid=
failures=0

log() { printf '\n==> %s\n' "$*"; }
pass() { printf 'PASS %s\n' "$*"; }
fail() {
	printf 'FAIL %s\n' "$*"
	failures=$((failures + 1))
}

cleanup() {
	local status=$?
	trap - EXIT
	[[ -z $stub_pid ]] || kill "$stub_pid" 2>/dev/null || true
	[[ -z $lock_pid ]] || kill "$lock_pid" 2>/dev/null || true
	docker rm -f -v "$pg" "$s3" >/dev/null 2>&1 || true
	docker image rm "$minio_image" >/dev/null 2>&1 || true
	rm -rf -- "$tmp"
	exit "$status"
}
trap cleanup EXIT

for tool in docker go age age-keygen aws curl python3 flock; do
	command -v "$tool" >/dev/null || {
		echo "required tool not found: $tool" >&2
		exit 2
	}
done

wait_for() {
	local what=$1
	shift
	for _ in $(seq 1 90); do
		if "$@" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	echo "$what did not become ready" >&2
	exit 1
}

log "building the pinned-source S3 fixture"
docker build -t "$minio_image" -f "$here/Dockerfile.minio-e2e" "$here"

log "starting disposable Postgres ($pg) and MinIO ($s3)"
docker run -d --name "$pg" -p 127.0.0.1::5432 \
	-e POSTGRES_USER=gnomonitoring -e POSTGRES_PASSWORD="$run_id" -e POSTGRES_DB=gnomonitoring \
	postgres:16 >/dev/null
docker run -d --name "$s3" -p 127.0.0.1::9000 \
	-e MINIO_ROOT_USER="$run_id" -e MINIO_ROOT_PASSWORD="$run_id-secret" -e MINIO_REGION=fr-par \
	"$minio_image" server /data >/dev/null
pg_port=$(docker port "$pg" 5432/tcp | head -n 1 | sed 's/.*://')
s3_port=$(docker port "$s3" 9000/tcp | head -n 1 | sed 's/.*://')
wait_for Postgres docker exec "$pg" pg_isready -q -h 127.0.0.1 -U gnomonitoring -d gnomonitoring
wait_for MinIO curl -fsS "http://127.0.0.1:$s3_port/minio/health/live"

# The aws CLI must see only the MinIO credentials, never the user's ~/.aws.
export AWS_ACCESS_KEY_ID=$run_id AWS_SECRET_ACCESS_KEY=$run_id-secret
export AWS_CONFIG_FILE=$tmp/aws-config AWS_SHARED_CREDENTIALS_FILE=$tmp/aws-credentials
unset AWS_PROFILE AWS_SESSION_TOKEN
: >"$AWS_CONFIG_FILE"
: >"$AWS_SHARED_CREDENTIALS_FILE"
endpoint="http://127.0.0.1:$s3_port"
aws --endpoint-url "$endpoint" --region fr-par s3 mb s3://gnomonitoring-e2e >/dev/null

log "creating the schema with the backend's database.InitDB"
# The helper only exists in a go build overlay: nothing is written to the
# repository, and the package still sits inside the backend module, so it may
# import internal/database.
mkdir -p "$tmp/initdb"
cat >"$tmp/initdb/main.go" <<'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

func main() {
	if _, err := database.InitDB(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
EOF
printf '{"Replace":{"%s/_pgbackup_e2e_initdb/main.go":"%s/initdb/main.go"}}' "$backend" "$tmp" >"$tmp/initdb/overlay.json"
(cd "$backend" && go build -overlay="$tmp/initdb/overlay.json" -o "$tmp/initdb/initdb" ./_pgbackup_e2e_initdb)
if ! "$tmp/initdb/initdb" "host=127.0.0.1 port=$pg_port user=gnomonitoring password=$run_id dbname=gnomonitoring sslmode=disable" \
	>"$tmp/initdb/log" 2>&1; then
	cat "$tmp/initdb/log" >&2
	exit 1
fi

log "seeding synthetic rows ($rows daily_participations)"
started=$SECONDS
docker exec -i "$pg" psql -X -q -v ON_ERROR_STOP=1 -v rows="$rows" -U gnomonitoring -d gnomonitoring <<'SQL'
INSERT INTO users (user_id, nameuser, email, created_at)
SELECT 'user_' || i, 'User ' || i, 'user' || i || '@example.invalid', now() FROM generate_series(1, 40) i;
INSERT INTO hour_reports (user_id, daily_report_hour, daily_report_minute, timezone)
SELECT 'user_' || i, 9, 0, 'Europe/Paris' FROM generate_series(1, 40) i;
INSERT INTO webhook_validators (created_at, description, user_id, url, type, chain_id)
SELECT now(), 'hook ' || i, 'user_' || (i % 40 + 1), 'https://discord.invalid/api/webhooks/' || i,
       CASE WHEN i % 2 = 0 THEN 'discord' ELSE 'slack' END, 'betanet' FROM generate_series(1, 60) i;
INSERT INTO webhook_gov_daos (created_at, description, user_id, url, type, last_checked_id, chain_id)
SELECT now(), 'gov ' || i, 'user_' || (i % 40 + 1), 'https://discord.invalid/api/webhooks/g' || i, 'discord', i, 'betanet'
FROM generate_series(1, 30) i;
INSERT INTO alert_contacts (created_at, user_id, moniker, namecontact, mention_tag, id_webhook)
SELECT now(), 'user_' || (i % 40 + 1), 'val' || (i % 50), 'contact ' || i, '&' || (100000 + i), i % 60 + 1
FROM generate_series(1, 80) i;
INSERT INTO addr_monikers (chain_id, addr, moniker, first_active_block, voting_power)
SELECT 'betanet', 'g1val' || i, 'val' || i, 1, 1000 + i FROM generate_series(0, 49) i;
INSERT INTO daily_participations (date, block_height, chain_id, moniker, addr, participated, tx_contribution,
                                  proposed, precommit_lag_ms, late_for_quorum)
SELECT now() - make_interval(secs => (:rows - i) * 0.1), i / 50 + 1, 'betanet', 'val' || (i % 50), 'g1val' || (i % 50),
       i % 97 <> 0, i % 13 = 0, i % 50 = (i / 50) % 50, (i % 400)::bigint, i % 7 = 0
FROM generate_series(0, :rows - 1) i;
INSERT INTO daily_participation_agregas (chain_id, addr, block_date, moniker, participated_count, missed_count,
       tx_contribution_count, total_blocks, first_block_height, last_block_height, proposed_count,
       late_for_quorum_count, late_for_quorum_samples, precommit_lag_p50_ms, precommit_lag_p90_ms)
SELECT 'betanet', 'g1val' || v, to_char(date '2026-01-01' + d, 'YYYY-MM-DD'), 'val' || v, 17000, 280, 900, 17280,
       d * 17280, d * 17280 + 17279, 345, 1200, 17000, 120.5, 340.25
FROM generate_series(0, 49) v, generate_series(0, 299) d;
INSERT INTO alert_logs (chain_id, addr, moniker, level, start_height, end_height, skipped, msg, sent_at)
SELECT 'betanet', 'g1val' || (i % 50), 'val' || (i % 50), (ARRAY['WARNING', 'CRITICAL', 'RESOLVED'])[i % 3 + 1],
       i * 100, i * 100 + 30, true, 'synthetic alert ' || i, now() - make_interval(mins => i)
FROM generate_series(1, 5000) i;
INSERT INTO govdaos (id, chain_id, url, title, tx, status, status_synced)
SELECT i, 'betanet', 'https://gno.invalid/r/gov/dao:' || i, 'Proposal ' || i, 'tx' || i, 'ACCEPTED', true
FROM generate_series(0, 120) i;
INSERT INTO telegrams (chat_id, type, chain_id, chat_title)
SELECT -1000000 - i, CASE WHEN i % 2 = 0 THEN 'govdao' ELSE 'validator' END, 'betanet', 'chat ' || i
FROM generate_series(1, 20) i;
INSERT INTO telegram_hour_reports (chat_id, chain_id, daily_report_hour, daily_report_minute, activate, timezone)
SELECT -1000000 - i, 'betanet', 8, 30, true, 'UTC' FROM generate_series(1, 20) i;
INSERT INTO telegram_validator_subs (chat_id, chain_id, moniker, addr, activate, created_at)
SELECT -1000000 - (i % 20 + 1), 'betanet', 'val' || (i % 50), 'g1val' || (i % 50), true, now()
FROM generate_series(1, 200) i ON CONFLICT DO NOTHING;
ANALYZE;
SQL
db_size=$(docker exec "$pg" psql -X -A -t -q -U gnomonitoring -d gnomonitoring \
	-c "SELECT pg_size_pretty(pg_database_size('gnomonitoring'))")
echo "seeded in $((SECONDS - started)) s, database size $db_size"
"$here/pg-row-counts.sh" --container "$pg" >"$tmp/source.tsv"
cat "$tmp/source.tsv"

log "starting the Discord webhook stub"
mkdir -p "$tmp/discord"
cat >"$tmp/stub.py" <<'EOF'
import http.server, itertools, json, pathlib, sys
out = pathlib.Path(sys.argv[1])
seq = itertools.count(1)
class Hook(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        json.loads(body)  # a malformed payload fails the request
        (out / f"{next(seq):03d}.json").write_bytes(body)
        self.send_response(204)
        self.end_headers()
    def log_message(self, *args):
        pass
server = http.server.HTTPServer(("127.0.0.1", 0), Hook)
(out / "port").write_text(str(server.server_port))
server.serve_forever()
EOF
python3 "$tmp/stub.py" "$tmp/discord" &
stub_pid=$!
wait_for "webhook stub" test -s "$tmp/discord/port"
hook_url="http://127.0.0.1:$(cat "$tmp/discord/port")/api/webhooks/e2e/token"
messages() { find "$tmp/discord" -name '*.json' | wc -l; }
last_message() { find "$tmp/discord" -name '*.json' | sort | tail -n 1 | xargs cat; }

log "generating a throwaway age key pair"
age-keygen -o "$tmp/key.txt" 2>/dev/null
age-keygen -y "$tmp/key.txt" >"$tmp/recipients"
chmod 644 "$tmp/recipients"
age-keygen -o "$tmp/other-key.txt" 2>/dev/null

mkdir -p "$tmp/work"
cat >"$tmp/pg-backup.env" <<EOF
PG_CONTAINER=$pg
S3_BUCKET=gnomonitoring-e2e
S3_PREFIX=postgres
S3_ENDPOINT=$endpoint
AGE_RECIPIENTS_FILE=$tmp/recipients
DISCORD_WEBHOOK_URL="$hook_url"
NOTIFY_ON_SUCCESS=true
WORK_DIR=$tmp/work
LOCK_FILE=$tmp/backup.lock
EOF
chmod 600 "$tmp/pg-backup.env"
backup=("$here/pg-backup-scaleway.sh" --config "$tmp/pg-backup.env")

log "pre-flight check"
if "${backup[@]}" --check; then pass "--check succeeds"; else fail "--check failed"; fi
if [[ $(messages) == 0 ]]; then pass "--check sends nothing"; else fail "--check sent a notification"; fi

log "backup run"
started=$SECONDS
if "${backup[@]}"; then pass "backup run succeeds"; else fail "backup run failed"; fi
backup_seconds=$((SECONDS - started))
mapfile -t keys < <(aws --endpoint-url "$endpoint" --region fr-par s3api list-objects-v2 \
	--bucket gnomonitoring-e2e --query 'Contents[].Key' --output text | tr '\t' '\n')
if [[ ${#keys[@]} == 1 && ${keys[0]} =~ ^postgres/gnomonitoring-[0-9]{8}T[0-9]{6}Z\.dump\.age$ ]]; then
	pass "one object ${keys[0]}"
else
	fail "unexpected objects: ${keys[*]}"
fi
aws --endpoint-url "$endpoint" --region fr-par s3 cp --only-show-errors "s3://gnomonitoring-e2e/${keys[0]}" "$tmp/fetched.age"
dump_bytes=$(stat -c %s "$tmp/fetched.age")
if [[ $(head -c 21 "$tmp/fetched.age") == age-encryption.org/v1 ]]; then
	pass "object is age ciphertext"
else
	fail "object is not age ciphertext"
fi
if [[ -z $(ls -A "$tmp/work") ]]; then pass "work directory left empty"; else fail "work directory not empty"; fi
if [[ $(messages) == 1 ]] && last_message | grep -q 'backup OK'; then
	pass "success notification sent"
else
	fail "success notification missing"
fi

# A foreign name under the prefix must not be taken for the latest backup.
printf 'not a backup' | aws --endpoint-url "$endpoint" --region fr-par s3 cp --only-show-errors - \
	s3://gnomonitoring-e2e/postgres/zzz.dump.age

log "restore into a throwaway container, exact comparison"
started=$SECONDS
if "$here/pg-restore-test.sh" --identity "$tmp/key.txt" --latest --bucket gnomonitoring-e2e \
	--endpoint "$endpoint" --source-counts "$tmp/source.tsv"; then
	pass "restore matches the source row counts"
else
	fail "restore check failed"
fi
restore_seconds=$((SECONDS - started))

log "drift tolerance: 1% of a large table passes, 20 rows on a small one fails"
awk -F '\t' -v OFS='\t' '$1 == "public.daily_participations" { $2 -= 500 } $1 == "public.users" { $2 += 20 } { print }' \
	"$tmp/source.tsv" >"$tmp/drifted.tsv"
if "$here/pg-restore-test.sh" --identity "$tmp/key.txt" --file "$tmp/fetched.age" \
	--source-counts "$tmp/drifted.tsv" --allow-drift >"$tmp/drift.out" 2>&1; then
	fail "a 20-row difference on users passed"
elif grep -Eq 'public\.daily_participations .* drift$' "$tmp/drift.out" &&
	grep -Eq 'public\.users .* DIFF$' "$tmp/drift.out"; then
	pass "drift within tolerance accepted, beyond it refused"
else
	fail "unexpected drift report: $(grep -E 'daily_participations|users' "$tmp/drift.out")"
fi

log "restore with the wrong key must fail"
if "$here/pg-restore-test.sh" --identity "$tmp/other-key.txt" --file "$tmp/fetched.age" >/dev/null 2>&1; then
	fail "the wrong key restored the backup"
else
	pass "the wrong key is refused"
fi

log "forced failure: missing container"
sed "s/^PG_CONTAINER=.*/PG_CONTAINER=gnomonitoring-e2e-missing/" "$tmp/pg-backup.env" >"$tmp/broken.env"
chmod 600 "$tmp/broken.env"
before=$(messages)
if "$here/pg-backup-scaleway.sh" --config "$tmp/broken.env"; then
	fail "a missing container did not fail the run"
else
	pass "a missing container fails the run"
fi
if [[ $(messages) == $((before + 1)) ]] && last_message | grep -q 'FAILED' && last_message | grep -q 'step: .preflight.'; then
	pass "failure notification sent (step preflight)"
else
	fail "failure notification missing"
fi

log "overlapping run: the lock is held"
# -o: the lock belongs to flock itself, so killing flock releases it.
flock -o "$tmp/backup.lock" sleep 60 &
lock_pid=$!
lock_held() { ! flock -n "$tmp/backup.lock" true; }
wait_for "lock holder" lock_held
before=$(messages)
if "${backup[@]}"; then fail "an overlapping run was not refused"; else pass "an overlapping run is refused"; fi
if [[ $(messages) == $((before + 1)) ]] && last_message | grep -q 'step: .lock.'; then
	pass "failure notification sent (step lock)"
else
	fail "lock notification missing"
fi
kill "$lock_pid" 2>/dev/null || true
lock_pid=

# expect_config_failure NAME CONFIG: the run must fail at step config and notify.
expect_config_failure() {
	local name=$1 config=$2 before
	before=$(messages)
	if "$here/pg-backup-scaleway.sh" --config "$config" --check 2>"$tmp/config.err"; then
		fail "$name was accepted"
	elif [[ $(messages) == $((before + 1)) ]] && last_message | grep -q 'step: .config.'; then
		pass "$name is refused and notified"
	else
		fail "$name: no notification ($(tail -n 1 "$tmp/config.err"))"
	fi
}

log "configuration errors are refused and notified"
chmod 644 "$tmp/pg-backup.env"
expect_config_failure "a 0644 config file" "$tmp/pg-backup.env"
grep -q 'chmod 600' "$tmp/config.err" || fail "unexpected refusal message: $(cat "$tmp/config.err")"
chmod 600 "$tmp/pg-backup.env"
{ cat "$tmp/pg-backup.env"; echo 'NOTIFY_ON_SUCESS=true'; } >"$tmp/typo.env"
chmod 600 "$tmp/typo.env"
expect_config_failure "an unknown config key" "$tmp/typo.env"
echo 'age1qqqqtypo' >"$tmp/bad-recipients"
chmod 644 "$tmp/bad-recipients"
sed "s|^AGE_RECIPIENTS_FILE=.*|AGE_RECIPIENTS_FILE=$tmp/bad-recipients|" "$tmp/pg-backup.env" >"$tmp/bad-recipients.env"
chmod 600 "$tmp/bad-recipients.env"
expect_config_failure "a malformed recipient" "$tmp/bad-recipients.env"

log "dump timeout: pg_dump blocked behind a table lock"
docker exec -d -e PGAPPNAME=e2e-locker "$pg" psql -X -q -U gnomonitoring -d gnomonitoring \
	-c 'BEGIN; LOCK TABLE users IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(120); COMMIT;'
lock_granted() {
	[[ $(docker exec "$pg" psql -X -A -t -q -U gnomonitoring -d gnomonitoring -c \
		"SELECT count(*) FROM pg_locks WHERE relation = 'users'::regclass AND mode = 'AccessExclusiveLock' AND granted") == 1 ]]
}
wait_for "table lock" lock_granted
{ cat "$tmp/pg-backup.env"; echo 'DUMP_TIMEOUT=5s'; } >"$tmp/timeout.env"
chmod 600 "$tmp/timeout.env"
before=$(messages)
if "$here/pg-backup-scaleway.sh" --config "$tmp/timeout.env" 2>"$tmp/timeout.err"; then
	fail "a blocked dump did not time out"
elif grep -q 'exceeded DUMP_TIMEOUT' "$tmp/timeout.err" && [[ $(messages) == $((before + 1)) ]] &&
	last_message | grep -q 'step: .dump.'; then
	pass "a blocked dump times out and is notified (step dump)"
else
	fail "unexpected timeout result: $(tail -n 2 "$tmp/timeout.err")"
fi
sleep 2
left=$(docker exec "$pg" psql -X -A -t -q -U gnomonitoring -d gnomonitoring -c \
	"SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'gnomonitoring-pg-backup-%'")
if [[ $left == 0 ]]; then pass "no pg_dump session left after the timeout"; else fail "$left pg_dump session(s) left"; fi
docker exec "$pg" psql -X -A -t -q -U gnomonitoring -d gnomonitoring -c \
	"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'e2e-locker'" >/dev/null

log "summary"
echo "daily_participations rows: $rows, source database: $db_size"
echo "backup run: ${backup_seconds} s, encrypted object: $dump_bytes bytes ($((dump_bytes / 1024 / 1024)) MiB)"
echo "restore test (download + decrypt + restore + counts): ${restore_seconds} s"
if ((failures > 0)); then
	echo "$failures check(s) FAILED"
	exit 1
fi
echo "all checks passed"
