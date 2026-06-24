package gnovalidator

import (
	"fmt"
	"sort"
)

// bftMinValidatorsForAlert is the smallest validator-set size for which a BFT
// margin alert is meaningful. Below it (devnets, bootstrapping chains) every
// validator is effectively critical, so alerting would just be noise — the
// margin is still shown in /status and reports, just never alerted on.
const bftMinValidatorsForAlert = 4

// BFTMargin summarizes how close a chain is to losing its Byzantine
// fault-tolerance safety threshold, i.e. how many currently-active validators
// can still go offline before commits stop.
type BFTMargin struct {
	ActiveCount      int   // validators that participated (rate > 0) and have known power
	TotalCount       int   // validators in the set
	TolerableOffline int   // how many more active validators can go offline before quorum is lost
	TotalPower       int64 // sum of voting power across the set
	ActivePower      int64 // sum of voting power of active validators
	RequiredPower    int64 // minimum online power needed to keep committing
}

// ComputeBFTMargin derives the BFT safety margin from the validator set and a
// participation-rate map. A validator counts as "active" when it has a rate
// strictly above zero AND its voting power is known from the set.
//
// Tendermint commits a block only when validators representing strictly more
// than 2/3 of total voting power precommit, so the minimum online power is
// totalPower*2/3 + 1 (integer arithmetic, matching Tendermint). TolerableOffline
// is computed worst-case: the highest-power active validators are removed first.
func ComputeBFTMargin(set []ValidatorInfo, rates map[string]ValidatorRate) BFTMargin {
	var m BFTMargin
	m.TotalCount = len(set)

	// Single pass: accumulate total power, and for each active validator (rate
	// > 0) bump the active count and — only when it actually carries positive
	// voting power — its active power and the per-validator power list used for
	// the worst-case removal below. Guarding on VotingPower > 0 keeps a
	// zero/negative-power validator from being "removed for free" and inflating
	// the tolerance.
	activePowers := make([]int64, 0, len(set))
	for _, v := range set {
		m.TotalPower += v.VotingPower
		if r, ok := rates[v.Address]; ok && r.Rate > 0 {
			m.ActiveCount++
			if v.VotingPower > 0 {
				m.ActivePower += v.VotingPower
				activePowers = append(activePowers, v.VotingPower)
			}
		}
	}

	if m.TotalPower <= 0 {
		return m
	}
	m.RequiredPower = m.TotalPower*2/3 + 1

	// Worst case: drop the largest-power active validators first and count how
	// many can be removed while the remaining power stays at or above the
	// required quorum.
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

// FormatBFTMarginLine renders the BFT margin as a single report line, or "" when
// no validator-set power is known (so the caller can omit it). The emoji tracks
// the tolerance: 🔴 when a single further failure would halt the chain, ⚠️ when
// exactly one more can be lost, 🟢 otherwise.
func FormatBFTMarginLine(m BFTMargin) string {
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

// BFTAlertLevel classifies a BFT margin into an alert level for the chain-level
// BFT watcher: "CRITICAL" when the next failure would halt the chain, "WARNING"
// when only one more validator can be lost, or "" when healthy / not alertable.
// Sets smaller than bftMinValidatorsForAlert, or with unknown power, never
// alert.
func BFTAlertLevel(m BFTMargin) string {
	if m.TotalPower <= 0 || m.TotalCount < bftMinValidatorsForAlert {
		return ""
	}
	switch m.TolerableOffline {
	case 0:
		return "CRITICAL"
	case 1:
		return "WARNING"
	default:
		return ""
	}
}
