#!/bin/sh
set -e  

MONIKER=${MONIKER:-"validator1"}


if [ ! -f /gnoroot/gnoland-data/secrets/node_key.json ]; then

gnoland secrets init

mkdir -p ./gnoland-data/config
cp config.toml ./gnoland-data/config/config.toml
gnoland config init --force = true
gnoland config set moniker       "${MONIKER}"
#gnoland config set p2p.pex       "${PEX}"
#gnoland config set p2p.persistent_peers       "${PERSISTENT_PEERS}"
gnoland config set rpc.laddr "tcp://0.0.0.0:26657"
gnoland config set telemetry.enabled true
gnoland config set telemetry.service_instance_id "${MONIKER}"
gnoland config set telemetry.exporter_endpoint "otel-collector:4317"
gnoland start --lazy 
fi

exec gnoland start 
#--genesis="/gnoroot/genesis.json"
