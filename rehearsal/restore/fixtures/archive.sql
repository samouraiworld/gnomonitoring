-- Invented fixture only. No production endpoint, account, token or row is used.
CREATE ROLE fixture_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
CREATE ROLE restore_globals_canary SUPERUSER;
ALTER DATABASE fixture_archive OWNER TO fixture_owner;
SET ROLE fixture_owner;
CREATE TABLE public.addr_monikers (
  id bigserial PRIMARY KEY,
  chain_id text NOT NULL DEFAULT 'betanet',
  addr text NOT NULL,
  moniker text NOT NULL,
  first_active_block bigint DEFAULT -1,
  voting_power bigint NOT NULL DEFAULT 0
);
CREATE TABLE public.daily_participations (
  id bigserial PRIMARY KEY,
  date timestamptz,
  block_height bigint,
  chain_id text NOT NULL DEFAULT 'betanet',
  moniker text,
  addr text NOT NULL,
  participated boolean NOT NULL,
  tx_contribution boolean NOT NULL,
  proposed boolean NOT NULL DEFAULT false
);
INSERT INTO public.addr_monikers (chain_id, addr, moniker, first_active_block, voting_power)
VALUES ('synthetic-chain', 'g1synthetic', 'synthetic-validator', -1, 1),
       ('synthetic-peer-chain', 'g1synthetic', 'synthetic-validator', -1, 1);
INSERT INTO public.daily_participations
  (date, block_height, chain_id, moniker, addr, participated, tx_contribution)
VALUES ('2026-10-26T12:00:00Z', 9, 'synthetic-chain', 'synthetic-validator', 'g1synthetic', false, false),
       ('2026-10-26T12:01:00Z', 10, 'synthetic-chain', 'synthetic-validator', 'g1synthetic', true, false),
       ('2026-10-26T12:01:00Z', 9, 'synthetic-peer-chain', 'synthetic-validator', 'g1synthetic', true, false);
CREATE TABLE public.archive_probe (
  id bigserial PRIMARY KEY,
  chain_id text,
  marker text NOT NULL,
  amount integer NOT NULL,
  CONSTRAINT archive_probe_amount_positive CHECK (amount > 0),
  CONSTRAINT archive_probe_chain_marker_key UNIQUE (chain_id, marker)
);
INSERT INTO public.archive_probe (chain_id, marker, amount)
VALUES ('synthetic-chain', 'first', 10),
       ('synthetic-chain', 'second', 20),
       ('synthetic-peer-chain', 'first', 30),
       ('', 'empty-chain', 40),
       (NULL, 'null-chain', 50);
CREATE INDEX archive_probe_chain_idx ON public.archive_probe (chain_id);
CREATE VIEW public.archive_probe_chain_totals AS
  SELECT chain_id, count(*) AS entries, sum(amount) AS total_amount
  FROM public.archive_probe GROUP BY chain_id;
RESET ROLE;
