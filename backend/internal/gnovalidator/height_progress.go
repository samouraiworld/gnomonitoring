package gnovalidator

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
