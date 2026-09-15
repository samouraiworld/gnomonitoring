#!/bin/sh
# Resets the local devnet to block 0 before compose_dev_chain.yml starts it.
#
# Runs inside the one-shot "chain-reset" service (postgres:16-alpine, root),
# with this directory bind-mounted at /devnet. It is the in-container
# equivalent of 'make reinit' (reinit-chain.sh + clean-db):
#   1. wipe every node's chain state (config, db, wal) and reset its
#      priv_validator_state.json, keeping the generated keys and genesis;
#   2. purge the "dev" chain rows from the gnomonitoring database, so the
#      backend does not wait for the new chain to climb past stale heights.
#
# Keys, genesis and .env are NOT regenerated: that is bootstrap.sh's job
# ('make full-reinit' / 'make clean-all'), which needs the host docker daemon.
set -eu

NODES="validator validator2 validator3 validator4"
OWNER="${DOCKER_USER:-1000:1000}"
DEV_CHAIN="dev"

cd /devnet

# compose re-runs this one-shot service on every 'up', including against a
# stack that is already running. Never wipe the state of live validators: any
# single node still answering means the chain is (partly) up — e.g. during the
# validator-outage scenarios, where 'validator' itself may be the stopped one.
for node in $NODES; do
  if wget -q -T 2 -O /dev/null "http://$node:26657/status" 2>/dev/null; then
    echo "ℹ️  $node is running - skipping reset (use 'make dev-up' to restart from block 0)"
    exit 0
  fi
done

if [ ! -f genesis.json ] || [ ! -f .env ]; then
  echo "❌ gnoland-test/genesis.json or .env missing - run 'make full-reinit' (bootstrap.sh) first" >&2
  exit 1
fi

# DOCKER_USER / GNO_IMAGE here come from gnoland-test/.env (env_file), while
# the validators' user/image were interpolated by compose from its --env-file
# (COMPOSE_*). If compose was started without gnoland-test/.env it silently
# fell back to the defaults: validators would run as a different uid than the
# owner of their 0600 secrets, or on the wrong image. Refuse instead.
if [ "${COMPOSE_DOCKER_USER:-}" != "$OWNER" ] || { [ -n "${GNO_IMAGE:-}" ] && [ "${COMPOSE_GNO_IMAGE:-}" != "$GNO_IMAGE" ]; }; then
  echo "❌ compose resolved DOCKER_USER=${COMPOSE_DOCKER_USER:-?} GNO_IMAGE=${COMPOSE_GNO_IMAGE:-?}," >&2
  echo "   but gnoland-test/.env has DOCKER_USER=$OWNER GNO_IMAGE=${GNO_IMAGE:-<unset>}." >&2
  echo "   Pass both env files: docker compose -f compose_dev_chain.yml --env-file .env --env-file gnoland-test/.env ..." >&2
  echo "   (or use 'make dev-up' from gnoland-test/)" >&2
  exit 1
fi

echo "🔄 Resetting devnet chain state..."
for node in $NODES; do
  cp genesis.json "$node/"
  [ -f config.toml ] && cp config.toml "$node/"

  data="$node/gnoland-data"
  if [ ! -d "$data" ]; then
    echo "   ⚠️  $node: no gnoland-data directory, skipping"
    continue
  fi

  rm -rf "$data/config" "$data/db" "$data/wal" "$data/genesis.json"
  if [ -f "$data/secrets/priv_validator_state.json" ]; then
    printf '{"height":"0","round":"0","step":0}\n' > "$data/secrets/priv_validator_state.json"
  fi
  chown -R "$OWNER" "$node"
  echo "   🧹 $node reset"
done

echo "🧹 Purging chain '$DEV_CHAIN' rows from the gnomonitoring database (keeping webhooks, contacts and Telegram subs)..."
for table in daily_participations addr_monikers govdaos daily_participation_agregas alert_logs; do
  # Tables do not exist yet on a brand-new database: the backend creates them
  # on first start, so a missing table is not an error here.
  psql -v ON_ERROR_STOP=1 -qtc \
    "DO \$\$ BEGIN IF to_regclass('public.$table') IS NOT NULL THEN DELETE FROM $table WHERE chain_id = '$DEV_CHAIN'; END IF; END \$\$;"
done

echo "✅ Devnet reset to block 0."
