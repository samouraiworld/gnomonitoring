// Package score turns raw per-validator metrics into a 0-100 health score and
// a human-readable tier. It is a pure package with no DB dependency so the
// scoring policy can be unit-tested in isolation.
package score

import (
	"math"
	"strconv"
)

type Tier string

const (
	TierExcellent Tier = "Excellent"
	TierGood      Tier = "Good"
	TierWatch     Tier = "Watch"
	TierCritical  Tier = "Critical"
)

// Weights holds the tunable scoring parameters (loaded from admin_config in
// production, defaulted here).
type Weights struct {
	CriticalWeight         int
	CriticalCap            int
	DowntimeBlocksPerPoint int
	DowntimeCap            int
	WarningWeight          int
	WarningCap             int
	ProposerMinExpected    int
	SignWeight             float64
	ProposerWeight         float64
	VpSeverityFactor       float64
}

func DefaultWeights() Weights {
	return Weights{
		CriticalWeight:         6,
		CriticalCap:            60,
		DowntimeBlocksPerPoint: 500,
		DowntimeCap:            20,
		WarningWeight:          2,
		WarningCap:             20,
		ProposerMinExpected:    5,
		SignWeight:             0.8,
		ProposerWeight:         0.2,
		VpSeverityFactor:       0.5,
	}
}

// Inputs carries one validator's raw metrics for one period. All fields are
// non-negative. Proposer/VP fields are 0 until those collectors are deployed,
// in which case their components degrade to neutral (proposer dropped,
// severity = 1).
type Inputs struct {
	SignedBlocks   int64 // participated_count over the period
	TotalBlocks    int64 // total blocks this validator was expected to sign
	ProposedBlocks int64 // proposed_count over the period
	ChainBlocks    int64 // total blocks on the chain in the period
	VotingPower    int64 // current voting power
	MaxVotingPower int64 // max VP across the chain (severity normalization)
	SumVotingPower int64 // sum of VP across the chain (vp share)
	DowntimeBlocks int64
	CriticalCount  int
	WarningCount   int
}

// Result is the computed score plus the surfaced sub-metrics.
type Result struct {
	Score               int
	Tier                Tier
	SignRate            float64 // 0..100
	ProposerReliability float64 // 0..100 (meaningful only when ProposerScored)
	ProposerScored      bool
}

func Compute(in Inputs, w Weights) Result {
	signRate := 0.0
	if in.TotalBlocks > 0 {
		signRate = float64(in.SignedBlocks) / float64(in.TotalBlocks) * 100
	}
	base := signRate

	// Proposer reliability, dropped when the validator's expected proposal
	// count is too small to be a reliable signal.
	propScored := false
	propRel := 0.0
	if in.SumVotingPower > 0 && in.ChainBlocks > 0 && in.VotingPower > 0 {
		vpShare := float64(in.VotingPower) / float64(in.SumVotingPower)
		expected := vpShare * float64(in.ChainBlocks)
		if expected >= float64(w.ProposerMinExpected) {
			ratio := float64(in.ProposedBlocks) / expected
			if ratio > 1 {
				ratio = 1
			}
			if ratio < 0 {
				ratio = 0
			}
			propRel = ratio * 100
			propScored = true
		}
	}

	presence := base
	if propScored && (w.SignWeight+w.ProposerWeight) > 0 {
		presence = (w.SignWeight*base + w.ProposerWeight*propRel) / (w.SignWeight + w.ProposerWeight)
	}

	critPenalty := in.CriticalCount * w.CriticalWeight
	if critPenalty > w.CriticalCap {
		critPenalty = w.CriticalCap
	}
	warnPenalty := in.WarningCount * w.WarningWeight
	if warnPenalty > w.WarningCap {
		warnPenalty = w.WarningCap
	}
	downPenalty := 0
	if w.DowntimeBlocksPerPoint > 0 {
		downPenalty = int(in.DowntimeBlocks / int64(w.DowntimeBlocksPerPoint))
	}
	if downPenalty > w.DowntimeCap {
		downPenalty = w.DowntimeCap
	}

	severity := 1.0
	if in.MaxVotingPower > 0 && in.VotingPower > 0 {
		severity = 1 + w.VpSeverityFactor*(float64(in.VotingPower)/float64(in.MaxVotingPower))
	}
	totalPenalty := float64(critPenalty+warnPenalty+downPenalty) * severity

	s := int(math.Round(presence - totalPenalty))
	if s < 0 {
		s = 0
	}
	if s > 100 {
		s = 100
	}
	return Result{
		Score:               s,
		Tier:                tierFor(s),
		SignRate:            signRate,
		ProposerReliability: propRel,
		ProposerScored:      propScored,
	}
}

func tierFor(s int) Tier {
	switch {
	case s >= 85:
		return TierExcellent
	case s >= 60:
		return TierGood
	case s >= 30:
		return TierWatch
	default:
		return TierCritical
	}
}

// admin_config keys for the tunable scoring weights.
const (
	KeyCriticalWeight         = "report_score_critical_weight"
	KeyCriticalCap            = "report_score_critical_cap"
	KeyDowntimeBlocksPerPoint = "report_score_downtime_blocks_per_point"
	KeyDowntimeCap            = "report_score_downtime_cap"
	KeyWarningWeight          = "report_score_warning_weight"
	KeyWarningCap             = "report_score_warning_cap"
	KeyProposerMinExpected    = "report_score_proposer_min_expected"
	KeySignWeight             = "report_score_sign_weight"
	KeyProposerWeight         = "report_score_proposer_weight"
	KeyVpSeverityFactor       = "report_score_vp_severity_factor"
)

// WeightsFromConfig builds Weights from an admin_config key/value map, using
// DefaultWeights() for any missing or non-numeric value.
func WeightsFromConfig(cfg map[string]string) Weights {
	w := DefaultWeights()
	w.CriticalWeight = numOr(cfg, KeyCriticalWeight, w.CriticalWeight, strconv.Atoi)
	w.CriticalCap = numOr(cfg, KeyCriticalCap, w.CriticalCap, strconv.Atoi)
	w.DowntimeBlocksPerPoint = numOr(cfg, KeyDowntimeBlocksPerPoint, w.DowntimeBlocksPerPoint, strconv.Atoi)
	w.DowntimeCap = numOr(cfg, KeyDowntimeCap, w.DowntimeCap, strconv.Atoi)
	w.WarningWeight = numOr(cfg, KeyWarningWeight, w.WarningWeight, strconv.Atoi)
	w.WarningCap = numOr(cfg, KeyWarningCap, w.WarningCap, strconv.Atoi)
	w.ProposerMinExpected = numOr(cfg, KeyProposerMinExpected, w.ProposerMinExpected, strconv.Atoi)
	w.SignWeight = numOr(cfg, KeySignWeight, w.SignWeight, parseFloat64)
	w.ProposerWeight = numOr(cfg, KeyProposerWeight, w.ProposerWeight, parseFloat64)
	w.VpSeverityFactor = numOr(cfg, KeyVpSeverityFactor, w.VpSeverityFactor, parseFloat64)
	return w
}

// numOr returns the parsed config value for key, or fallback when the key is
// absent or unparseable.
func numOr[T any](cfg map[string]string, key string, fallback T, parse func(string) (T, error)) T {
	v, ok := cfg[key]
	if !ok {
		return fallback
	}
	n, err := parse(v)
	if err != nil {
		return fallback
	}
	return n
}

func parseFloat64(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
