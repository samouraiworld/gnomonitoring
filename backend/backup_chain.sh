#!/usr/bin/env bash
set -euo pipefail

# Backup a single chain's data out of the shared prod Postgres DB before that
# chain is decommissioned. Two things are produced:
#   1. A full pg_dump (custom format, all chains) - the real safety net,
#      trivially restorable with pg_restore.
#   2. Per-table CSV exports filtered to chain_id=<chain_id> - for quickly
#      inspecting/archiving that chain's data alone, without restoring the
#      whole database.
#
# Usage:
#   ./backup_chain.sh <chain_id> [backup_dir]
#
# Env overrides:
#   PGCONTAINER   docker-compose container name (default: gnomonitoring-postgres)
#   PGUSER        postgres user                 (default: gnomonitoring)
#   PGDATABASE    postgres database             (default: gnomonitoring)
#   PGPASSWORD    only needed if pg_hba requires auth for local connections

if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <chain_id> [backup_dir]" >&2
    exit 1
fi

CHAIN_ID="$1"
BACKUP_DIR="${2:-./backups}"

if ! [[ "$CHAIN_ID" =~ ^[A-Za-z0-9_-]+$ ]]; then
    echo "Invalid chain_id: $CHAIN_ID" >&2
    exit 1
fi

PGCONTAINER="${PGCONTAINER:-gnomonitoring-postgres}"
PGUSER="${PGUSER:-gnomonitoring}"
PGDATABASE="${PGDATABASE:-gnomonitoring}"

if ! docker ps --format '{{.Names}}' | grep -qx "$PGCONTAINER"; then
    echo "Container '$PGCONTAINER' is not running" >&2
    exit 1
fi

DOCKER_EXEC=(docker exec)
if [[ -n "${PGPASSWORD:-}" ]]; then
    DOCKER_EXEC+=(-e "PGPASSWORD=${PGPASSWORD}")
fi
DOCKER_EXEC+=("$PGCONTAINER")

# Tables scoped by chain_id are discovered dynamically instead of hardcoded:
# GORM's default naming with acronyms (GovDAO) is inconsistent across this
# codebase (e.g. db_init.go alters "govdao" while migrate-sqlite-to-postgres
# copies "webhook_validators" - the actual names only information_schema
# knows for sure). Webhook tables also have a *nullable* chain_id (NULL =
# "all chains"); NULL rows are intentionally excluded since they aren't
# specific to CHAIN_ID and are unaffected by its shutdown.
echo "==> Discovering chain-scoped tables"
mapfile -t TABLES < <("${DOCKER_EXEC[@]}" psql -U "$PGUSER" -d "$PGDATABASE" -tAc \
    "SELECT table_name FROM information_schema.columns WHERE table_schema = 'public' AND column_name = 'chain_id' ORDER BY table_name")

if [[ ${#TABLES[@]} -eq 0 ]]; then
    echo "No table with a chain_id column found - aborting" >&2
    exit 1
fi
echo "  found: ${TABLES[*]}"

TS="$(date +'%Y%m%d-%H%M%S')"
OUT_DIR="$BACKUP_DIR/${CHAIN_ID}_${TS}"
mkdir -p "$OUT_DIR"

echo "==> Full database dump (all chains, custom format)"
"${DOCKER_EXEC[@]}" pg_dump -U "$PGUSER" -d "$PGDATABASE" -Fc \
    > "$OUT_DIR/full_${PGDATABASE}_${TS}.dump"

echo "==> Chain-scoped CSV export (chain_id='$CHAIN_ID')"
for table in "${TABLES[@]}"; do
    echo "  - $table"
    "${DOCKER_EXEC[@]}" psql -U "$PGUSER" -d "$PGDATABASE" -v ON_ERROR_STOP=1 \
        -c "\copy (SELECT * FROM \"$table\" WHERE chain_id = '$CHAIN_ID') TO STDOUT WITH CSV HEADER" \
        > "$OUT_DIR/${table}.csv"
done

echo "==> Archiving"
tar -czf "$BACKUP_DIR/${CHAIN_ID}_${TS}.tar.gz" -C "$BACKUP_DIR" "${CHAIN_ID}_${TS}"
rm -rf "$OUT_DIR"

echo "Backup complete: $BACKUP_DIR/${CHAIN_ID}_${TS}.tar.gz"
