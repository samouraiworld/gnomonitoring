#!/usr/bin/env bash
set -euo pipefail
umask 077

[[ "${REHEARSAL_CLONE_ID:-}" =~ ^[0-9a-f]{32}$ ]]
[[ "${POSTGRES_USER:-}" == rehearsal_admin ]]
[[ "$(id -u)" == 999 ]]
[[ ! -e /run/rehearsal-postgres/clone.dsn ]]
[[ ! -e /run/rehearsal-postgres/empty.pgpass ]]
: > /run/rehearsal-postgres/empty.pgpass
chmod 0600 /run/rehearsal-postgres/empty.pgpass

# Entrypoint sources this only when initializing our brand-new PGDATA.
# There are deliberately no application tables, rows, marker or DSN here.
PGPASSFILE=/run/rehearsal-postgres/empty.pgpass psql --no-psqlrc --no-password -v ON_ERROR_STOP=1 -h /run/rehearsal-postgres \
  -U rehearsal_admin -d postgres -v clone_database="gnomonitoring_rehearsal_$REHEARSAL_CLONE_ID" <<'SQL'
CREATE ROLE rehearsal_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 8;
CREATE DATABASE :"clone_database" OWNER rehearsal_owner;
SQL
touch /run/rehearsal-postgres/empty-bootstrap.ready
chmod 0600 /run/rehearsal-postgres/empty-bootstrap.ready
