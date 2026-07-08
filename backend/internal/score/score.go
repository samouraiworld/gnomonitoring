// Package score turns raw per-validator alert metrics into a 0-100 health
// score and a human-readable tier. It is a pure package with no DB dependency
// so the scoring policy can be unit-tested in isolation.
package score

import "strconv"

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
// outages. Precondition: criticalCount >= 0 and downtimeBlocks >= 0; the score
// is nonetheless clamped to the 0-100 range as defense-in-depth against
// negative-input callers.
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
	if s > 100 {
		s = 100
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

// admin_config keys for the tunable scoring weights.
const (
	KeyCriticalWeight         = "report_score_critical_weight"
	KeyCriticalCap            = "report_score_critical_cap"
	KeyDowntimeBlocksPerPoint = "report_score_downtime_blocks_per_point"
	KeyDowntimeCap            = "report_score_downtime_cap"
)

// WeightsFromConfig builds Weights from an admin_config key/value map, using
// DefaultWeights() for any missing or non-integer value.
func WeightsFromConfig(cfg map[string]string) Weights {
	w := DefaultWeights()
	w.CriticalWeight = intOr(cfg, KeyCriticalWeight, w.CriticalWeight)
	w.CriticalCap = intOr(cfg, KeyCriticalCap, w.CriticalCap)
	w.DowntimeBlocksPerPoint = intOr(cfg, KeyDowntimeBlocksPerPoint, w.DowntimeBlocksPerPoint)
	w.DowntimeCap = intOr(cfg, KeyDowntimeCap, w.DowntimeCap)
	return w
}

func intOr(cfg map[string]string, key string, fallback int) int {
	v, ok := cfg[key]
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
