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
