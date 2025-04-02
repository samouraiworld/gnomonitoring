#!/bin/sh
set -e  

echo "if secrets exits."

if [ ! -f /gnoroot/gnoland-data/secrets/node_key.json ]; then
    echo "📜 don´t have secret ."
    gnoland  secrets init 
    gnoland start --lazy 
       
fi

echo "Run Gnoland"
exec gnoland start config /gnoroot/gnoland-data/config/config.toml