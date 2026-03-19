#!/bin/bash

DB_FILE="./backend/db/webhooks.db"
TABLES=("daily_participations" "alert_logs" "govdaos" "addr_monikers")
CONFIG_FILE="./backend/config.yaml"

read -p "⚠️  Are you sure you want to reset gnomonitoring? (y/N): " confirm

if [[ "$confirm" =~ ^[Yy]$ ]]; then
	if [ ! -f "$DB_FILE" ]; then
		echo "❌ Database file $DB_FILE not found."
		exit 1
	fi

	# Delete tables
	for table in "${TABLES[@]}"; do
		echo "Dropping table $table..."
		sqlite3 "$DB_FILE" "DROP TABLE IF EXISTS $table;"
	done

	echo "✅ Gnomonitoring reset completed."
else
	echo "❌ Reset cancelled."
	exit 1
fi