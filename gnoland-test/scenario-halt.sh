#!/usr/bin/env bash
# scenario-halt.sh - schedule a chain halt by GovDAO vote on the devnet and
# check that gnomonitoring announces it, reminds before it and reports the
# chain resuming after it. Run it through 'make scenario-halt', on a fresh
# chain built from gno v1.8.0 or later ('make dev-up' first).
#
# Steps:
#   1. Shorten the reminder thresholds (halt_reminder_minutes and
#      halt_final_reminder_minutes in admin_config) so the run takes minutes.
#   2. Make the dev account a T1 GovDAO member. Since v1.8.0 the genesis
#      loader sets the DAO up with no members and an empty AllowedDAOs, so a
#      MsgRun may add one (gno.land/cmd/gnoland/UPGRADES-TESTING.md).
#   3. Propose r/sys/params.NewSetHaltRequest(height, MIN_VERSION), vote YES
#      and execute it. The height leaves the scheduled notice outside the
#      reminder window, so both reminders go out on their own.
#   4. Wait for the chain to halt at that height and to resume. A node halts
#      by panicking in the BeginBlock after the halt height; docker restarts it
#      (restart: on-failure) and it resumes because the binary meets
#      MIN_VERSION. Nodes still stuck are restarted by hand.
#   5. Assert the alert_logs rows of chain dev: HALT_SCHEDULED, HALT_REMINDER,
#      HALT_FINAL_REMINDER and HALT_RESUMED, each dispatched once, in order.
#   6. On exit, success or failure: restore the previous thresholds.
#
# MIN_VERSION defaults to "dev", the version compiled into an image built
# without BUILD_VERSION: a non-semver version is compared by equality, so the
# same binary may resume. Prints PASS, or FAIL and exits non-zero.
set -euo pipefail
cd "$(dirname "$0")"

DEV_COMPOSE="${DEV_COMPOSE:-docker compose -f ../compose_dev_chain.yml --env-file ../.env --env-file .env}"
RPC="${RPC:-http://localhost:26658}"
PG_CONTAINER="${PG_CONTAINER:-gnomonitoring-postgres}"
PG_USER="${PG_USER:-gnomonitoring}"
PG_DB="${PG_DB:-gnomonitoring}"
DEV_HOME="${DEV_HOME:-$PWD/.gnokey-dev}"
DEV_PASS="${DEV_PASS:-devpassword123}"
MIN_VERSION="${MIN_VERSION:-dev}"
REMINDER_MINUTES="${REMINDER_MINUTES:-3}"
FINAL_REMINDER_MINUTES="${FINAL_REMINDER_MINUTES:-1}"
# Time between the scheduled notice and the halt. Longer than the reminder
# window, with room for the vote and execution txs.
LEAD_SECONDS="${LEAD_SECONDS:-300}"
# Compose services of the validators, restarted if a node stays halted.
VALIDATORS="${VALIDATORS:-validator validator2 validator3}"

# Any unexpected error ends the run as a FAIL, not a silent exit.
trap 'echo "FAIL: line $LINENO: $BASH_COMMAND"' ERR

# These values end up in SQL, in generated Gno code and in shell arithmetic.
for v in REMINDER_MINUTES FINAL_REMINDER_MINUTES LEAD_SECONDS; do
  [[ "${!v}" =~ ^[0-9]+$ ]] || { echo "FAIL: $v must be a non-negative integer"; exit 1; }
done
[[ "$MIN_VERSION" =~ ^[A-Za-z0-9._+/-]*$ ]] || { echo "FAIL: MIN_VERSION has unexpected characters"; exit 1; }

DEV_ADDR="$(awk '/^DEV_ADDR/{print $3}' .devaddrs.mk 2>/dev/null || true)"
[[ "$DEV_ADDR" =~ ^g1[a-z0-9]+$ ]] || { echo "FAIL: DEV_ADDR missing from .devaddrs.mk (run 'make full-reinit')"; exit 1; }

