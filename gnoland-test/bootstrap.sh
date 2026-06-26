#!/usr/bin/env bash
# One-shot setup for the local Gno devnet (3 genesis validators + a 4th
# identity reserved for the Phase 2 "new validator via GovDAO" scenario).
#
# Safe to re-run: every step is idempotent and skips work that was already
# done (images already built, secrets/genesis/dev account already
# generated). Nothing generated here is committed to git. To wipe everything
# and regenerate from scratch (fresh keys + addresses), run `make clean-all`.
#
# Usage:
#   GNO_REPO_PATH=/path/to/gno ./bootstrap.sh
#
# GNO_REPO_PATH defaults to /home/louis/gno.
set -euo pipefail

cd "$(dirname "$0")"

GNO_REPO_PATH="${GNO_REPO_PATH:-/home/$USER/gno}"
# Runtime image used by docker-compose for all 4 validators.
# Priority: env var > existing .env value > default.
# Override: GNO_IMAGE=chain:test-13 ./bootstrap.sh
if [ -z "${GNO_IMAGE:-}" ] && [ -f .env ]; then
  GNO_IMAGE="$(grep -E '^GNO_IMAGE=' .env | cut -d= -f2- | head -1)"
fi
GNO_IMAGE="${GNO_IMAGE:-gno-validator:local}"

# Image providing the gnogenesis binary + /gnoroot/examples for genesis generation.
# MUST be built from the same source as GNO_IMAGE (same branch/commit).
# When GNO_IMAGE is a custom build (e.g. chain:test-13), build the matching
# contribs image first and set GNO_CONTRIBS_IMAGE accordingly:
#   docker build --target gnocontribs -t gno-contribs:test-13 /path/to/gno
#   GNO_CONTRIBS_IMAGE=gno-contribs:test-13 ./bootstrap.sh
if [ -z "${GNO_CONTRIBS_IMAGE:-}" ] && [ -f .env ]; then
  GNO_CONTRIBS_IMAGE="$(grep -E '^GNO_CONTRIBS_IMAGE=' .env | cut -d= -f2- | head -1)"
fi
GNO_CONTRIBS_IMAGE="${GNO_CONTRIBS_IMAGE:-gno-contribs:local}"
DOCKER_USER="$(id -u):$(id -g)"
NODES=(validator validator2 validator3 validator4)

# Local-only dev account, generated into .gnokey-dev/ (gitignored, never
# committed). Used as the genesis "unrestricted" address (auth.unrestricted_addrs)
# so it can register valopers / submit GovDAO proposals without paying realm
# storage deposits during testing. Regenerated (new address) on every fresh setup.
DEV_PASSPHRASE="devpassword123"

echo "==> [1/7] Building bootstrap images"
if [ "$GNO_IMAGE" = "gno-validator:local" ]; then
  echo "    GNO_IMAGE=gno-validator:local → building gnoland from $GNO_REPO_PATH"
  docker build --target gnoland -t gno-validator:local "$GNO_REPO_PATH"
else
  echo "    GNO_IMAGE=$GNO_IMAGE → skipping local gnoland build"
fi
if [ "$GNO_CONTRIBS_IMAGE" = "gno-contribs:local" ]; then
  echo "    GNO_CONTRIBS_IMAGE=gno-contribs:local → building gnocontribs from $GNO_REPO_PATH"
  docker build --target gnocontribs -t gno-contribs:local "$GNO_REPO_PATH"
else
  echo "    GNO_CONTRIBS_IMAGE=$GNO_CONTRIBS_IMAGE → using pre-built contribs image"
fi
docker build --target gnokey -t gno-gnokey:local "$GNO_REPO_PATH"

echo "==> [2/7] Generating node secrets"
for node in "${NODES[@]}"; do
  mkdir -p "$node/gnoland-data/secrets"
  if [ -f "$node/gnoland-data/secrets/priv_validator_key.json" ]; then
    echo "    -> $node (existing secrets, skipping)"
    # Reset the signing state so a stale priv_validator_state.json (non-zero
    # height/signature from a previous run) doesn't make step [4/7] fail with
    # "invalid sign state sign bytes". Regenerating genesis restarts at height 0
    # anyway, so zeroing the state here is always safe and makes full-reinit
    # self-healing.
    printf '{"height":"0","round":"0","step":0}\n' \
      > "$node/gnoland-data/secrets/priv_validator_state.json"
  else
    echo "    -> $node"
    docker run --rm --user "$DOCKER_USER" \
      --entrypoint /usr/bin/gnoland \
      -v "$PWD/$node/gnoland-data:/gnoroot/gnoland-data" \
      "$GNO_IMAGE" secrets init
  fi
