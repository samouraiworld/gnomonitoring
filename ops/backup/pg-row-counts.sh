#!/usr/bin/env bash
# Print the exact row count of every table of a Postgres database running in a
# Docker container, one "schema.table<TAB>count" line per table, sorted.
#
# The session is read-only (default_transaction_read_only), so the script is
# safe to run against production. It reads every row of every table
# (count(*)): expect a few seconds to a minute of disk reads on a large table.
#
# Usage:
#   pg-row-counts.sh [--container NAME] [--user USER] [--db DB]
#
#   defaults: gnomonitoring-postgres, gnomonitoring, gnomonitoring
#
# Compare two outputs with pg-restore-test.sh --source-counts, or with diff.
set -euo pipefail

container=gnomonitoring-postgres
user=gnomonitoring
db=gnomonitoring

usage() {
	sed -n '/^# Usage:/,/^# Compare/p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

while (($#)); do
	case $1 in
	--container | --user | --db)
		(($# >= 2)) || usage
		printf -v "${1#--}" '%s' "$2"
		shift 2
		;;
	*) usage ;;
	esac
done

for name in container user db; do
	[[ ${!name} =~ ^[A-Za-z0-9_.-]+$ ]] || {
		echo "invalid --$name" >&2
		exit 2
	}
done

# query_to_xml runs one dynamic count(*) per table inside a single statement,
# so no PL/pgSQL function has to be created in the database.
query="
SELECT format('%s.%s', n.nspname, c.relname),
       (xpath('/row/n/text()',
              query_to_xml(format('SELECT count(*) AS n FROM %I.%I', n.nspname, c.relname),
                           false, true, '')))[1]::text
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND c.relpersistence <> 't' -- other sessions' temporary tables cannot be read
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
ORDER BY 1"

docker exec "$container" psql -X -A -t -q -w -v ON_ERROR_STOP=1 -F $'\t' \
	-U "$user" -d "$db" \
	-c 'SET default_transaction_read_only = on' \
	-c "$query"
