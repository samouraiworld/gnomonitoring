#!/usr/bin/env bash
# scenario-govdao-dead-chain.sh - reproduce the 2026-11-09 GovDAO delay
# incident on the devnet and check that it is fixed. Run it through
# 'make scenario-govdao-dead-chain', on a fresh chain ('make dev-up' first).
#
# The incident: pearl-1 was enabled while its RPC endpoints were dead. One
# failed DNS resolution at once, the other accepted connections and never
# answered, so each call waited for the 60s RPC client timeout. The GovDAO
# status watcher checked every proposal of every chain one after the other,
# so pearl-1's 42 proposals cost ~42 min per cycle and gnoland-1's ACCEPTED
# notification went out about an hour late.
#
# Steps:
#   1. Restart the backend with a second enabled chain, "deadnet", whose two
#      RPC endpoints fail the same way: one cannot be resolved, the other is a
#      server that accepts connections and never answers.
#   2. Seed 10 IN PROGRESS proposals for deadnet.
#   3. Create GovDAO proposal #0 on "dev", make sure it is tracked as
#      IN PROGRESS, then vote YES and execute it (scenario1's two halves).
#   4. Wait for the status cycle that sees #0 accepted, and assert that in
#      that cycle:
#      - dev's ACCEPTED notification is logged;
#      - deadnet is given up on after 2 failed fetches (1 "error fetching
#        status" line, then the skip line), not polled 10 times;
#      - dev's notification is logged before deadnet is given up on, so dev
#        did not wait behind the dead chain.
#      It also checks that govdaos stores #0 as ACCEPTED.
#   5. On exit, success or failure: restore the normal backend and delete
#      every deadnet row.
#
# Without the fix, the cycle checks deadnet's 10 proposals at ~60s each, and
# logs no skip line.
set -euo pipefail
cd "$(dirname "$0")"

DEV_COMPOSE="${DEV_COMPOSE:-docker compose -f ../compose_dev_chain.yml --env-file ../.env --env-file .env}"
RPC="${RPC:-http://localhost:26658}"
PG_CONTAINER="${PG_CONTAINER:-gnomonitoring-postgres}"
PG_USER="${PG_USER:-gnomonitoring}"
PG_DB="${PG_DB:-gnomonitoring}"
MAKE="${MAKE:-make}"

BACKEND=gnomonitoring-backend
DEAD_CHAIN=deadnet
DEAD_PROPOSALS=10
# The watcher runs every 5 minutes. After proposal #0 is executed, allow one
# full cycle plus deadnet's two ~60s fetches, with margin.
CYCLE_WAIT_SECONDS="${CYCLE_WAIT_SECONDS:-600}"

WORK_DIR="$PWD/.deadnet"
OVERRIDE="$WORK_DIR/compose.override.yml"
BASE_CONFIG="$PWD/../backend/config_dev_chain.yaml"

psql_q() {
	docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -tAc "$1"
}

# compose runs the dev stack's compose command, word-split on purpose: the
# command and its file arguments come as one string from the Makefile.
compose() {
	# shellcheck disable=SC2086
	$DEV_COMPOSE "$@"
}

# purge_dead_chain deletes every row of the dead chain, from every table that
# has a chain_id column. Only this scenario writes that chain ID.
purge_dead_chain() {
	psql_q "DO \$\$
DECLARE t text;
BEGIN
  FOR t IN
    SELECT c.table_name FROM information_schema.columns c
    JOIN information_schema.tables tb
      ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name
    WHERE c.table_schema = 'public' AND c.column_name = 'chain_id'
      AND tb.table_type = 'BASE TABLE'
  LOOP
    EXECUTE format('DELETE FROM %I WHERE chain_id = %L', t, '$DEAD_CHAIN');
  END LOOP;
END \$\$;" >/dev/null
}

cleanup() {
	local status=$?
	trap - EXIT
	echo ">>> Cleanup: restoring the normal backend and deleting '$DEAD_CHAIN' rows"
	if [ -f "$OVERRIDE" ]; then
		compose -f "$OVERRIDE" rm -sf deadnet-rpc >/dev/null 2>&1 || true
	fi
	compose up -d --no-deps --force-recreate gnomonitoring >/dev/null 2>&1 ||
		echo "    could not recreate the backend: run 'make dev-up'"
	purge_dead_chain || echo "    could not delete the '$DEAD_CHAIN' rows"
	rm -rf "$WORK_DIR"
	exit "$status"
}

