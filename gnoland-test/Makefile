# Pin the compose project name so the devnet network is always
# "gnoland-test_default", regardless of the directory name. The gnomonitoring
# backend (backend/docker-compose.yml) attaches to that network by name, so a
# stable project name prevents DNS breakage ("no such host: validator") when
# this folder gets copied or renamed.
export COMPOSE_PROJECT_NAME := gnoland-test

DEV_HOME := $(CURDIR)/.gnokey-dev
DEV_PASS := devpassword123
RPC      := http://localhost:26658

# Live addresses (VALIDATOR4_ADDR/PUB, VALIDATOR4_OP_ADDR, DEV_ADDR) are no longer
# hardcoded: keys are regenerated on every fresh setup, so bootstrap.sh writes
# them to .devaddrs.mk (gitignored) and we include them here. The leading "-"
# means make won't fail if it's missing (run 'make full-reinit' to generate it).
-include .devaddrs.mk

# gnomonitoring backend Postgres - used to purge the local "dev" chain data on
# every reinit, so validator metrics / participation don't carry stale block
# heights from the previous chain instance.
PG_CONTAINER := gnomonitoring-postgres
PG_USER      := gnomonitoring
PG_DB        := gnomonitoring
DEV_CHAIN    := dev

.PHONY: help up up-validator4 ensure-bootstrap down reinit regen-genesis full-reinit clean-db clean-all status logs \
        stop-val1 stop-val2 stop-val3 stop-val4 \
        start-val1 start-val2 start-val3 start-val4 \
        scenario1 scenario2 scenario2-restart scenario3 scenario3-restart \
        scenario4 scenario4-restart scenario5 scenario-mute

help:
	@echo "Chain lifecycle:"
	@echo "  make up                - start validator/validator2/validator3 + tx-indexer + gnoweb"
	@echo "  make up-validator4     - start validator4 (phase2 profile, needed for scenario1)"
	@echo "  make down              - stop and remove all containers"
	@echo "  make reinit            - wipe chain state + purge dev-chain DB rows, restart from existing genesis.json"
	@echo "  make regen-genesis     - delete + regenerate genesis.json (after editing generate-genesis.sh)"
	@echo "  make full-reinit       - regen-genesis + reinit + up (use after changing powers/params)"
	@echo "  make clean-db          - purge local dev-chain rows from the gnomonitoring DB (keeps Telegram subs)"
	@echo "  make clean-all         - nuke ALL generated state (keys/genesis/config) -> full regen on next full-reinit"
	@echo "  make status            - docker compose ps + height/catching_up per node"
	@echo "  make logs              - follow logs of all containers"
	@echo ""
	@echo "Per-validator control:"
	@echo "  make stop-val1..4      - docker compose stop validator[N]"
	@echo "  make start-val1..4     - docker compose start validator[N]"
	@echo ""
	@echo "Test scenarios (see README.md for details):"
	@echo "  make scenario1         - GovDAO: init DAO, register & onboard validator4"
	@echo "                           (targets proposal #0 - run 'make reinit' first"
	@echo "                           if this isn't the first proposal on the chain)"
	@echo "  make scenario2         - stop validator4 (downtime: WARNING then CRITICAL)"
	@echo "                           (run scenario1 first so validator4 is in the valset)"
	@echo "  make scenario2-restart - restart validator4 (expect RESOLVED)"
	@echo "  make scenario3         - stop validator2+validator3 (halt the chain -> stagnation)"
	@echo "  make scenario3-restart - restart validator2+validator3"
	@echo "  make scenario4         - stop ALL validators (RPC-error alert, LOG-ONLY)"
	@echo "  make scenario4-restart - restart all validators"
	@echo "  make scenario5         - GovDAO proposal voted NO (rejected-status path; fresh chain)"
	@echo "  make scenario-mute     - keep validator4 down to exercise the resend dedup"

