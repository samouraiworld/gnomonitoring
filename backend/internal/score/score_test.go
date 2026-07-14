package score

import "testing"

func TestCompute_FlakyNoAlerts_BaseBelow100(t *testing.T) {
	// 90/100 signed, no alerts, no VP/proposer → base 90, no penalty.
	r := Compute(Inputs{SignedBlocks: 90, TotalBlocks: 100}, DefaultWeights())
	if r.Score != 90 {
		t.Fatalf("score = %d, want 90", r.Score)
	}
	if r.SignRate != 90 {
		t.Fatalf("signRate = %v, want 90", r.SignRate)
	}
	if r.ProposerScored {
		t.Fatalf("proposer should be dropped when no VP data")
	}
	if r.Tier != TierExcellent {
		t.Fatalf("tier = %s, want Excellent", r.Tier)
	}
}

func TestCompute_PerfectNoData(t *testing.T) {
	// No participation data at all → sign rate 0 guarded to base 0.
	r := Compute(Inputs{SignedBlocks: 0, TotalBlocks: 0}, DefaultWeights())
	if r.Score != 0 {
		t.Fatalf("score = %d, want 0 (no blocks → base 0)", r.Score)
	}
}

func TestCompute_WarningPenaltyApplied(t *testing.T) {
	// 100/100 signed, 3 warnings @ weight 2 = 6 penalty.
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, WarningCount: 3}, DefaultWeights())
	if r.Score != 94 {
		t.Fatalf("score = %d, want 94", r.Score)
	}
}

func TestCompute_CriticalAndDowntime(t *testing.T) {
	// 100/100 signed, 2 criticals (@6 = 12), 1000 downtime (@500/pt = 2).
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, CriticalCount: 2, DowntimeBlocks: 1000}, DefaultWeights())
	if r.Score != 86 {
		t.Fatalf("score = %d, want 86", r.Score)
	}
}

func TestCompute_VpSeverityRampsPenalty(t *testing.T) {
	// 100/100 signed, 5 criticals (@6=30 capped at 60 → 30), VP == max so severity = 1+0.5 = 1.5.
	// totalPen = 30 * 1.5 = 45 → score 55.
	r := Compute(Inputs{
		SignedBlocks: 100, TotalBlocks: 100, CriticalCount: 5,
		VotingPower: 1000, MaxVotingPower: 1000, SumVotingPower: 4000,
	}, DefaultWeights())
	if r.Score != 55 {
		t.Fatalf("score = %d, want 55", r.Score)
	}
}

func TestCompute_ProposerBlendWhenExpectedMet(t *testing.T) {
	// vpShare = 1000/4000 = 0.25; chainBlocks 1000 → expected 250 (>= min 5).
	// proposed 125 → ratio 0.5 → propRel 50. base = 100.
	// presence = (0.8*100 + 0.2*50)/1.0 = 90. No alerts. severity: VP==max → 1.5 but no penalty.
	r := Compute(Inputs{
		SignedBlocks: 100, TotalBlocks: 100,
		ProposedBlocks: 125, ChainBlocks: 1000,
		VotingPower: 1000, MaxVotingPower: 1000, SumVotingPower: 4000,
	}, DefaultWeights())
	if !r.ProposerScored {
		t.Fatalf("proposer should be scored when expected >= min")
	}
	if r.ProposerReliability != 50 {
		t.Fatalf("propRel = %v, want 50", r.ProposerReliability)
	}
	if r.Score != 90 {
		t.Fatalf("score = %d, want 90", r.Score)
	}
}

func TestCompute_ProposerDroppedWhenExpectedBelowMin(t *testing.T) {
	// Tiny VP: vpShare 1/4000, chainBlocks 1000 → expected 0.25 < min 5 → dropped.
	r := Compute(Inputs{
		SignedBlocks: 100, TotalBlocks: 100,
		ProposedBlocks: 0, ChainBlocks: 1000,
		VotingPower: 1, MaxVotingPower: 1000, SumVotingPower: 4000,
	}, DefaultWeights())
	if r.ProposerScored {
		t.Fatalf("proposer should be dropped for tiny VP")
	}
	if r.Score != 100 {
		t.Fatalf("score = %d, want 100 (presence == base)", r.Score)
	}
}

func TestCompute_FreqPenaltyApplied(t *testing.T) {
	// 100/100 signed, 2 distinct incidents, PeriodDays=1 (last_24h anchor):
	// rate = 2/1*7 = 14/week; penalty = 14*0.43 = 6.02 -> score round(100-6.02)=94.
	// Same numeric result as the pre-change raw-count formula (2*3=6) by design.
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, IncidentCount: 2, PeriodDays: 1}, DefaultWeights())
	if r.Score != 94 {
		t.Fatalf("score = %d, want 94", r.Score)
	}
	if r.IncidentRatePerWeek != 14 {
		t.Fatalf("incidentRatePerWeek = %v, want 14", r.IncidentRatePerWeek)
	}
}

func TestCompute_FreqPenaltyCapped(t *testing.T) {
	// 100/100 signed, 20 incidents, PeriodDays=1: rate=140/week, penalty=140*0.43=60.2, capped at 30.
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, IncidentCount: 20, PeriodDays: 1}, DefaultWeights())
	if r.Score != 70 {
		t.Fatalf("score = %d, want 70 (penalty capped at 30)", r.Score)
	}
}

func TestCompute_FreqWeightZeroIsNoOp(t *testing.T) {
	w := DefaultWeights()
	w.FreqWeight = 0
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, IncidentCount: 5, PeriodDays: 1}, w)
	if r.Score != 100 {
		t.Fatalf("score = %d, want 100 (FreqWeight=0 must be a no-op)", r.Score)
	}
}

func TestCompute_FreqPenaltyDilutedOverLongerPeriod(t *testing.T) {
	// Same IncidentCount as TestCompute_FreqPenaltyApplied, but spread over 30
	// elapsed days instead of 1: rate = 2/30*7 ≈ 0.467/week, penalty ≈ 0.2,
	// strictly smaller than the PeriodDays=1 case (6.02) — the core fix.
	r := Compute(Inputs{SignedBlocks: 100, TotalBlocks: 100, IncidentCount: 2, PeriodDays: 30}, DefaultWeights())
	if r.Score != 100 {
		t.Fatalf("score = %d, want 100 (penalty rounds away to ~0.2)", r.Score)
	}
	if r.IncidentRatePerWeek <= 0 || r.IncidentRatePerWeek >= 14 {
		t.Fatalf("incidentRatePerWeek = %v, want strictly between 0 and 14", r.IncidentRatePerWeek)
	}
}

func TestCompute_FreqPenaltyZeroPeriodDaysIsSafe(t *testing.T) {
	// PeriodDays unset (0, e.g. the empty score.Compute(Inputs{}, ...) report
	// path) must not divide by zero.
	r := Compute(Inputs{IncidentCount: 5}, DefaultWeights())
	if r.IncidentRatePerWeek != 0 {
		t.Fatalf("incidentRatePerWeek = %v, want 0 when PeriodDays is unset", r.IncidentRatePerWeek)
	}
}