psql_q() {
  docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc "$1"
}

set_thresholds() {
  psql_q "UPDATE admin_configs SET value = '$1' WHERE key = 'halt_reminder_minutes';
          UPDATE admin_configs SET value = '$2' WHERE key = 'halt_final_reminder_minutes';" >/dev/null
}

threshold() {
  psql_q "SELECT value FROM admin_configs WHERE key = '$1'"
}

WORK_DIR=""
PREV_REMINDER=""
PREV_FINAL=""
cleanup() {
  if [ -n "$PREV_REMINDER" ] && [ -n "$PREV_FINAL" ]; then
    set_thresholds "$PREV_REMINDER" "$PREV_FINAL" || true
  fi
  if [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
    rm -rf -- "$WORK_DIR"
  fi
}
trap cleanup EXIT

height() {
  curl -s --max-time 5 "$RPC/status" | jq -r '.result.sync_info.latest_block_height // empty' 2>/dev/null || true
}

block_time() {
  curl -s --max-time 5 "$RPC/block?height=$1" | jq -r '.result.block_meta.header.time // .result.block.header.time'
}

halt_param() {
  curl -s --max-time 5 "$RPC/abci_query?path=%22params/node:p:$1%22" | jq -r '.result.response.ResponseBase.Data // empty' | base64 -d 2>/dev/null || true
}

gnokey_run() {
  echo "$DEV_PASS" | gnokey maketx run \
    -gas-fee 1000000ugnot -gas-wanted 100000000 -broadcast \
    -chainid dev -remote "$RPC" \
    -home "$DEV_HOME" -insecure-password-stdin dev "$1"
}

gnokey_call() {
  local fn="$1"; shift
  local args=()
  for a in "$@"; do args+=(-args "$a"); done
  echo "$DEV_PASS" | gnokey maketx call \
    -pkgpath gno.land/r/gov/dao -func "$fn" "${args[@]}" \
    -gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
    -chainid dev -remote "$RPC" \
    -home "$DEV_HOME" -insecure-password-stdin dev
}

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scenario-halt.XXXXXX")"

echo ">>> Step 1: reminder thresholds ${REMINDER_MINUTES}m / ${FINAL_REMINDER_MINUTES}m"
[ "$(psql_q "SELECT count(*) FROM admin_configs WHERE key IN ('halt_reminder_minutes','halt_final_reminder_minutes')")" = 2 ] \
  || { echo "FAIL: halt_* thresholds not seeded: is the backend built from this branch?"; exit 1; }
PREV_REMINDER="$(threshold halt_reminder_minutes)"
PREV_FINAL="$(threshold halt_final_reminder_minutes)"
set_thresholds "$REMINDER_MINUTES" "$FINAL_REMINDER_MINUTES"
psql_q "DELETE FROM alert_logs WHERE chain_id = 'dev' AND addr = 'halt';" >/dev/null

echo ">>> Step 2: make $DEV_ADDR a T1 GovDAO member"
cat >"$WORK_DIR/member.gno" <<EOF
package main

import "gno.land/r/gov/dao/memberstore/v0"

func main(cur realm) {
	err := memberstore.Get(0, cur).SetMember(memberstore.T1, address("$DEV_ADDR"), memberstore.NewMember(3))
	if err != nil {
		panic(err)
	}
}
EOF
gnokey_run "$WORK_DIR/member.gno" || echo "    (membership tx failed: already a member?)"

echo ">>> Step 3: schedule the halt by GovDAO vote"
h0="$(height)"
[[ "$h0" =~ ^[0-9]+$ ]] && [ "$h0" -ge 11 ] || { echo "FAIL: chain height '${h0}' too low to measure a pace"; exit 1; }
span=50
[ "$h0" -gt "$span" ] || span=$((h0 - 1))
t1="$(date -d "$(block_time "$h0")" +%s.%N)"
t0="$(date -d "$(block_time $((h0 - span)))" +%s.%N)"
pace="$(echo "($t1 - $t0) / $span" | bc -l)"
# Blocks for LEAD_SECONDS, plus ~20 blocks for the propose/vote/execute txs.
halt_height=$(( h0 + $(printf '%.0f' "$(echo "$LEAD_SECONDS / $pace" | bc -l)") + 20 ))
printf '    height %s, pace %.2fs/block, halt height %s, min version %s\n' "$h0" "$pace" "$halt_height" "$MIN_VERSION"

cat >"$WORK_DIR/propose.gno" <<EOF
package main

import (
	"gno.land/r/gov/dao"
	"gno.land/r/sys/params"
)

func main(cur realm) {
	pid := dao.MustCreateProposal(cross(cur), params.NewSetHaltRequest(cross(cur), $halt_height, "$MIN_VERSION"))
	println("PROPOSAL_ID", int64(pid))
}
EOF
out="$(gnokey_run "$WORK_DIR/propose.gno" 2>&1 || true)"
echo "$out" | grep -E "PROPOSAL_ID|OK!|ERROR" || true
pid="$(echo "$out" | awk '/PROPOSAL_ID/{print $2}' | tr -d '"' | head -1)"
[[ "$pid" =~ ^[0-9]+$ ]] || { echo "FAIL: no proposal id in the propose output"; exit 1; }
gnokey_call MustVoteOnProposalSimple "$pid" YES 2>&1 | grep -E "OK!|ERROR" || true
gnokey_call ExecuteProposal "$pid" 2>&1 | grep -E "OK!|ERROR" || true

got_height="$(halt_param halt_height)"
got_version="$(halt_param halt_min_version)"
echo "    params: halt_height=$got_height halt_min_version=$got_version"
[ "$got_height" = "\"$halt_height\"" ] || { echo "FAIL: halt_height param not set"; exit 1; }

echo ">>> Step 4: wait for the halt at $halt_height and the chain resuming"
deadline=$(( $(date +%s) + LEAD_SECONDS * 3 + 300 ))
stuck_since=""
while :; do
  h="$(height || true)"
  if [ -n "$h" ] && [ "$h" -gt "$halt_height" ]; then
    echo "    chain resumed at height $h"
    break
  fi
  if [ -n "$h" ] && [ "$h" -eq "$halt_height" ]; then
    stuck_since="${stuck_since:-$(date +%s)}"
    if [ $(( $(date +%s) - stuck_since )) -ge 60 ]; then
      echo "    halted at $h for 60s: restarting the validators"
      # shellcheck disable=SC2086
      $DEV_COMPOSE restart $VALIDATORS || echo "    (restart failed)"
      stuck_since=""
    fi
  fi
  [ "$(date +%s)" -lt "$deadline" ] || { echo "FAIL: chain did not halt and resume in time (height ${h:-unknown})"; exit 1; }
  sleep 5
done

echo ">>> Step 5: wait for the resumed notice (poll every 30s)"
want="HALT_SCHEDULED HALT_REMINDER HALT_FINAL_REMINDER HALT_RESUMED"
for _ in $(seq 1 24); do
  got="$(psql_q "SELECT string_agg(level, ' ' ORDER BY id) FROM alert_logs
                 WHERE chain_id = 'dev' AND addr = 'halt' AND skipped = true AND start_height = $halt_height")"
  [ "$got" = "$want" ] && break
  sleep 5
done
psql_q "SELECT level, start_height, moniker, skipped, sent_at FROM alert_logs
        WHERE chain_id = 'dev' AND addr = 'halt' ORDER BY id" | sed 's/^/    /'
docker logs gnomonitoring-backend 2>&1 | grep '\[halt\]\[dev\]' | grep -v "read failed" | tail -8 | sed 's/^/    /'
if [ "$got" = "$want" ]; then
  echo "PASS: $got"
else
  echo "FAIL: dispatched notices '$got', want '$want'"
  exit 1
fi