# Guard: the generated .env (P2P seeds) and genesis.json are required for the
# chain to form. docker-compose has no default for SEEDS_*, so a missing .env
# would start validators with empty peers and the chain would silently never
# produce blocks. Bootstrap (idempotent) if either is missing.
ensure-bootstrap:
	@if [ ! -f .env ] || [ ! -f genesis.json ]; then \
		echo "ℹ️  .env/genesis.json missing — running bootstrap.sh first..."; \
		./bootstrap.sh; \
	fi

up: ensure-bootstrap
	docker compose up -d

up-validator4: ensure-bootstrap
	docker compose --profile phase2 up -d validator4

down:
	docker compose down

reinit: clean-db
	./reinit-chain.sh
	docker compose up -d

regen-genesis:
	rm -f genesis.json
	./bootstrap.sh

full-reinit: regen-genesis clean-db
	./reinit-chain.sh
	docker compose up -d

# Purge the local "dev" chain rows from the gnomonitoring backend DB. Chain data
# only (participation, monikers, govdao proposals, aggregates, alert logs).
# Telegram subscriptions are intentionally KEPT so alerts are still delivered on
# the next test run. No-op (with a notice) if the Postgres container isn't up.
clean-db:
	@if docker ps --format '{{.Names}}' | grep -qx '$(PG_CONTAINER)'; then \
		echo "🧹 Purging chain '$(DEV_CHAIN)' data from DB '$(PG_DB)' (keeping Telegram subs)..."; \
		docker exec $(PG_CONTAINER) psql -U $(PG_USER) -d $(PG_DB) \
			-c "DELETE FROM daily_participations WHERE chain_id='$(DEV_CHAIN)';" \
			-c "DELETE FROM addr_monikers WHERE chain_id='$(DEV_CHAIN)';" \
			-c "DELETE FROM govdaos WHERE chain_id='$(DEV_CHAIN)';" \
			-c "DELETE FROM daily_participation_agregas WHERE chain_id='$(DEV_CHAIN)';" \
			-c "DELETE FROM alert_logs WHERE chain_id='$(DEV_CHAIN)';" \
		|| echo "⚠️  DB purge failed (continuing with chain reinit anyway)"; \
	else \
		echo "ℹ️  $(PG_CONTAINER) not running — skipping DB purge."; \
	fi

# Nuke ALL generated devnet state so the next 'make full-reinit' regenerates
# keys, accounts, genesis and config from scratch (fresh addresses everywhere).
# None of this is committed (see .gitignore). Uses a throwaway alpine container
# to delete files that docker may have created as root.
clean-all: clean-db
	docker compose --profile phase2 down -v 2>/dev/null || true
	@echo "🧨 Removing all generated devnet artifacts (keys, genesis, config, keybase)..."
	docker run --rm -v "$(CURDIR):/work" -w /work alpine sh -c \
		'rm -rf validator/gnoland-data validator2/gnoland-data validator3/gnoland-data validator4/gnoland-data \
		        validator/genesis.json validator2/genesis.json validator3/genesis.json validator4/genesis.json \
		        validator/config.toml validator2/config.toml validator3/config.toml validator4/config.toml \
		        .gnokey-dev genesis.json genesis_balances.txt config.toml .env .devaddrs.mk \
		        init-govdao.gno propose-validator4.gno'
	@echo "✅ Clean. Run 'make full-reinit' to regenerate everything from scratch."

status:
	docker compose ps
	@for port in 26658 26659 26660 26661; do \
		echo "--- localhost:$$port ---"; \
		curl -s http://localhost:$$port/status | jq -r '.result.sync_info | {latest_block_height, catching_up}' 2>/dev/null || echo "unreachable"; \
	done

logs:
	docker compose logs -f

stop-val1:
	docker compose stop validator
stop-val2:
	docker compose stop validator2
stop-val3:
	docker compose stop validator3
stop-val4:
	docker compose stop validator4

start-val1:
	docker compose start validator
start-val2:
	docker compose start validator2
start-val3:
	docker compose start validator3
start-val4:
	docker compose start validator4

