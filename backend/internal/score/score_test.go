package score

import "testing"

func TestComputeClean(t *testing.T) {
	s, tier := Compute(0, 0, DefaultWeights())
	if s != 100 || tier != TierExcellent {
		t.Fatalf("clean validator: got (%d,%s), want (100,Excellent)", s, tier)
	}
}

func TestComputeCriticalPenalty(t *testing.T) {
	// 2 criticals * -6 = -12 -> 88 -> Excellent
	s, tier := Compute(2, 0, DefaultWeights())
	if s != 88 || tier != TierExcellent {
		t.Fatalf("got (%d,%s), want (88,Excellent)", s, tier)
	}
}

func TestComputeCriticalCap(t *testing.T) {
	// 100 criticals capped at -60 -> 40 -> Watch
	s, tier := Compute(100, 0, DefaultWeights())
	if s != 40 || tier != TierWatch {
		t.Fatalf("got (%d,%s), want (40,Watch)", s, tier)
	}
}

func TestComputeDowntimePenalty(t *testing.T) {
	// 1500 blocks / 500 = -3, no criticals -> 97 -> Excellent
	s, tier := Compute(0, 1500, DefaultWeights())
	if s != 97 || tier != TierExcellent {
		t.Fatalf("got (%d,%s), want (97,Excellent)", s, tier)
	}
}

func TestComputeDowntimeCap(t *testing.T) {
	// 100000 blocks capped at -20 -> 80 -> Good
	s, tier := Compute(0, 100000, DefaultWeights())
	if s != 80 || tier != TierGood {
		t.Fatalf("got (%d,%s), want (80,Good)", s, tier)
	}
}

func TestComputeFloorAndCriticalTier(t *testing.T) {
	// 100 criticals (-60) + huge downtime (-20) = -80 -> 20 -> Critical
	s, tier := Compute(100, 100000, DefaultWeights())
	if s != 20 || tier != TierCritical {
		t.Fatalf("got (%d,%s), want (20,Critical)", s, tier)
	}
}

// TestTierBoundaries pins the exact tier thresholds so an off-by-one
// regression in tierFor (e.g. `>` vs `>=`) is caught. It calls the unexported
// tierFor directly to isolate the boundary logic from penalty arithmetic.
func TestTierBoundaries(t *testing.T) {
	cases := []struct {
		score int
		want  Tier
	}{
		{100, TierExcellent},
		{85, TierExcellent},
		{84, TierGood},
		{60, TierGood},
		{59, TierWatch},
		{30, TierWatch},
		{29, TierCritical},
		{0, TierCritical},
	}
	for _, c := range cases {
		if got := tierFor(c.score); got != c.want {
			t.Errorf("tierFor(%d) = %s, want %s", c.score, got, c.want)
		}
	}
}

// TestComputeUpperClamp guards the public function against negative-input
// callers producing a score above 100.
func TestComputeUpperClamp(t *testing.T) {
	// Negative criticalCount would otherwise yield a positive penalty offset
	// pushing the score above 100.
	s, tier := Compute(-5, 0, DefaultWeights())
	if s != 100 || tier != TierExcellent {
		t.Fatalf("got (%d,%s), want (100,Excellent)", s, tier)
	}
}

// TestComputeZeroDowntimeDivisor covers the division-by-zero guard branch:
// with DowntimeBlocksPerPoint = 0 the downtime must contribute no penalty and
// must not panic.
func TestComputeZeroDowntimeDivisor(t *testing.T) {
	w := DefaultWeights()
	w.DowntimeBlocksPerPoint = 0
	s, tier := Compute(0, 100000, w)
	if s != 100 || tier != TierExcellent {
		t.Fatalf("got (%d,%s), want (100,Excellent)", s, tier)
	}
}
