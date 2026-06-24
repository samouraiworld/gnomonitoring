package gnovalidator

import (
	"fmt"
	"sort"
)

// bftMargin summarizes how close a chain is to losing its Byzantine
// fault-tolerance safety threshold, i.e. how many currently-active validators
// can still go offline before commits stop.
type bftMargin struct {
	ActiveCount      int   // validators that participated (rate > 0) and have known power
	TotalCount       int   // validators in the set
	TolerableOffline int   // how many more active validators can go offline before quorum is lost
	TotalPower       int64 // sum of voting power across the set
	ActivePower      int64 // sum of voting power of active validators
	RequiredPower    int64 // minimum online power needed to keep committing
}

// computeBFTMargin derives the BFT safety margin from the validator set and
// yesterday's participation rates. A validator counts as "active" when it has a
// rate strictly above zero AND its voting power is known from the set.
//
// Tendermint commits a block only when validators representing strictly more
// than 2/3 of total voting power precommit, so the minimum online power is
// totalPower*2/3 + 1 (integer arithmetic, matching Tendermint). TolerableOffline
// is computed worst-case: the highest-power active validators are removed first.
func computeBFTMargin(set []ValidatorInfo, rates map[string]ValidatorRate) bftMargin {
	var m bftMargin
	m.TotalCount = len(set)

	for _, v := range set {
		m.TotalPower += v.VotingPower
		if r, ok := rates[v.Address]; ok && r.Rate > 0 {
			m.ActiveCount++
			m.ActivePower += v.VotingPower
		}
	}

	if m.TotalPower <= 0 {
		return m
	}
	m.RequiredPower = m.TotalPower*2/3 + 1

	// Worst case: drop the largest-power active validators first and count how
	// many can be removed while the remaining power stays at or above the
	// required quorum.
	activePowers := make([]int64, 0, m.ActiveCount)
	for _, v := range set {
		if r, ok := rates[v.Address]; ok && r.Rate > 0 {
			activePowers = append(activePowers, v.VotingPower)
		}
	}
	sort.Slice(activePowers, func(i, j int) bool { return activePowers[i] > activePowers[j] })

	remaining := m.ActivePower
	for _, p := range activePowers {
		if remaining-p >= m.RequiredPower {
			remaining -= p
			m.TolerableOffline++
		} else {
			break
		}
	}
	return m
}

// formatBFTMarginLine renders the BFT margin as a single report line, or "" when
// no validator-set power is known (so the caller can omit it). The emoji tracks
// the tolerance: 🔴 when a single further failure would halt the chain, ⚠️ when
// exactly one more can be lost, 🟢 otherwise.
func formatBFTMarginLine(m bftMargin) string {
	if m.TotalPower <= 0 {
		return ""
	}
	switch m.TolerableOffline {
	case 0:
		return fmt.Sprintf("BFT: %d/%d validators active — 🔴 no safety margin (next offline halts the chain)",
			m.ActiveCount, m.TotalCount)
	case 1:
		return fmt.Sprintf("BFT: %d/%d validators active — can tolerate 1 more offline ⚠️",
			m.ActiveCount, m.TotalCount)
	default:
		return fmt.Sprintf("BFT: %d/%d validators active — can tolerate %d more offline 🟢",
			m.ActiveCount, m.TotalCount, m.TolerableOffline)
	}
}
