#!/usr/bin/env bash
# check-chain-scoping.sh — heuristic CI gate for CLAUDE.md's "DB query WHERE
# chain_id scoping" rule: every query against daily_participations,
# alert_logs, addr_monikers, or telegram_validator_subs must be scoped by
# chain_id, or it silently leaks/aggregates data across chains.
#
# For each function touched by the diff, if it mentions one of those tables
# but never mentions chain_id anywhere in the same function body, flag it.
# Heuristic, not proof: it can't see scoping applied by a caller, and it
# will miss violations split across helper functions. Meant to catch the
# obvious "forgot the WHERE clause" case cheaply, not replace review.
set -euo pipefail

BASE="${BASE_SHA:-$(git merge-base origin/main HEAD)}"
TABLES='daily_participations|alert_logs|addr_monikers|telegram_validator_subs'

violations=0

# -W shows the whole enclosing function for each hunk, so a scoping clause
# a few lines away from the new SQL still counts as covering it.
diff_output="$(git diff -W "$BASE"...HEAD -- 'backend/**/*.go' || true)"
[ -z "$diff_output" ] && exit 0

while IFS= read -r -d '' hunk; do
    if grep -qE "$TABLES" <<<"$hunk" && ! grep -q 'chain_id' <<<"$hunk"; then
        echo "::error::Query touching a chain-scoped table with no chain_id in the same function (see CLAUDE.md: DB query WHERE chain_id scoping):"
        echo "$hunk" | head -20
        violations=$((violations + 1))
    fi
done < <(awk '/^@@/{if(buf)printf "%s\0",buf; buf=""} {buf=buf $0 "\n"} END{if(buf)printf "%s\0",buf}' <<<"$diff_output")

if [ "$violations" -gt 0 ]; then
    echo "::error::${violations} chain-scoping violation(s) found."
    exit 1
fi
