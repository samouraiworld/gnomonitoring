#!/bin/sh
set -e  

MONIKER=${MONIKER:-"validator1"}
PEX=${PEX:-"true"}
PERSISTENT_PEERS=${PERSISTENT_PEERS:-"validator2:26656"}

echo "if secrets exits."

if [ ! -f /gnoroot/gnoland-data/secrets/node_key.json ]; then
    echo "📜 don´t have secret ."
    gnoland  secrets init 
    gnoland config set moniker       "${MONIKER}"
    gnoland config set p2p.pex       "${PEX}"
    gnoland config set p2p.persistent_peers       "${PERSISTENT_PEERS}"

    gnoland start --lazy 
       
fi

echo "Run Gnoland"
exec gnoland start  --genesis="/gnoroot/genesis.json" --log-level=debug
