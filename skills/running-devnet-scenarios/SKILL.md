---
name: running-devnet-scenarios
description: Use when verifying gnomonitoring end-to-end against the local Gno.land devnet in gnoland-test/ — running the validator-onboarding, downtime, chain-halt, RPC-outage, GovDAO-rejection or alert-dedup scenarios, or checking that alerts / Prometheus metrics / GovDAO detection still work before merging.
---

# Running devnet scenarios

## Overview

`gnoland-test/` ships a Docker 3-validator Gno.land devnet (chain id **`dev`**) and
a `Makefile` that wraps the chain lifecycle and a set of test scenarios. This skill
is the procedure for driving them to verify gnomonitoring end-to-end (participation
tracking, WARNING/CRITICAL/RESOLVED alerts, chain stagnation, RPC-error, GovDAO
detection, metrics). Nothing in the devnet is committed — `bootstrap.sh` regenerates
keys/genesis/`.env` each setup, so addresses differ every run.

All `make` commands run from `gnoland-test/`. The gnomonitoring backend + Postgres
run separately (`backend/docker-compose.yml`) and monitor the `dev` chain by service
name over the `gnoland-test_default` network.

## Prerequisites

- Docker + a local clone of [gno](https://github.com/gnolang/gno) (to build images)
- `gnomonitoring-backend` + `gnomonitoring-postgres` running with a `dev` chain
  entry in `backend/config_docker.yaml` (rpc `http://validator:26657`)
- Run `make help` for the full target list

## Core workflow (order matters)

```bash
cd gnoland-test
make full-reinit                       # build, regenerate keys+genesis, start chain
docker restart gnomonitoring-backend   # REQUIRED after any reset (see gotchas)
# wait until the chain produces blocks and the backend live-watches dev:
curl -s http://localhost:26658/status | jq '.result.sync_info'
make scenario1                         # then run scenarios
```

To start fully fresh (new keys + addresses): `make clean-all && make full-reinit`.
Between scenarios that need a clean chain (e.g. proposal #0): `make reinit` then
restart the backend again.

## Scenario quick reference

| Target | Exercises | Assert |
|--------|-----------|--------|
| `scenario1` | onboard validator4 via GovDAO | `v3.IsValidator`=true; `voting_power` 0→1 on `:26661/status`; `govdaos` row id 0 |
| `scenario2` / `-restart` | validator downtime (stops validator4) | `alert_logs` WARNING→CRITICAL then RESOLVED for `addr=VALIDATOR4_ADDR` |
| `scenario3` / `-restart` | chain halt (stops validator2+3) | `alert_logs` `addr='all'` CRITICAL; log `Blockchain stuck`; height frozen |
| `scenario4` / `-restart` | total RPC outage (stops ALL) | **log only** `Error when querying latest block height` (no DB row, no webhook) |
| `scenario5` | GovDAO proposal voted NO | live "New Proposal" alert; render `NO PERCENT: 100%` |
| `scenario-mute` | resend dedup (validator4 stays down) | exactly one `alert_logs` row per level + `dedup: skipping` log |

`scenario2`/`scenario-mute` stop validator4 → run `scenario1` first so it is in the
valset. `scenario5` targets proposal #0 → run on a fresh chain (`make reinit`).

## Assertion commands

```bash
# validator alerts (scenario2 / mute): VALIDATOR4_ADDR is in gnoland-test/.devaddrs.mk
docker exec gnomonitoring-postgres psql -U gnomonitoring -d gnomonitoring \
  -c "SELECT level,start_height,end_height,skipped FROM alert_logs \
      WHERE chain_id='dev' AND addr='$(awk '/VALIDATOR4_ADDR/{print $3}' .devaddrs.mk)' ORDER BY id;"

# stagnation (scenario3): addr='all'
docker exec gnomonitoring-postgres psql -U gnomonitoring -d gnomonitoring \
  -c "SELECT level,start_height FROM alert_logs WHERE chain_id='dev' AND addr='all' ORDER BY id;"

# RPC outage (scenario4): log-only, nothing in DB
docker logs --since 60s gnomonitoring-backend | grep 'Error when querying latest block height'

# GovDAO (scenario1 / scenario5): proposal tracked + alert dispatched
docker exec gnomonitoring-postgres psql -U gnomonitoring -d gnomonitoring \
  -c "SELECT id,status,title FROM govdaos WHERE chain_id='dev';"
docker logs --since 60s gnomonitoring-backend | grep -i 'New Proposal'

# ingestion sanity (after backend restart on a fresh chain)
docker exec gnomonitoring-postgres psql -U gnomonitoring -d gnomonitoring \
  -c "SELECT count(*),count(DISTINCT addr),max(block_height) FROM daily_participations WHERE chain_id='dev';"
```

## Gotchas (these cause "no alert" / "nothing happens")

- **Restart the backend after every reset.** It caches last-seen height per chain;
  after a reset the new chain starts at 0 and it waits to climb past the old height
  before ingesting, so `daily_participations` for `dev` stays empty. The restart
  also reconnects the GovDAO websocket.
- **GovDAO alerts only fire for proposals seen LIVE over the websocket**
  (`who == "socket"` in `ProcessProposal`). A proposal created while the backend
  isn't live-watching is picked up by the startup scan → inserted into `govdaos`
  **without** an alert. So: reset → restart backend → wait for websocket → create
  proposal. On a fresh devnet there is no `webhook_gov_daos` row for `dev`, so the
  alert is delivered via the **GovDAO Telegram bot only** — check that bot, not
  Discord/Slack.
- **scenario5: NO vote does not reject immediately.** The proposal stays "open for
  votes" (`NO PERCENT: 100%`) until its voting deadline; `govdaos.status` flips to
  `REJECTED` only after that, on the next 5-minute `CheckProposalStatus` cycle.
  The immediate observable is the alert + the 100% NO render.
- **Run `make full-reinit` and `make scenario*` as SEPARATE commands.** Addresses
  are read via `-include .devaddrs.mk` at make-parse time, so the file must exist
  before the invocation that uses it (it does after `full-reinit`).
- **scenario4 alerts are log-only** — no `alert_logs` row, no webhook; assert on
  backend logs.
- A transient `[govdao][dev] dial error: lookup tx-indexer ... no such host` right
  after a backend restart is normal; it retries and recovers within seconds.

See `gnoland-test/README.md` for the full walkthrough and `make help` for all targets.
