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
// gated on precommit membership. A nil latency map leaves every latency field
// nil.
func buildParticipation(precommitAddrs []string, proposerAddr string, hasTx bool, timeStp time.Time, latency map[string]precommitLatency) map[string]Participation {
	participating := make(map[string]Participation, len(precommitAddrs)+1)
	for _, addr := range precommitAddrs {
		p := Participation{
			Participated:   true,
			Timestamp:      timeStp,
			TxContribution: hasTx && addr == proposerAddr,
			Proposed:       addr == proposerAddr,
		}
		if l, ok := latency[addr]; ok {
			lag := l.LagMs
			p.PrecommitLagMs = &lag
			p.LateForQuorum = l.LateForQuorum
		}
		participating[addr] = p
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

// participation computes the per-validator Participation for this block,
// including signing latency derived with votingPower (the chain's latest
// valset snapshot).
func (b fetchedBlock) participation(votingPower map[string]int64) map[string]Participation {
	latency := computePrecommitLatency(b.Precommits, votingPower)
	return buildParticipation(b.PrecommitAddrs, b.ProposerAddr, b.HasTx, b.Time, latency)
}
