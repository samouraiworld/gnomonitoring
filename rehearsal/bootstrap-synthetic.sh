#!/usr/bin/env bash
set -euo pipefail
umask 077

[[ "${REHEARSAL_CLONE_ID:-}" =~ ^[0-9a-f]{32}$ ]]
[[ "${POSTGRES_USER:-}" == rehearsal_admin ]]
[[ ! -e /run/rehearsal-postgres/clone.dsn ]]
[[ "$(id -u)" == 999 ]]

# Docker's entrypoint invokes this only while initializing the fresh PGDATA.
# Never run this script against an already-running database or a restored dump.
psql --no-psqlrc -v ON_ERROR_STOP=1 -h /run/rehearsal-postgres -U rehearsal_admin -d postgres \
  -v clone_id="$REHEARSAL_CLONE_ID" -v clone_database="gnomonitoring_rehearsal_$REHEARSAL_CLONE_ID" <<'SQL'
CREATE ROLE rehearsal_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION CONNECTION LIMIT 5;
CREATE DATABASE :"clone_database" OWNER rehearsal_owner;
\connect :clone_database
CREATE SCHEMA rehearsal_guard AUTHORIZATION rehearsal_admin;
CREATE TABLE rehearsal_guard.clone_marker (
  clone_id text PRIMARY KEY,
  database_name text NOT NULL,
  base_revision text NOT NULL
);
INSERT INTO rehearsal_guard.clone_marker VALUES (
  :'clone_id', current_database(), '92ed63fe7eb5ec54327aff36dc2bcaf2e6f24968'
);
REVOKE ALL ON SCHEMA rehearsal_guard FROM PUBLIC;
REVOKE ALL ON rehearsal_guard.clone_marker FROM PUBLIC;
GRANT USAGE ON SCHEMA rehearsal_guard TO rehearsal_owner;
GRANT SELECT ON rehearsal_guard.clone_marker TO rehearsal_owner;
SET ROLE rehearsal_owner;
CREATE TABLE public.addr_monikers (
 id bigserial PRIMARY KEY, chain_id text NOT NULL DEFAULT 'betanet', addr text NOT NULL,
 moniker text NOT NULL, first_active_block bigint DEFAULT -1, voting_power bigint NOT NULL DEFAULT 0
);
CREATE TABLE public.daily_participations (
 id bigserial PRIMARY KEY, date timestamptz, block_height bigint, chain_id text NOT NULL DEFAULT 'betanet',
 moniker text, addr text NOT NULL, participated boolean NOT NULL, tx_contribution boolean NOT NULL,
 proposed boolean NOT NULL DEFAULT false
);
INSERT INTO public.addr_monikers (chain_id, addr, moniker, first_active_block, voting_power)
 VALUES ('synthetic-chain', 'g1synthetic', 'synthetic-validator', -1, 1);
INSERT INTO public.daily_participations (date, block_height, chain_id, moniker, addr, participated, tx_contribution)
 VALUES ('2026-10-26T12:00:00Z', 9, 'synthetic-chain', 'synthetic-validator', 'g1synthetic', false, false),
        ('2026-10-26T12:01:00Z', 10, 'synthetic-chain', 'synthetic-validator', 'g1synthetic', true, false);
SQL

printf 'postgresql://rehearsal_owner@/gnomonitoring_rehearsal_%s?host=/run/rehearsal-postgres&sslmode=disable\n' \
  "$REHEARSAL_CLONE_ID" > /run/rehearsal-postgres/clone.dsn
chmod 0600 /run/rehearsal-postgres/clone.dsn
chmod 0700 /run/rehearsal-private
