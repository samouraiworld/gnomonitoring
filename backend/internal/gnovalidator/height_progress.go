package gnovalidator

import "time"

// heightObservation describes how a freshly polled chain height compares to
// the highest height already observed for that chain.
type heightObservation int

const (
	// heightAdvanced: the chain moved forward (or this is the first poll).
	heightAdvanced heightObservation = iota
	// heightStalled: the same height as last time — the input to stagnation
	// detection.
	heightStalled
	// heightRegressed: a height below the highest already seen. The chain
	// cannot go backwards, so this means the endpoint currently serving us is
	// behind the one that served the previous poll.
	heightRegressed
)

// classifyHeightObservation compares a polled height against the highest
// height seen so far. lastSeen == 0 means nothing has been observed yet.
//
// Treating a regression as progress is what used to break stagnation
// detection: two endpoints a few blocks apart make the height oscillate, and
// every oscillation reset lastProgressTime and lastStagnationAlertTime, so
// the "Blockchain stuck" alert could never reach its threshold even on a
// genuinely halted chain.
func classifyHeightObservation(latest, lastSeen int64) heightObservation {
	switch {
	case lastSeen == 0:
		return heightAdvanced
	case latest > lastSeen:
		return heightAdvanced
	case latest == lastSeen:
		return heightStalled
	default:
		return heightRegressed
	}
}

// evaluateRegression decides whether an ongoing height regression should
// still be ignored (treated as a transient artifact of endpoint fail-over)
// or accepted as the new reality (the chain genuinely rewound — e.g. an
// operator restored a node from an older snapshot — and that lower height is
// now what we must track).
//
// regressedSince is the zero Time when no regression is currently being
// tracked (either this is the first regressed observation, or the tracker
// was reset because the previous observation was not a regression). now is
// the time of the current observation. bound is the maximum duration a
// regression may be ignored before it is accepted; the caller is expected to
// pass GetThresholds().StagnationFirstAlert() — the same threshold that
// already governs "we have not made forward progress in too long" for the
// stalled case, since a regression that persists that long is
// indistinguishable in effect from a stall.
//
// Returns accept (true once bound has elapsed) and nextRegressedSince, the
// value the caller must store for the next call: now on the first regressed
// observation of an episode, unchanged while still within bound, and reset
// to the zero Time once accepted (the regression is no longer "in
// progress" — it has become the new baseline).
func evaluateRegression(regressedSince, now time.Time, bound time.Duration) (accept bool, nextRegressedSince time.Time) {
	if regressedSince.IsZero() {
		regressedSince = now
	}
	if now.Sub(regressedSince) > bound {
		return true, time.Time{}
	}
	return false, regressedSince
}
