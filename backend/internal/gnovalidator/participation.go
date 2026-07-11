package gnovalidator

import "time"

// buildParticipation derives per-validator Participation for one block from
// the previous block's precommit signer addresses (LastCommit.Precommits
// reflects h-1's signers, not h's proposer) and h's own proposer address.
//
// The proposer is always credited with Proposed=true, even when it is absent
// from precommitAddrs: a validator can be selected to propose a block having
// missed precommitting the prior one (e.g. it just came back online), and
// CLAUDE.md documents proposer marking as unconditional — it must not be
// gated on precommit membership.
func buildParticipation(precommitAddrs []string, proposerAddr string, hasTx bool, timeStp time.Time) map[string]Participation {
	participating := make(map[string]Participation, len(precommitAddrs)+1)
	for _, addr := range precommitAddrs {
		participating[addr] = Participation{
			Participated:   true,
			Timestamp:      timeStp,
			TxContribution: hasTx && addr == proposerAddr,
			Proposed:       addr == proposerAddr,
		}
	}

	if p, ok := participating[proposerAddr]; ok {
		p.Proposed = true
		participating[proposerAddr] = p
	} else {
		participating[proposerAddr] = Participation{
			Timestamp:      timeStp,
			Proposed:       true,
			TxContribution: hasTx,
		}
	}
	return participating
}
