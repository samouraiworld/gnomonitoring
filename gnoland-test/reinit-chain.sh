#!/bin/bash

set -e

echo "🔄 Reinitializing chain..."

GENESIS_FILE="genesis.json"
CONFIG_FILE="config.toml"
NODES=("validator" "validator2" "validator3")

# 1. Check genesis exists
if [ ! -f "$GENESIS_FILE" ]; then
  echo "❌ genesis.json not found in current directory"
  exit 1
fi

if [ ! -f "$CONFIG_FILE" ]; then
  echo "⚠️ config.toml not found (skipping config copy)"
fi

# 2. Stop containers (optional but recommended)
echo "🛑 Stopping containers..."
docker compose down

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
  rm -rf config db wal genesis.json

  echo "   - Resetting priv_validator_state.json"
  if [ -f "secrets/priv_validator_state.json" ]; then
    cat <<EOF > secrets/priv_validator_state.json
{
  "height": "0",
  "round": "0",
  "step": 0
}
EOF
  else
    echo "⚠️  priv_validator_state.json not found in $node"
  fi

  cd - > /dev/null
done

echo "✅ Chain reinitialized."
echo ""
echo "👉 Now restart your nodes:"
echo "docker compose up -d"