done

echo "==> [3/7] Generating shared base config.toml"
if [ -f config.toml ]; then
  echo "    -> config.toml (already exists, skipping)"
else
  docker run --rm --user "$DOCKER_USER" \
    --entrypoint /usr/bin/gnoland \
    -v "$PWD:/work" -w /work \
    "$GNO_IMAGE" config init -config-path config.toml
fi
for node in "${NODES[@]}"; do
  cp config.toml "$node/config.toml"
done

echo "==> [4/7] Extracting addresses / pubkeys / node IDs"
get_secret() {
  docker run --rm --user "$DOCKER_USER" \
    --entrypoint /usr/bin/gnoland \
    -v "$PWD/$1/gnoland-data:/gnoroot/gnoland-data" \
    "$GNO_IMAGE" secrets get "$2" -raw
}

VALIDATOR_ADDR=$(get_secret validator validator_key.address)
VALIDATOR_PUBKEY=$(get_secret validator validator_key.pub_key)
VALIDATOR_NODEID=$(get_secret validator node_id.id)

VALIDATOR2_ADDR=$(get_secret validator2 validator_key.address)
VALIDATOR2_PUBKEY=$(get_secret validator2 validator_key.pub_key)
VALIDATOR2_NODEID=$(get_secret validator2 node_id.id)

VALIDATOR3_ADDR=$(get_secret validator3 validator_key.address)
VALIDATOR3_PUBKEY=$(get_secret validator3 validator_key.pub_key)
VALIDATOR3_NODEID=$(get_secret validator3 node_id.id)

VALIDATOR4_ADDR=$(get_secret validator4 validator_key.address)
VALIDATOR4_PUBKEY=$(get_secret validator4 validator_key.pub_key)
VALIDATOR4_NODEID=$(get_secret validator4 node_id.id)

echo "    validator   addr=$VALIDATOR_ADDR  node_id=$VALIDATOR_NODEID"
echo "    validator2  addr=$VALIDATOR2_ADDR  node_id=$VALIDATOR2_NODEID"
echo "    validator3  addr=$VALIDATOR3_ADDR  node_id=$VALIDATOR3_NODEID"
echo "    validator4  addr=$VALIDATOR4_ADDR  node_id=$VALIDATOR4_NODEID (phase2, not in genesis)"

echo "==> [5/7] Writing .env (P2P seed addresses + runtime image)"
cat > .env <<EOF
# Generated by bootstrap.sh - peer addresses derived from each node's
# node_key.json. Re-generating secrets requires re-running bootstrap.sh
# to refresh these values.
SEEDS_VALIDATOR=${VALIDATOR2_NODEID}@validator2:26656,${VALIDATOR3_NODEID}@validator3:26656
SEEDS_VALIDATOR2=${VALIDATOR_NODEID}@validator:26656,${VALIDATOR3_NODEID}@validator3:26656
SEEDS_VALIDATOR3=${VALIDATOR_NODEID}@validator:26656,${VALIDATOR2_NODEID}@validator2:26656
SEEDS_VALIDATOR4=${VALIDATOR_NODEID}@validator:26656,${VALIDATOR2_NODEID}@validator2:26656,${VALIDATOR3_NODEID}@validator3:26656

# Host user identity — containers run as this user so gnoland-data files
# are owned by the host user and reinit-chain.sh can clean them without sudo.
DOCKER_USER=${DOCKER_USER}

# Gnoland validator image used by docker-compose.yml.
# Override to pin a specific build instead of the locally built image.
# Example: GNO_IMAGE=chain:test-13 ./bootstrap.sh
GNO_IMAGE=${GNO_IMAGE}

# Gnogenesis + examples image used for genesis generation.
# Must be built from the same source as GNO_IMAGE.
# Example: docker build --target gnocontribs -t gno-contribs:test-13 /path/to/gno
GNO_CONTRIBS_IMAGE=${GNO_CONTRIBS_IMAGE}
EOF

echo "==> [6/7] Generating local dev accounts (gnokey)"
if [ -d .gnokey-dev/data ]; then
  echo "    -> .gnokey-dev (already exists, skipping)"