scenario1: up-validator4
	@echo ">>> Step 1: initialize GovDAO (InitWithUsers)"
	@if gnokey query vm/qeval -remote $(RPC) \
		-data 'gno.land/r/gov/dao.AllowedDAOs()' 2>/dev/null | grep -q 'gno.land/r/gov/dao/v3/impl'; then \
		echo "    GovDAO already initialized on this chain (AllowedDAOs locked to v3/impl) - skipping."; \
		echo "    (InitWithUsers is one-shot per chain lifetime; run 'make reinit' first for a clean re-init)"; \
	else \
		echo "$(DEV_PASS)" | gnokey maketx run \
			-gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
			-chainid dev -remote $(RPC) \
			-home "$(DEV_HOME)" -insecure-password-stdin \
			dev init-govdao.gno; \
	fi
	@echo ">>> Step 2: validator4 pubkey (from committed secrets)"
	@echo "    address: $(VALIDATOR4_ADDR)"
	@echo "    pub_key: $(VALIDATOR4_PUB)"
	@echo ">>> Step 3: register validator4 as a valoper (signed by v4op operator account)"
	echo "$(DEV_PASS)" | gnokey maketx call \
		-pkgpath gno.land/r/gnops/valopers -func Register \
		-args "samourai-crew-4" \
		-args "Phase 2 test validator for gnomonitoring" \
		-args "on-prem" \
		-args $(VALIDATOR4_OP_ADDR) \
		-args $(VALIDATOR4_PUB) \
		-send 20000000ugnot -gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home "$(DEV_HOME)" -insecure-password-stdin v4op
	@echo ">>> Step 4: create GovDAO proposal (expects #0)"
	echo "$(DEV_PASS)" | gnokey maketx run \
		-gas-fee 1000000ugnot -gas-wanted 100000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home "$(DEV_HOME)" -insecure-password-stdin \
		dev propose-validator4.gno
	@echo ">>> Step 5: vote YES on proposal #0"
	echo "$(DEV_PASS)" | gnokey maketx call \
		-pkgpath gno.land/r/gov/dao -func MustVoteOnProposalSimple \
		-args 0 -args YES \
		-gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home "$(DEV_HOME)" -insecure-password-stdin dev
	@echo ">>> Step 6: execute proposal #0"
	echo "$(DEV_PASS)" | gnokey maketx call \
		-pkgpath gno.land/r/gov/dao -func ExecuteProposal \
		-args 0 \
		-gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home "$(DEV_HOME)" -insecure-password-stdin dev
	@echo ">>> Step 7: verify validator4 joined the valset"
	gnokey query vm/qeval \
		-data 'gno.land/r/sys/validators/v3.IsValidator("$(VALIDATOR4_ADDR)")' \
		-remote $(RPC)
	curl -s http://localhost:26661/status | jq '.result.validator_info'

scenario2:
	docker compose stop validator4
	@echo "validator4 stopped. Watch for a WARNING alert (>=5 missed blocks),"
	@echo "then CRITICAL (>=30 missed blocks)."

scenario2-restart:
	docker compose start validator4
	@echo "validator4 restarted. Expect a RESOLVED alert once it participates again."

scenario3:
	docker compose stop validator2 validator3
	@echo "validator2+validator3 stopped (11/31 power left, below the 2/3 supermajority)."
	@echo "Watch gnoland_chain_current_height (should stop increasing) and the"
	@echo "global stagnation/RPC-error alert."

scenario3-restart:
	docker compose start validator2 validator3
	@echo "If the chain doesn't resume producing blocks within ~1-2 min, run 'make reinit'."

# scenario4: total RPC outage. Distinct from scenario3 (stagnation): there the
# chain is reachable but stuck; here EVERY RPC endpoint is down, so the backend's
# fallback client can't reach the node at all. This alert is LOG-ONLY: it does
# NOT write to alert_logs and does NOT hit webhooks (see gnovalidator_realtime.go).
scenario4:
	docker compose --profile phase2 stop validator validator2 validator3 validator4
	@echo "All validators stopped -> every RPC endpoint is unreachable."
	@echo "Expect a LOG-ONLY alert (no alert_logs row, no webhook):"
	@echo "  docker logs gnomonitoring-backend | grep 'Error when querying latest block height'"
	@echo "It fires once, then is suppressed for the RPC-error cooldown (~10 min)."

