#!/bin/bash

DB_FILE="./backend/db/webhooks.db"
TABLES=("daily_participations" "alert_logs" "govdos" "addr_monikers" )
CONFIG_FILE="./backend/config_docker.yaml"


read -p "⚠️  Are you sure you want to reset gnomonitoring? (y/N): " confirm

if [[ "$confirm" =~ ^[Yy]$ ]]; then

read -p "👉 Enter new rpc_endpoint: " NEW_RPC
read -p "👉 Enter new gnoweb: " NEW_GNOWEB
read -p "👉 Enter new graphql without https:// and with the /graphql/query at the end: " NEW_GRAPHQL

if [ ! -f "$CONFIG_FILE" ]; then
    echo "❌ $CONFIG_FILE not found!"
    exit 1
fi
echo "🔧 Updating config.yaml..."


sed -i "s|^rpc_endpoint:.*|rpc_endpoint: \"$NEW_RPC\"|" "$CONFIG_FILE"
sed -i "s|^gnoweb:.*|gnoweb: \"$NEW_GNOWEB\"|" "$CONFIG_FILE"
sed -i "s|^graphql:.*|graphql: \"$NEW_GRAPHQL\"|" "$CONFIG_FILE"

echo "✅ config.yaml updated:"
grep -E "rpc_endpoint|gnoweb|graphql" "$CONFIG_FILE"


echo "🛑 Stopping gnomonitoring services..."
docker compose -f docker-compose.yml stop
echo "✅ Services stopped."


if [ ! -f "$DB_FILE" ]; then
  echo "❌ db  $DB_FILE not exist."
  exit 1
fi

# Delete tables
for table in "${TABLES[@]}"; do
  echo "Delete tables $table..."
  sqlite3 "$DB_FILE" "DROP TABLE IF EXISTS $table;"
done




echo " ✅ Gnomonitoring reset completed."
else
    echo "❌ Reset cancelled."
    exit 1
fi