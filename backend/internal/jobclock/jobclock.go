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
//	minute 0      GovDAO proposal watcher (+0s), valset refresh (chains spread over +20s..+60s)
//	              BFT margin alert check (chains spread over +45s..+60s, a small query)
//	minutes 1-2   Prometheus metrics updater (+60s, chains run one after another)
//	minutes 3-4   latency alert check (chains spread over +180s..+300s)
//
// Missed-block alert checks (every alert_check_interval_seconds) are spread
// evenly across their own interval, one slot per chain.
//
// The plan assumes the default 5-minute periods. With an admin-configured
// period that is not 5 minutes, a job keeps its offset modulo its own period
// but may then share a minute with another job. Offsets depend on the chain's
// position among the enabled chains, so callers recompute them every cycle
// to follow chains added or removed at runtime.
package jobclock

import "time"

// Cycle is the period of the GovDAO watcher and the metrics updater, and the
// default period of the valset refresh and the latency and BFT alert checks.
const Cycle = 5 * time.Minute

const (
	govDAOPhase   = 0
	valsetPhase   = 20 * time.Second
	valsetWindow  = 40 * time.Second // valset refreshes stay inside minute 0
	metricsPhase  = 60 * time.Second
	latencyPhase  = 180 * time.Second
	latencyWindow = 120 * time.Second // latency checks stay inside minutes 3-4
	bftPhase      = 45 * time.Second
	bftWindow     = 15 * time.Second // BFT checks stay inside minute 0
	// alertPhase is not a multiple of 5s, so with the usual intervals and
	// chain counts the alert checks avoid the round seconds the other jobs use.
	alertPhase = 7 * time.Second
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
	return valsetPhase + spread(chainID, chains, valsetWindow)
}

// LatencyOffset is the phase of a chain's latency alert check.
func LatencyOffset(chainID string, chains []string) time.Duration {
	return latencyPhase + spread(chainID, chains, latencyWindow)
}

// BFTOffset is the phase of a chain's BFT margin alert check. It runs after
// most valset refreshes, so it usually reads a voting-power snapshot taken in
// the same cycle.
func BFTOffset(chainID string, chains []string) time.Duration {
	return bftPhase + spread(chainID, chains, bftWindow)
}

// spread gives each chain an even share of window, by its position in chains.
func spread(chainID string, chains []string, window time.Duration) time.Duration {
	n := len(chains)
	if n < 1 {
		n = 1
	}
	return time.Duration(ChainIndex(chainID, chains)) * window / time.Duration(n)
}

// AlertOffset is the phase of a chain's missed-block alert check inside its
// own interval: the chains share the interval evenly, starting at alertPhase.
func AlertOffset(chainID string, chains []string, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return (alertPhase + spread(chainID, chains, interval)) % interval
}