scenario4-restart:
	docker compose --profile phase2 start validator validator2 validator3 validator4
	@echo "Validators restarted. The monitor loop should resume fetching heights."

# scenario5: GovDAO rejection path. Mirrors scenario1 but votes NO and never
# executes, so the proposal is rejected instead of onboarding validator4. This
# exercises the GovDAO watcher's CheckProposalStatus (status -> rejected/expired).
# Run on a FRESH chain (targets proposal #0): 'make reinit && make scenario5'.
scenario5: up-validator4
	@echo ">>> Rejected-proposal scenario (FRESH chain expected: targets proposal #0)"
	@echo ">>> Step 1: initialize GovDAO (skipped if already initialized)"
	@if gnokey query vm/qeval -remote $(RPC) \
		-data 'gno.land/r/gov/dao.AllowedDAOs()' 2>/dev/null | grep -q 'gno.land/r/gov/dao/v3/impl'; then \
		echo "    GovDAO already initialized - skipping."; \
	else \
		echo "$(DEV_PASS)" | gnokey maketx run \
			-gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
			-chainid dev -remote $(RPC) \
			-home "$(DEV_HOME)" -insecure-password-stdin \
			dev init-govdao.gno; \
	fi
	@echo ">>> Step 2: register validator4 as a valoper (signed by v4op)"
	echo "$(DEV_PASS)" | gnokey maketx call \
		-pkgpath gno.land/r/gnops/valopers -func Register \
		-args "samourai-crew-4" \
		-args "Phase 2 test validator for gnomonitoring" \
		-args "on-prem" \
		-args $(VALIDATOR4_OP_ADDR) \
		-args $(VALIDATOR4_PUB) \
		-send 20000000ugnot -gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home "$(DEV_HOME)" -insecure-password-stdin v4op
	@echo ">>> Step 3: create GovDAO proposal (expects #0)"
	echo "$(DEV_PASS)" | gnokey maketx run \
		-gas-fee 1000000ugnot -gas-wanted 100000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home "$(DEV_HOME)" -insecure-password-stdin \
		dev propose-validator4.gno
	@echo ">>> Step 4: vote NO on proposal #0 (rejection path - NOT executed)"
	echo "$(DEV_PASS)" | gnokey maketx call \
		-pkgpath gno.land/r/gov/dao -func MustVoteOnProposalSimple \
		-args 0 -args NO \
		-gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home "$(DEV_HOME)" -insecure-password-stdin dev
	@echo ">>> Done. Expect the GovDAO watcher to track it and mark it rejected/expired:"
	@echo "  docker exec gnomonitoring-postgres psql -U gnomonitoring -d gnomonitoring \\"
	@echo "    -c \"SELECT id, status FROM govdaos WHERE chain_id='dev';\""

# scenario-mute: NOT a separate chain action - it asserts the resend DEDUP
# (CLAUDE.md's "4 repeats -> MUTED" is outdated; the real rule is one alert per
# level per validator within the resend window: WARNING=6h, CRITICAL=24h, unless
# a RESOLVED was sent). Keep validator4 down across several alert-check cycles.
scenario-mute:
	docker compose stop validator4
	@echo "validator4 stopped. The FIRST WARNING/CRITICAL is written to alert_logs;"
	@echo "every later check within the resend window is SKIPPED (no new row)."
	@echo "Assert exactly ONE row per level:"
	@echo "  docker exec gnomonitoring-postgres psql -U gnomonitoring -d gnomonitoring \\"
	@echo "    -c \"SELECT level, count(*) FROM alert_logs WHERE chain_id='dev' AND addr='$(VALIDATOR4_ADDR)' GROUP BY level;\""
	@echo "And confirm the dedup log line appears:"
	@echo "  docker logs gnomonitoring-backend | grep 'dedup: skipping'"
	@echo "Restart with: make scenario2-restart"
