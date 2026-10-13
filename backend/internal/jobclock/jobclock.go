// Package jobclock spreads the periodic background jobs over fixed phases of a
// shared wall-clock cycle, so their database work no longer lands in the same
// minute.
//
// Before, every 5-minute loop started at boot and slept "interval" after each
// run, so the jobs fired together and drifted together, and the per-chain loops
// ran in lockstep. Scheduling on wall-clock slots (Unix time modulo the period,
// plus a fixed offset) keeps each job on its own phase however long a run takes:
// a run that overruns its slot skips to the next one instead of bunching up.
//
// The 5-minute plan:
//
//	minute 0      GovDAO proposal watcher (+0s), valset refresh (+20s, +10s per chain)
//	minutes 1-2   Prometheus metrics updater (+60s, chains run one after another)
//	minutes 3-4   latency alert check (+180s, +30s per chain, up to four chains)
//
// Missed-block alert checks (every alert_check_interval_seconds) are spread
// evenly across their own interval, one slot per chain.
package jobclock

import "time"

// Cycle is the period of the GovDAO watcher and the metrics updater, and the
// default period of the valset refresh and the latency alert check.
const Cycle = 5 * time.Minute

const (
	govDAOPhase        = 0
	valsetPhase        = 20 * time.Second
	valsetChainStride  = 10 * time.Second
	metricsPhase       = 60 * time.Second
	latencyPhase       = 180 * time.Second
	latencyChainStride = 30 * time.Second
	latencyChainSlots  = 4 // latencyPhase + slots*stride stays inside the cycle
	alertPhase         = 5 * time.Second
	valsetChainSlots   = 4 // valsetPhase + slots*stride stays inside minute 0
)

// UntilNext returns how long to wait from now until the next instant strictly
// after now whose Unix time, modulo period, equals offset. It returns 0 when
// period is not positive, so callers fall back to running immediately.
func UntilNext(now time.Time, period, offset time.Duration) time.Duration {
	if period <= 0 {
		return 0
	}
	offset %= period
	if offset < 0 {
		offset += period
	}
	pos := time.Duration(now.UnixNano()) % period
	wait := offset - pos
	if wait <= 0 {
		wait += period
	}
	return wait
}

// ChainIndex returns the position of chainID in chains (internal.EnabledChains,
// sorted), or 0 when it is absent.
func ChainIndex(chainID string, chains []string) int {
	for i, c := range chains {
		if c == chainID {
			return i
		}
	}
	return 0
}

// GovDAOOffset is the phase of the GovDAO proposal status watcher.
func GovDAOOffset() time.Duration { return govDAOPhase }

// MetricsOffset is the phase of the Prometheus metrics updater.
func MetricsOffset() time.Duration { return metricsPhase }

// ValsetOffset is the phase of a chain's valset refresh (InitMonikerMap).
func ValsetOffset(chainID string, chains []string) time.Duration {
	return valsetPhase + time.Duration(ChainIndex(chainID, chains)%valsetChainSlots)*valsetChainStride
}

// LatencyOffset is the phase of a chain's latency alert check.
func LatencyOffset(chainID string, chains []string) time.Duration {
	return latencyPhase + time.Duration(ChainIndex(chainID, chains)%latencyChainSlots)*latencyChainStride
}

// AlertOffset is the phase of a chain's missed-block alert check inside its
// own interval: the chains share the interval evenly, starting at +5s so the
// checks do not coincide with the slot boundaries the other jobs use.
func AlertOffset(chainID string, chains []string, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	n := len(chains)
	if n < 1 {
		n = 1
	}
	idx := ChainIndex(chainID, chains)
	return (alertPhase + time.Duration(idx)*interval/time.Duration(n)) % interval
}
