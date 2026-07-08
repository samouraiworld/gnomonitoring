// Package score turns raw per-validator alert metrics into a 0-100 health
// score and a human-readable tier. It is a pure package with no DB dependency
// so the scoring policy can be unit-tested in isolation.
package score

// Tier is the human-readable band a score falls into.
type Tier string

const (
	TierExcellent Tier = "Excellent"
	TierGood      Tier = "Good"
	TierWatch     Tier = "Watch"
	TierCritical  Tier = "Critical"
)

// Weights holds the tunable scoring parameters (loaded from admin_config in
// production, defaulted here). All values are positive magnitudes.
type Weights struct {
	CriticalWeight         int // points lost per CRITICAL alert
	CriticalCap            int // max points lost from criticals
	DowntimeBlocksPerPoint int // blocks of downtime that cost 1 point
	DowntimeCap            int // max points lost from downtime
}

// DefaultWeights returns the starting calibration.
func DefaultWeights() Weights {
	return Weights{
		CriticalWeight:         6,
		CriticalCap:            60,
		DowntimeBlocksPerPoint: 500,
		DowntimeCap:            20,
	}
}

// Compute returns the score (0-100) and its tier for a validator over one
// period. criticalCount is the raw number of CRITICAL alert rows (resends
// included); downtimeBlocks is the summed (end-start) block span of those
// outages.
func Compute(criticalCount int, downtimeBlocks int64, w Weights) (int, Tier) {
	critPenalty := criticalCount * w.CriticalWeight
	if critPenalty > w.CriticalCap {
		critPenalty = w.CriticalCap
	}

	downPenalty := 0
	if w.DowntimeBlocksPerPoint > 0 {
		downPenalty = int(downtimeBlocks / int64(w.DowntimeBlocksPerPoint))
	}
	if downPenalty > w.DowntimeCap {
		downPenalty = w.DowntimeCap
	}

	s := 100 - critPenalty - downPenalty
	if s < 0 {
		s = 0
	}
	return s, tierFor(s)
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
