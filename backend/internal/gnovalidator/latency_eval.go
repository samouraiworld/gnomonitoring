package gnovalidator

import "sort"

// latencyResolveRatio is the hysteresis margin: an active alert only resolves
// once the validator is comfortably back under the trigger thresholds, so a
// validator sitting right at the threshold does not flip between LATENCY and
// LATENCY_RESOLVED on every cycle.
const latencyResolveRatio = 0.6

// latencyMinPeers is the minimum number of other eligible validators needed to
// compute a meaningful peer median. Below that, latency is not evaluated at
// all rather than compared against one arbitrary neighbour.
const latencyMinPeers = 2

// latencyStat is one validator's signing-latency summary over the alert
// window. LateRatio is nil when no block in the window had a known quorum.
type latencyStat struct {
	Addr      string
	Moniker   string
	Samples   int
	P50       float64
	P90       float64
	LateRatio *float64
	MinHeight int64
	MaxHeight int64
}

type latencyVerdict string

const (
	verdictTrigger      latencyVerdict = "trigger"
	verdictResolve      latencyVerdict = "resolve"
	verdictHold         latencyVerdict = "hold"
	verdictNotEvaluated latencyVerdict = "not_evaluated"
)

// latencyEval is the verdict for one validator plus the numbers behind it,
// which the alert message quotes verbatim.
type latencyEval struct {
	Stat          latencyStat
	PeerMedianP50 float64
	Verdict       latencyVerdict
}

// evaluateLatency classifies every validator in stats. A validator triggers
// when its median lag is at once above the absolute floor and at least
// peer_factor times its peers' median — the peer comparison alone would fire
// on a healthy but fast chain, and the floor alone would need per-chain
// tuning. Voting power is deliberately not involved: unlike late_for_quorum,
// the lag does not depend on it.
func evaluateLatency(stats []latencyStat, cfg Thresholds) map[string]latencyEval {
	result := make(map[string]latencyEval, len(stats))

	eligible := make([]latencyStat, 0, len(stats))
	for _, s := range stats {
		if s.Samples >= cfg.LatencyAlertMinSamples {
			eligible = append(eligible, s)
			continue
		}
		result[s.Addr] = latencyEval{Stat: s, Verdict: verdictNotEvaluated}
	}

	for _, s := range eligible {
		peers := make([]float64, 0, len(eligible)-1)
		for _, other := range eligible {
			if other.Addr != s.Addr {
				peers = append(peers, other.P50)
			}
		}
		if len(peers) < latencyMinPeers {
			result[s.Addr] = latencyEval{Stat: s, Verdict: verdictNotEvaluated}
			continue
		}

		peerMedian := median(peers)
		peerThreshold := cfg.LatencyAlertPeerFactor * peerMedian
		minLag := float64(cfg.LatencyAlertMinLagMs)

		verdict := verdictHold
		switch {
		case s.P50 >= minLag && s.P50 >= peerThreshold:
			verdict = verdictTrigger
		case s.P50 < latencyResolveRatio*minLag || s.P50 < latencyResolveRatio*peerThreshold:
			verdict = verdictResolve
		}
		result[s.Addr] = latencyEval{Stat: s, PeerMedianP50: peerMedian, Verdict: verdict}
	}

	return result
}

// median returns the median of values (mean of the two middle values for an
// even count). values is copied before sorting so the caller's slice is
// untouched.
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
