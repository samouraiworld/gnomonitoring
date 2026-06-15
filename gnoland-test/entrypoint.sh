#!/usr/bin/env sh

LOG_LEVEL=${LOG_LEVEL:-"info"}
MONIKER=${MONIKER:-"gnode"}
PEX=${PEX:-"true"}
PERSISTENT_PEERS=${PERSISTENT_PEERS:-""}
PRIVATE_PEER_IDS=${PRIVATE_PEER_IDS:-""}
SEEDS=${SEEDS:-""}
MAX_PEERS=${MAX_PEERS:-"40"}
INBOUND=${INBOUND:-"40"}


# Generate secrets only if they don't already exist. bootstrap.sh normally
# pre-generates and bind-mounts gnoland-data/secrets/, so this is a safety
# net for fresh/empty volumes, not the primary path.
if [ ! -f ./gnoland-data/secrets/priv_validator_key.json ]; then
  gnoland secrets init
fi

# Copy base config

mkdir -p ./gnoland-data/config
cp config.toml ./gnoland-data/config/config.toml

# Set the config values
gnoland config init --force
gnoland config set moniker  "${MONIKER}"
gnoland config set p2p.pex  "${PEX}"
gnoland config set p2p.persistent_peers       "${PERSISTENT_PEERS}"
gnoland config set p2p.private_peer_ids       "${PRIVATE_PEER_IDS}"
gnoland config set p2p.seeds "${SEEDS}"
gnoland config set p2p.max_num_outbound_peers  "${MAX_PEERS}"
gnoland config set p2p.max_num_inbound_peers "${INBOUND}"
gnoland config set telemetry.metrics_enabled true
gnoland config set rpc.laddr "tcp://0.0.0.0:26657"

gnoland config set telemetry.service_instance_id "${MONIKER}"
gnoland config set telemetry.exporter_endpoint "otel-collector:4317"

exec gnoland start --skip-genesis-sig-verification --genesis="./gnoland-data/genesis.json" --log-level=${LOG_LEVEL} --log-format=json
