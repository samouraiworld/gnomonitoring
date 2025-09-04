#!/bin/bash

DB_FILE="./backend/db/webhooks.db"
CSV_FILE="addr_monikers.csv"
TABLE="addr_monikers"

if [ ! -f "$DB_FILE" ]; then
    echo "❌ Database $DB_FILE not found!"
    exit 1
fi

if [ ! -f "$CSV_FILE" ]; then
    echo "❌ CSV file $CSV_FILE not found!"
    exit 1
fi

echo "📥 Importing data from $CSV_FILE into table $TABLE..."


tail -n +2 "$CSV_FILE" | while IFS=";" read -r addr moniker; do
    if [ -n "$addr" ] && [ -n "$moniker" ]; then
        sqlite3 "$DB_FILE" "INSERT INTO $TABLE (addr, moniker) VALUES ('$addr', '$moniker');"
        echo "   -> Inserted $addr | $moniker"
    fi
done

echo "✅ Import completed."