else
  # Pre-create the keybase dir as the host user. Otherwise Docker auto-creates
  # the bind-mount source as root, and gnokey (running as $DOCKER_USER) can't
  # write keybase/data -> "mkdir ... permission denied".
  mkdir -p .gnokey-dev
  printf '%s\n%s\n' "$DEV_PASSPHRASE" "$DEV_PASSPHRASE" | docker run --rm -i --user "$DOCKER_USER" \
    -v "$PWD/.gnokey-dev:/gnoroot/keybase" \
    gno-gnokey:local add -insecure-password-stdin -home /gnoroot/keybase dev
  printf '%s\n%s\n' "$DEV_PASSPHRASE" "$DEV_PASSPHRASE" | docker run --rm -i --user "$DOCKER_USER" \
    -v "$PWD/.gnokey-dev:/gnoroot/keybase" \
    gno-gnokey:local add -insecure-password-stdin -home /gnoroot/keybase v4op
fi
KEYLIST=$(docker run --rm --user "$DOCKER_USER" -v "$PWD/.gnokey-dev:/gnoroot/keybase" \
  gno-gnokey:local list -home /gnoroot/keybase 2>/dev/null)
DEV_ADDR=$(echo "$KEYLIST" | grep "^0\." | sed -n 's/.*addr: \(g1[a-z0-9]*\).*/\1/p')
V4OP_ADDR=$(echo "$KEYLIST" | grep " v4op " | sed -n 's/.*addr: \(g1[a-z0-9]*\).*/\1/p')
echo "    dev  addr=$DEV_ADDR"
echo "    v4op addr=$V4OP_ADDR"

echo "==> Rendering GovDAO scripts from templates (addresses injected at runtime)"
sed "s/__DEV_ADDR__/$DEV_ADDR/g" init-govdao.gno.tmpl > init-govdao.gno
sed "s/__V4OP_ADDR__/$V4OP_ADDR/g" propose-validator4.gno.tmpl > propose-validator4.gno
echo "    -> init-govdao.gno (dev=$DEV_ADDR)"
echo "    -> propose-validator4.gno (v4op=$V4OP_ADDR)"

echo "==> Writing .devaddrs.mk (live addresses for the Makefile scenario targets)"
cat > .devaddrs.mk <<EOF
# Generated by bootstrap.sh - DO NOT COMMIT. Regenerated whenever keys change.
# Included by the Makefile (-include) so scenario targets use the live addresses
# instead of hardcoded ones.
VALIDATOR4_ADDR := ${VALIDATOR4_ADDR}
VALIDATOR4_PUB := ${VALIDATOR4_PUBKEY}
VALIDATOR4_OP_ADDR := ${V4OP_ADDR}
DEV_ADDR := ${DEV_ADDR}
EOF

echo "==> [7/7] Generating genesis_balances.txt and genesis.json"
cat > genesis_balances.txt <<EOF
# Local devnet balances - throwaway accounts, generously funded for testing.
${VALIDATOR_ADDR}=10000000000000ugnot  # samourai-crew-1
${VALIDATOR2_ADDR}=10000000000000ugnot # samourai-crew-2
${VALIDATOR3_ADDR}=10000000000000ugnot # samourai-crew-3
${VALIDATOR4_ADDR}=10000000000000ugnot # samourai-crew-4 BFT signing address
${DEV_ADDR}=10000000000000ugnot        # dev (unrestricted, .gnokey-dev)
${V4OP_ADDR}=10000000000000ugnot       # v4op (validator4 operator account)
EOF

if [ -f genesis.json ]; then
  echo "    -> genesis.json (already exists, skipping generation)"
else
  GNOGENESIS_IMG="$GNO_CONTRIBS_IMAGE" \
  VALIDATOR1_ADDR="$VALIDATOR_ADDR" VALIDATOR1_PUBKEY="$VALIDATOR_PUBKEY" VALIDATOR1_NAME="samourai-crew-1" \
  VALIDATOR2_ADDR="$VALIDATOR2_ADDR" VALIDATOR2_PUBKEY="$VALIDATOR2_PUBKEY" VALIDATOR2_NAME="samourai-crew-2" \
  VALIDATOR3_ADDR="$VALIDATOR3_ADDR" VALIDATOR3_PUBKEY="$VALIDATOR3_PUBKEY" VALIDATOR3_NAME="samourai-crew-3" \
  UNRESTRICTED_ADDR="$DEV_ADDR" \
    ./generate-genesis.sh
fi

for node in "${NODES[@]}"; do
  cp genesis.json "$node/genesis.json"
done

echo ""
echo "Bootstrap complete. Start the devnet with: docker compose up -d"
