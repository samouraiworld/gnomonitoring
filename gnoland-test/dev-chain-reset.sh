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
# stack that is already running. Never wipe the state of live validators.
if wget -q -T 2 -O /dev/null http://validator:26657/status 2>/dev/null; then
  echo "ℹ️  devnet already running - skipping reset (use 'make dev-up' to restart from block 0)"
  exit 0
fi

if [ ! -f genesis.json ] || [ ! -f .env ]; then
  echo "❌ gnoland-test/genesis.json or .env missing - run 'make full-reinit' (bootstrap.sh) first" >&2
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
