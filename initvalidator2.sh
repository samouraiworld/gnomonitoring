#!/bin/sh
set -e  



MONIKER=${MONIKER:-"validator2"}
PEX=${PEX:-"true"}
PERSISTENT_PEERS=${PERSISTENT_PEERS:-"validator1:26656"}

echo "if secrets exits."

if [ ! -f /gnoroot/gnoland-data/secrets/node_key.json ]; then

gnoland secrets init

mkdir -p ./gnoland-data/config
cp config.toml ./gnoland-data/config/config.toml

gnoland config set moniker       "${MONIKER}"
gnoland config set p2p.pex       "${PEX}"
gnoland config set p2p.persistent_peers       "${PERSISTENT_PEERS}"
fi

exec gnoland start --genesis="./gnoland-data/genesis.json"
