package gnovalidator

import (
	"sort"
	"time"
)

// signedPrecommit is one precommit for the committed BlockID, read from a
// block's LastCommit. Precommits for nil or for another BlockID are excluded
// before this point.
type signedPrecommit struct {
	Addr      string
	Timestamp time.Time
}

// precommitLatency is one validator's signing latency within a single commit.
//
// Timestamps are the signer's local clock (tm2 voteTime), so an NTP drift
// biases the absolute value: use these for trends and before/after
// comparisons, not as an exact network delay.
type precommitLatency struct {
	// LagMs is the precommit timestamp minus the earliest precommit timestamp
	// of the same commit, truncated to whole milliseconds.
	LagMs int64
	// LateForQuorum is true when the precommit is strictly after the precommit
	// that brought cumulative voting power above 2/3. nil when the quorum
	// cannot be derived from the known voting powers.
	LateForQuorum *bool
}

// computePrecommitLatency derives per-address latency for one commit.
// votingPower is the latest known valset snapshot (addr -> VP); its sum is the
// quorum denominator, so validators that did not sign still count toward it.
func computePrecommitLatency(precommits []signedPrecommit, votingPower map[string]int64) map[string]precommitLatency {
	if len(precommits) == 0 {
		return nil
	}

	sorted := make([]signedPrecommit, len(precommits))
	copy(sorted, precommits)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	earliest := sorted[0].Timestamp
	quorumAt, quorumKnown := quorumTimestamp(sorted, votingPower)

	result := make(map[string]precommitLatency, len(sorted))
	for _, pc := range sorted {
		l := precommitLatency{LagMs: pc.Timestamp.Sub(earliest).Milliseconds()}
		if quorumKnown {
			late := pc.Timestamp.After(quorumAt)
			l.LateForQuorum = &late
		}
		result[pc.Addr] = l
	}
	return result
}

// quorumTimestamp returns the timestamp of the precommit (in sorted, ascending
// timestamp order) at which cumulative voting power first exceeds 2/3 of the
// total. ok is false when the total is 0, when any signer has no known
// positive VP (the snapshot predates a valset change, so the total itself is
// unreliable), or when the quorum is never reached.
func quorumTimestamp(sorted []signedPrecommit, votingPower map[string]int64) (time.Time, bool) {
	var total int64
	for _, vp := range votingPower {
		total += vp
	}
	if total <= 0 {
		return time.Time{}, false
	}
	for _, pc := range sorted {
		if votingPower[pc.Addr] <= 0 {
			return time.Time{}, false
		}
	}

	var cumulative int64
	for _, pc := range sorted {
		cumulative += votingPower[pc.Addr]
		if cumulative*3 > total*2 {
			return pc.Timestamp, true
		}
	}
	return time.Time{}, false
}