fail() {
	echo "❌ FAIL: $*"
	exit 1
}

wait_for() { # wait_for <seconds> <description> <command...>
	local timeout=$1 what=$2
	shift 2
	local deadline=$((SECONDS + timeout))
	until "$@"; do
		[ "$SECONDS" -lt "$deadline" ] || fail "timed out after ${timeout}s waiting for $what"
		sleep 5
	done
}

echo ">>> Preconditions"
docker ps --format '{{.Names}}' | grep -qx "$BACKEND" ||
	fail "$BACKEND is not running: run 'make dev-up' first"
curl -sf "$RPC/status" >/dev/null || fail "the dev chain RPC $RPC does not answer: run 'make dev-up' first"
[ -f "$BASE_CONFIG" ] || fail "$BASE_CONFIG is missing"
python3 -c 'import yaml' 2>/dev/null || fail "python3 with PyYAML is required to write the scenario config"
[ "$(psql_q "SELECT count(*) FROM govdaos WHERE chain_id = 'dev'")" = 0 ] ||
	fail "dev already has GovDAO proposals; this scenario needs proposal #0: run 'make dev-up' first"

trap cleanup EXIT
purge_dead_chain # leftovers of an interrupted run

echo ">>> Step 1: restart the backend with a dead second chain, '$DEAD_CHAIN'"
umask 077 # the scenario config is a copy of the dev config, secrets included
mkdir -p "$WORK_DIR"
python3 - "$BASE_CONFIG" "$WORK_DIR/config.yaml" "$DEAD_CHAIN" <<'PY'
import sys
import yaml

src, dst, chain = sys.argv[1:4]
with open(src) as f:
    cfg = yaml.safe_load(f)
cfg.setdefault("chains", {})[chain] = {
    "rpc_endpoints": [
        # Fails at once: '.invalid' never resolves.
        "http://deadnet-dns.invalid:26657",
        # Accepts the connection and never answers: 60s client timeout.
        "http://deadnet-rpc:26657",
    ],
    "graphqls": ["http://deadnet-dns.invalid:8546/graphql/query"],
    "gnowebs": ["http://deadnet-dns.invalid:8888"],
    "enabled": True,
}
with open(dst, "w") as f:
    yaml.safe_dump(cfg, f, sort_keys=False)
PY
chmod 644 "$WORK_DIR/config.yaml" # read by the backend's non-root user
cat >"$OVERRIDE" <<EOF
services:
  gnomonitoring:
    volumes:
      - $WORK_DIR/config.yaml:/app/config.yaml:ro
  deadnet-rpc:
    # postgres:16 is already pulled for the stack and ships perl. The server
    # accepts every connection, keeps it open and never answers.
    image: postgres:16
    entrypoint: ["perl", "-MIO::Socket::INET", "-e",
      "\$\$s = IO::Socket::INET->new(LocalPort => 26657, Listen => 128, ReuseAddr => 1) or die \$\$!; while (\$\$c = \$\$s->accept) { push @k, \$\$c }"]
EOF
compose -f "$OVERRIDE" up -d --no-deps deadnet-rpc
compose -f "$OVERRIDE" up -d --no-deps --force-recreate gnomonitoring
wait_for 90 "the backend to start monitoring '$DEAD_CHAIN'" \
	sh -c "docker logs $BACKEND 2>&1 | grep -q 'starting monitoring for chain $DEAD_CHAIN'"

echo ">>> Step 2: seed $DEAD_PROPOSALS IN PROGRESS proposals for '$DEAD_CHAIN'"
psql_q "INSERT INTO govdaos (id, chain_id, url, title, tx, status, status_synced)
SELECT g, '$DEAD_CHAIN', 'https://deadnet.invalid/r/gov/dao:' || g, 'Dead-chain proposal #' || g, '', 'IN PROGRESS', true
FROM generate_series(0, $((DEAD_PROPOSALS - 1))) AS g
ON CONFLICT DO NOTHING" >/dev/null

