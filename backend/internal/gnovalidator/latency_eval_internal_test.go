package gnovalidator

import "testing"

func cfgForTest() Thresholds {
	return Thresholds{LatencyAlertMinLagMs: 50, LatencyAlertPeerFactor: 3, LatencyAlertMinSamples: 300}
}

func stat(addr string, p50 float64, samples int) latencyStat {
	return latencyStat{Addr: addr, Moniker: addr, Samples: samples, P50: p50, P90: p50 * 2, MinHeight: 1, MaxHeight: 1000}
}

func verdictOf(t *testing.T, got map[string]latencyEval, addr string) latencyVerdict {
	t.Helper()
	e, ok := got[addr]
	if !ok {
		t.Fatalf("%s missing from evaluation", addr)
	}
	return e.Verdict
}

// gnoland-1 before the config fix: 67 ms against peers at ~8 ms.
func TestEvaluateLatency_TriggersOnSlowValidator(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("slow", 67, 1000), stat("a", 8, 1000), stat("b", 7, 1000), stat("c", 9, 1000),
	}, cfgForTest())

	if v := verdictOf(t, got, "slow"); v != verdictTrigger {
		t.Errorf("slow verdict = %s, want trigger", v)
	}
	if got["slow"].PeerMedianP50 != 8 {
		t.Errorf("peer median = %v, want 8 (median of 7, 8, 9)", got["slow"].PeerMedianP50)
	}
	for _, addr := range []string{"a", "b", "c"} {
		if v := verdictOf(t, got, addr); v != verdictResolve {
			t.Errorf("%s verdict = %s, want resolve (healthy validators are below both resolve thresholds)", addr, v)
		}
	}
}

// After the fix: 9 ms is below 0.6 * 50.
func TestEvaluateLatency_ResolvesAfterFix(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("fixed", 9, 1000), stat("a", 8, 1000), stat("b", 7, 1000), stat("c", 9, 1000),
	}, cfgForTest())

	if v := verdictOf(t, got, "fixed"); v != verdictResolve {
		t.Errorf("verdict = %s, want resolve", v)
	}
}

// 40 ms with peers at 8: below the 50 ms floor (so not a trigger) but above
// 0.6*50 = 30, so it must not resolve either.
func TestEvaluateLatency_HoldBetweenThresholds(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("mid", 40, 1000), stat("a", 8, 1000), stat("b", 7, 1000), stat("c", 9, 1000),
	}, cfgForTest())

	if v := verdictOf(t, got, "mid"); v != verdictHold {
		t.Errorf("verdict = %s, want hold", v)
	}
}

// Exactly at both thresholds triggers (>=, not >).
func TestEvaluateLatency_ExactThresholdTriggers(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("edge", 50, 1000), stat("a", 50.0/3, 1000), stat("b", 50.0/3, 1000), stat("c", 50.0/3, 1000),
	}, cfgForTest())

	if v := verdictOf(t, got, "edge"); v != verdictTrigger {
		t.Errorf("verdict = %s, want trigger at exactly min lag and exactly factor x peer median", v)
	}
}

func TestEvaluateLatency_NotEnoughSamples(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("thin", 500, 299), stat("a", 8, 1000), stat("b", 7, 1000), stat("c", 9, 1000),
	}, cfgForTest())

	if v := verdictOf(t, got, "thin"); v != verdictNotEvaluated {
		t.Errorf("verdict = %s, want not_evaluated below min samples", v)
	}
}

func TestEvaluateLatency_NotEnoughPeers(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("slow", 500, 1000), stat("a", 8, 1000),
	}, cfgForTest())

	for _, addr := range []string{"slow", "a"} {
		if v := verdictOf(t, got, addr); v != verdictNotEvaluated {
			t.Errorf("%s verdict = %s, want not_evaluated with fewer than 2 peers", addr, v)
		}
	}
}

// Peer median 0 (a fast local chain): the absolute floor alone decides.
func TestEvaluateLatency_ZeroPeerMedian(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("slow", 149, 1000), stat("a", 0, 1000), stat("b", 0, 1000), stat("c", 0, 1000),
	}, cfgForTest())

	if v := verdictOf(t, got, "slow"); v != verdictTrigger {
		t.Errorf("verdict = %s, want trigger", v)
	}
	if v := verdictOf(t, got, "a"); v != verdictResolve {
		t.Errorf("a verdict = %s, want resolve", v)
	}
}

// Even peer count: median is the mean of the two middle values.
func TestEvaluateLatency_EvenPeerCountMedian(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("x", 100, 1000), stat("a", 2, 1000), stat("b", 4, 1000), stat("c", 6, 1000), stat("d", 8, 1000),
	}, cfgForTest())

	if got["x"].PeerMedianP50 != 5 {
		t.Errorf("peer median = %v, want 5 (mean of 4 and 6)", got["x"].PeerMedianP50)
	}
}

// A validator must never be part of its own peer median.
func TestEvaluateLatency_ExcludesSelfFromPeers(t *testing.T) {
	got := evaluateLatency([]latencyStat{
		stat("slow1", 200, 1000), stat("slow2", 200, 1000), stat("a", 5, 1000),
	}, cfgForTest())

	if got["slow1"].PeerMedianP50 != 102.5 {
		t.Errorf("peer median for slow1 = %v, want 102.5 (mean of 5 and 200, self excluded)", got["slow1"].PeerMedianP50)
	}
}
