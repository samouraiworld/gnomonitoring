package gnovalidator

import (
	"fmt"
	"sort"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

// bftOnlineSignRate is the minimum last_24h sign rate (0..100) for a
// validator to count as online in the BFT margin. A fixed threshold keeps a
// validator that missed a handful of blocks from being counted as offline.
const bftOnlineSignRate = 50.0

// BFTMargin summarizes how far the chain's online voting power sits above
// the > 2/3 precommit quorum Tendermint needs to commit a block. Only voting
// power counts, never the validator count: with unequal power, one validator
// holding more than 1/3 halts the chain alone by going offline.
//
// Available is false when no voting-power snapshot exists yet (every report
// entry has VotingPower 0); the other fields are then meaningless and the
// report marks the margin unavailable instead of printing a wrong number.
type BFTMargin struct {
	Available   bool
	OnlineCount int
	TotalCount  int
	OnlineVP    int64
	TotalVP     int64
	// TolerableLosses is how many more online validators can go offline,
	// removing the largest voting power first (worst case), before the
	// online power drops to 2/3 or below.
	TolerableLosses int
}

// AboveQuorum reports whether the online voting power is strictly above 2/3
// of the total.
func (m BFTMargin) AboveQuorum() bool {
	return 3*m.OnlineVP > 2*m.TotalVP
}

// MarginPercent returns the online voting power above the 2/3 quorum, as a
// percentage of the total voting power. Zero or negative means the quorum
// is not met. Only meaningful when Available.
func (m BFTMargin) MarginPercent() float64 {
	if m.TotalVP <= 0 {
		return 0
	}
	return float64(3*m.OnlineVP-2*m.TotalVP) / float64(3*m.TotalVP) * 100
}

// computeBFTMargin derives the BFT margin from the last_24h report entries:
// each entry's VotingPower comes from the latest addr_monikers snapshot and
// its SignRate from the same window as the report's Problems. Entries
// without voting power (not in the snapshot) are ignored.
func computeBFTMargin(entries []database.ValidatorReportEntry) BFTMargin {
	var m BFTMargin
	var onlineVPs []int64
	for _, e := range entries {
		if e.VotingPower <= 0 {
			continue
		}
		m.TotalCount++
		m.TotalVP += e.VotingPower
		if e.SignRate >= bftOnlineSignRate {
			m.OnlineCount++
			m.OnlineVP += e.VotingPower
			onlineVPs = append(onlineVPs, e.VotingPower)
		}
	}
	if m.TotalVP == 0 {
		return BFTMargin{}
	}
	m.Available = true

	sort.Slice(onlineVPs, func(i, j int) bool { return onlineVPs[i] > onlineVPs[j] })
	remaining := m.OnlineVP
	for _, vp := range onlineVPs {
		if 3*(remaining-vp) <= 2*m.TotalVP {
			break
		}
		remaining -= vp
		m.TolerableLosses++
	}
	return m
}

// bftMarginEmoji grades the margin by how many validators can still be
// lost: two or more is healthy, one is a warning, none is critical.
func bftMarginEmoji(m BFTMargin) string {
	switch {
	case !m.AboveQuorum() || m.TolerableLosses == 0:
		return "🔴"
	case m.TolerableLosses == 1:
		return "⚠️"
	default:
		return "✅"
	}
}

// bftMarginText returns the plain (unescaped) BFT margin line, or "" when m
// is nil (a DailyReportData built without going through
// BuildDailyReportData).
func bftMarginText(m *BFTMargin) string {
	if m == nil {
		return ""
	}
	if !m.Available {
		return "🛡️ BFT margin: unavailable (no voting-power snapshot yet)"
	}
	head := fmt.Sprintf("🛡️ BFT margin (last 24h): %d/%d validators online (%.1f%% VP)",
		m.OnlineCount, m.TotalCount, float64(m.OnlineVP)/float64(m.TotalVP)*100)
	if !m.AboveQuorum() {
		return fmt.Sprintf("%s — at or below 2/3 quorum %s", head, bftMarginEmoji(*m))
	}
	noun := "validators"
	if m.TolerableLosses == 1 {
		noun = "validator"
	}
	return fmt.Sprintf("%s — %.1f%% VP above 2/3 quorum, can lose %d more %s (largest first) %s",
		head, m.MarginPercent(), m.TolerableLosses, noun, bftMarginEmoji(*m))
}

// proposedText returns the "Proposed: N" fragment appended to each problem
// validator's line: blocks it proposed over the same last_24h window.
func proposedText(e database.ValidatorReportEntry) string {
	return fmt.Sprintf("Proposed: %d", e.ProposedBlocks)
}
