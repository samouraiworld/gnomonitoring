#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  backend|block_exporter) module=$1 ;;
  *) echo 'Expected backend or block_exporter.' >&2; exit 2 ;;
esac
repository=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repository/$module"

# Keep the module-wide inventory visible even when an affected package is not
# called. Exit 3 means findings; all scanner/database/toolchain errors still fail.
inventory_status=0
govulncheck -scan module || inventory_status=$?
if [[ "$inventory_status" != 0 && "$inventory_status" != 3 ]]; then
  exit "$inventory_status"
fi

# Block affected imported packages, including test dependencies, even when no
# vulnerable symbol is called by the current binary entry point.
govulncheck -scan package -test ./...

# Text mode propagates vulnerability findings. JSON/SARIF mode exits zero even
# when it reports vulnerabilities, so it must not be used as a blocking gate.
govulncheck -test -show traces ./...
