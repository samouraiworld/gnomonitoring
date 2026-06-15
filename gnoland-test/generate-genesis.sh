#!/usr/bin/env bash
# Generates gnoland-test/genesis.json for the local devnet.
#
# Not meant to be run directly: bootstrap.sh exports the validator
# addresses/pubkeys/names and the dev (unrestricted) account address as
# environment variables, then calls this script.
set -euo pipefail

cd "$(dirname "$0")"

GENESIS_FILE="genesis.json"
BALANCE_FILE="genesis_balances.txt"
GNO_REPOS="/gnoroot/examples/gno.land"
GNOGENESIS_IMG="${GNOGENESIS_IMG:-gno-contribs:local}"

: "${VALIDATOR1_ADDR:?}" "${VALIDATOR1_PUBKEY:?}" "${VALIDATOR1_NAME:?}"
: "${VALIDATOR2_ADDR:?}" "${VALIDATOR2_PUBKEY:?}" "${VALIDATOR2_NAME:?}"
: "${VALIDATOR3_ADDR:?}" "${VALIDATOR3_PUBKEY:?}" "${VALIDATOR3_NAME:?}"
: "${UNRESTRICTED_ADDR:?}"

run_gnogenesis() {
  docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" -w /work "$GNOGENESIS_IMG" "gnogenesis $*"
}

echo "Generating genesis..."

rm -f "$GENESIS_FILE"

echo "Running gnogenesis generate..."
run_gnogenesis generate

echo "Adding validators..."
run_gnogenesis "validator add --address $VALIDATOR1_ADDR --pub-key $VALIDATOR1_PUBKEY --name $VALIDATOR1_NAME --power 10 --genesis-path $GENESIS_FILE"
run_gnogenesis "validator add --address $VALIDATOR2_ADDR --pub-key $VALIDATOR2_PUBKEY --name $VALIDATOR2_NAME --power 10 --genesis-path $GENESIS_FILE"
run_gnogenesis "validator add --address $VALIDATOR3_ADDR --pub-key $VALIDATOR3_PUBKEY --name $VALIDATOR3_NAME --power 11 --genesis-path $GENESIS_FILE"

if [ ! -f "$BALANCE_FILE" ]; then
  echo "genesis_balances.txt not found" >&2
  exit 1
fi

echo "Adding balances..."
run_gnogenesis "balances add --balance-sheet $BALANCE_FILE --genesis-path $GENESIS_FILE"

echo "Adding packages from $GNO_REPOS..."
run_gnogenesis "txs add packages --genesis-path $GENESIS_FILE $GNO_REPOS"

echo "Setting unrestricted address..."
run_gnogenesis "params set auth.unrestricted_addrs $UNRESTRICTED_ADDR --genesis-path $GENESIS_FILE"

echo "Genesis successfully generated at $GENESIS_FILE"
