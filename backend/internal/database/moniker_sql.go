package database

import "fmt"

// MonikerFallback selects what MonikerSQL returns when no usable name exists.
type MonikerFallback int

const (
	// MonikerFallbackEmpty resolves to '' (callers render their own placeholder).
	MonikerFallbackEmpty MonikerFallback = iota
	// MonikerFallbackAddr resolves to the validator address (Prometheus
	// labels and Telegram lists, which must never show an empty name).
	MonikerFallbackAddr
)

// MonikerSQL returns the single SQL expression every moniker-returning query
// uses to name a validator. chainCol and addrCol are SQL column expressions
// identifying the validator in the enclosing query; in a GROUP BY query both
// must be grouped columns.
//
// Resolution order, ignoring empty names and the "unknown" placeholder at
// every step:
//  1. addr_monikers, the live name refreshed by InitMonikerMap;
//  2. the moniker frozen on the validator's latest daily_participations row
//     (that one row only, so the lookup never walks a week of raw rows);
//  3. the moniker frozen on its latest named daily_participation_agregas day
//     (raw rows are pruned after the retention window);
//  4. the moniker frozen on its latest alert_logs row, for addresses that
//     only exist there (the chain-wide 'all' and 'rpc' rows);
//  5. the fallback: an empty string or the address.
//
// The frozen columns are written once at insert time and go stale on a
// rename, so they only serve when addr_monikers has no name. Steps 2 to 4
// take the most recent name, never MAX(moniker): MAX returns the lexically
// largest name a validator ever had, not its current one. Each step is a
// self-contained scalar subquery on an indexed (chain_id, addr, ...) prefix,
// so callers add no JOIN, and COALESCE stops at the first non-NULL step.
func MonikerSQL(chainCol, addrCol string, fallback MonikerFallback) string {
	last := "''"
	if fallback == MonikerFallbackAddr {
		last = addrCol
	}
	return fmt.Sprintf(`COALESCE(
		(SELECT NULLIF(NULLIF(mr_am.moniker, ''), 'unknown') FROM addr_monikers mr_am
			WHERE mr_am.chain_id = %[1]s AND mr_am.addr = %[2]s),
		(SELECT NULLIF(NULLIF(mr_dp.moniker, ''), 'unknown') FROM daily_participations mr_dp
			WHERE mr_dp.chain_id = %[1]s AND mr_dp.addr = %[2]s
			ORDER BY mr_dp.block_height DESC LIMIT 1),
		(SELECT mr_dpa.moniker FROM daily_participation_agregas mr_dpa
			WHERE mr_dpa.chain_id = %[1]s AND mr_dpa.addr = %[2]s
			  AND mr_dpa.moniker NOT IN ('', 'unknown')
			ORDER BY mr_dpa.block_date DESC LIMIT 1),
		(SELECT mr_al.moniker FROM alert_logs mr_al
			WHERE mr_al.chain_id = %[1]s AND mr_al.addr = %[2]s
			  AND mr_al.moniker NOT IN ('', 'unknown')
			ORDER BY mr_al.id DESC LIMIT 1),
		%[3]s)`, chainCol, addrCol, last)
}