echo ">>> Step 3: create proposal #0 on dev"
"$MAKE" --no-print-directory scenario1-propose
# The websocket inserts the row when it is connected. Insert it ourselves if it
# has not after a short wait, so the scenario does not depend on that timing.
dev_row_present() { [ "$(psql_q "SELECT count(*) FROM govdaos WHERE chain_id = 'dev' AND id = 0")" = 1 ]; }
deadline=$((SECONDS + 30))
until dev_row_present || [ "$SECONDS" -ge "$deadline" ]; do sleep 3; done
psql_q "INSERT INTO govdaos (id, chain_id, url, title, tx, status, status_synced)
VALUES (0, 'dev', 'http://gnoweb:8888/r/gov/dao:0', 'Proposal #0', '', 'IN PROGRESS', true)
ON CONFLICT DO NOTHING" >/dev/null
row=$(psql_q "SELECT status || '|' || status_synced FROM govdaos WHERE chain_id = 'dev' AND id = 0")
[ "$row" = "IN PROGRESS|true" ] || fail "proposal #0 must be tracked as IN PROGRESS (synced) before it is executed, got '$row'"

echo ">>> Step 3b: vote YES and execute proposal #0"
"$MAKE" --no-print-directory scenario1-execute

echo ">>> Step 4: wait for the status cycle that announces #0 (up to ${CYCLE_WAIT_SECONDS}s)"
# evaluate prints "<dev line> <skip line> <deadnet fetch errors> <cycle>" for
# the cycle in which dev's ACCEPTED was logged: line numbers in the backend
# log (0 = not logged yet). The backend was recreated in step 1, so its log
# only holds whole cycles of this run. Cycles never overlap: the watcher
# starts the next one only once every chain of the current one is done.
evaluate() {
	docker logs "$BACKEND" 2>&1 | awk -v dead="$DEAD_CHAIN" '
		index($0, "[govdao] checking proposal statuses") { cycle++; next }
		index($0, "[govdao][dev] proposal 0 (") && / accepted$/ && !dev { dev = NR; devcycle = cycle; next }
		index($0, "[govdao][" dead "] every RPC endpoint failed on") && !skip[cycle] { skip[cycle] = NR; next }
		index($0, "[govdao][" dead "] error fetching status for proposal") { errs[cycle]++ }
		END { print dev + 0, (dev ? skip[devcycle] + 0 : 0), (dev ? errs[devcycle] + 0 : 0), devcycle + 0 }'
}
deadline=$((SECONDS + CYCLE_WAIT_SECONDS))
while :; do
	read -r dev_line skip_line dead_errs cycle < <(evaluate)
	[ "$dev_line" -gt 0 ] && [ "$skip_line" -gt 0 ] && break
	[ "$SECONDS" -lt "$deadline" ] || break
	sleep 10
done

[ "$dev_line" -gt 0 ] || fail "no '[govdao][dev] proposal 0 (...) accepted' line within ${CYCLE_WAIT_SECONDS}s"
[ "$skip_line" -gt 0 ] || fail "'$DEAD_CHAIN' was not given up on in cycle $cycle (no 'every RPC endpoint failed on' line)"
[ "$dev_line" -lt "$skip_line" ] ||
	fail "dev's ACCEPTED (log line $dev_line) waited until '$DEAD_CHAIN' was given up on (log line $skip_line)"
[ "$dead_errs" = 1 ] ||
	fail "'$DEAD_CHAIN' should fail once, then be skipped on the 2nd failure; got $dead_errs fetch errors before the skip"
[ "$(psql_q "SELECT status FROM govdaos WHERE chain_id = 'dev' AND id = 0")" = ACCEPTED ] ||
	fail "govdaos does not store proposal #0 as ACCEPTED"
remaining=$(psql_q "SELECT count(*) FROM govdaos WHERE chain_id = '$DEAD_CHAIN' AND status = 'IN PROGRESS'")
[ "$remaining" = "$DEAD_PROPOSALS" ] || fail "'$DEAD_CHAIN' proposals must stay untouched, $remaining of $DEAD_PROPOSALS are IN PROGRESS"

echo "✅ PASS (cycle $cycle): dev's ACCEPTED went out without waiting for '$DEAD_CHAIN',"
echo "   which was given up on after 2 failed fetches instead of $DEAD_PROPOSALS."
docker logs "$BACKEND" 2>&1 | grep -E "\[govdao\]\[dev\] proposal 0 \(|\[govdao\]\[$DEAD_CHAIN\] (every RPC endpoint failed|error fetching status)" | tail -4
