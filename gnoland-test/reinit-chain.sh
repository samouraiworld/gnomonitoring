#!/bin/bash

set -e

echo "🔄 Reinitializing chain..."

GENESIS_FILE="genesis.json"
CONFIG_FILE="config.toml"
NODES=("validator" "validator2" "validator3" "validator4")

# 1. Ensure genesis exists (regenerate via bootstrap.sh if missing)
if [ ! -f "$GENESIS_FILE" ]; then
  echo "⚙️  genesis.json not found — regenerating via bootstrap.sh..."
  ./bootstrap.sh
fi

if [ ! -f "$CONFIG_FILE" ]; then
  echo "⚠️ config.toml not found (skipping config copy)"
fi

# 2. Stop containers (including validator4 if it was started via the phase2 profile)
# -v removes named Docker volumes (tx-indexer-db) so the indexer starts fresh
# with the new chain. Validator bind mounts are NOT affected by -v.
echo "🛑 Stopping containers and removing tx-indexer DB volume..."
docker compose --profile phase2 down -v

# 3. Copy genesis to all nodes
echo "📦 Copying genesis.json to nodes..."
for node in "${NODES[@]}"; do
  cp "$GENESIS_FILE" "$node/"
done


# 4. Copy config to all nodes
echo "📦 Copying config.toml to nodes..."
for node in "${NODES[@]}"; do
  cp "$CONFIG_FILE" "$node/"
done


# 3. Reset each node
for node in "${NODES[@]}"; do
  echo "🧹 Cleaning node: $node"

  DATA_DIR="$node/gnoland-data"

  if [ ! -d "$DATA_DIR" ]; then
    echo "⚠️  Skipping $node (no gnoland-data directory)"
    continue
  fi

  cd "$DATA_DIR"

  echo "   - Removing config, db, wal, genesis.json"
  docker run --rm -v "$PWD:/data" alpine sh -c "rm -rf /data/config /data/db /data/wal /data/genesis.json"

  echo "   - Resetting priv_validator_state.json"
  if [ -f "secrets/priv_validator_state.json" ] || docker run --rm -v "$PWD/secrets:/secrets" alpine test -f /secrets/priv_validator_state.json 2>/dev/null; then
    docker run --rm --user "$(id -u):$(id -g)" -v "$PWD/secrets:/secrets" alpine sh -c \
      'printf '"'"'{"height":"0","round":"0","step":0}\n'"'"' > /secrets/priv_validator_state.json'
  else
    echo "⚠️  priv_validator_state.json not found in $node"
  fi

  cd - > /dev/null
done

echo "✅ Chain reinitialized."
echo ""
echo "👉 Now restart your nodes:"
echo "docker compose up -d"
