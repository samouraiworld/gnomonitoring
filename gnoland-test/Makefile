DEV_HOME := $(CURDIR)/.gnokey-dev
DEV_ADDR := g1fz9afdzgqsk2kw6sp44rjthcx25vz0e3ucgst5
DEV_PASS := devpassword123
RPC      := http://localhost:26658

VALIDATOR4_ADDR := g132qwl96yd6s40a4wer8jxsq7a04xy5pr0c4qj7
VALIDATOR4_PUB  := gpub1pggj7ard9eg82cjtv4u52epjx56nzwgjyg9zq0df6ddc3ns89ysxtxrc4g4zd3wtgz4heqgmguvzdpqkhj4t5zy6rskcxm
# Operator (gnokey) account for validator4 — separate from the BFT consensus key.
# In chain/test13, Register() requires caller == operator address (anti-squat guard).
VALIDATOR4_OP_ADDR := g1d7wuvd5qz83clvryls3wqk3ta9rqfqske0zm55

.PHONY: help up up-validator4 down reinit regen-genesis full-reinit status logs \
        stop-val1 stop-val2 stop-val3 stop-val4 \
        start-val1 start-val2 start-val3 start-val4 \
        scenario1 scenario2 scenario2-restart scenario3 scenario3-restart

help:
	@echo "Chain lifecycle:"
	@echo "  make up                - start validator/validator2/validator3 + tx-indexer + gnoweb"
	@echo "  make up-validator4     - start validator4 (phase2 profile, needed for scenario1)"
	@echo "  make down              - stop and remove all containers"
	@echo "  make reinit            - wipe chain state, restart from existing genesis.json"
	@echo "  make regen-genesis     - delete + regenerate genesis.json (after editing generate-genesis.sh)"
	@echo "  make full-reinit       - regen-genesis + reinit + up (use after changing powers/params)"
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
	@echo "  make scenario2         - stop validator2 (downtime/recovery alerts)"
	@echo "  make scenario2-restart - restart validator2"
	@echo "  make scenario3         - stop validator2+validator3 (halt the chain)"
	@echo "  make scenario3-restart - restart validator2+validator3"

up:
	docker compose up -d

up-validator4:
	docker compose --profile phase2 up -d validator4

down:
	docker compose down

reinit:
	./reinit-chain.sh
	docker compose up -d

regen-genesis:
	rm -f genesis.json
	./bootstrap.sh

full-reinit: regen-genesis
	./reinit-chain.sh
	docker compose up -d

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
			-home $(DEV_HOME) -insecure-password-stdin \
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
		-home $(DEV_HOME) -insecure-password-stdin v4op
	@echo ">>> Step 4: create GovDAO proposal (expects #0)"
	echo "$(DEV_PASS)" | gnokey maketx run \
		-gas-fee 1000000ugnot -gas-wanted 100000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home $(DEV_HOME) -insecure-password-stdin \
		dev propose-validator4.gno
	@echo ">>> Step 5: vote YES on proposal #0"
	echo "$(DEV_PASS)" | gnokey maketx call \
		-pkgpath gno.land/r/gov/dao -func MustVoteOnProposalSimple \
		-args 0 -args YES \
		-gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home $(DEV_HOME) -insecure-password-stdin dev
	@echo ">>> Step 6: execute proposal #0"
	echo "$(DEV_PASS)" | gnokey maketx call \
		-pkgpath gno.land/r/gov/dao -func ExecuteProposal \
		-args 0 \
		-gas-fee 1000000ugnot -gas-wanted 50000000 -broadcast \
		-chainid dev -remote $(RPC) \
		-home $(DEV_HOME) -insecure-password-stdin dev
	@echo ">>> Step 7: verify validator4 joined the valset"
	gnokey query vm/qeval \
		-data 'gno.land/r/sys/validators/v2.IsValidator("$(VALIDATOR4_ADDR)")' \
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
