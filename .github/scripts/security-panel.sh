#!/usr/bin/env bash
set -euo pipefail

repository=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
audit_report=$(mktemp)
trap 'rm -f -- "$audit_report"' EXIT
cd "$repository/panel"

# Include the build dependencies: the panel artifact is produced by Vite and its
# complete dependency tree, not only by the browser's runtime dependencies.
audit_status=0
npm audit --json --audit-level=high --include=dev --include=optional --include=peer > "$audit_report" || audit_status=$?
node "$repository/.github/scripts/check-npm-audit.mjs" "$audit_report" "$audit_status"
