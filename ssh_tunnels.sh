#!/bin/bash

TUNNELS=(
    "3000:127.0.0.1:3000 root@monitoring" # grafana
    "9464:127.0.0.1:9464 root@monitoring" #oltp
    "9090:127.0.0.1:9090 root@monitoring" #node_exporter
    "9100:127.0.0.1:9100 root@monitoring" #prometheus
    "26657:127.0.0.1:26657 root@monitoring" #rpc
    "8888:127.0.0.1:8888 root@monitoring" #block_exproter
)


for TUNNEL in "${TUNNELS[@]}"; do
    echo "Ouverture du tunnel SSH : $TUNNEL"
    ssh -N -L $TUNNEL &
    sleep 1  
done

echo "Tous les tunnels sont ouverts."
echo "close all tunnel pkill -f \"ssh -N -L\" "


wait



