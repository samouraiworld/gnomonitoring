package gnovalidator

import (
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
)

const (
	fetchBlockMaxAttempts = 3
	fetchBlockRetryDelay  = 150 * time.Millisecond
)

// fetchedBlock is what the participation pipeline needs from one block.
type fetchedBlock struct {
	// PrecommitAddrs lists every non-nil precommit signer of LastCommit
	// (h-1's commit); this is what "participated" is derived from.
	PrecommitAddrs []string
	// Precommits holds only the precommits for LastCommit.BlockID, with their
	// timestamps, for signing-latency computation.
	Precommits   []signedPrecommit
	ProposerAddr string
	HasTx        bool
	Time         time.Time
}

// fetchBlockParticipation fetches block h from client and extracts the data
// needed to compute participation and signing latency, retrying on failure
// (transient RPC errors, or a block/LastCommit that isn't populated yet) up
// to fetchBlockMaxAttempts times with a short backoff. ok is false only once
// every attempt has failed; callers are responsible for logging that and
// treating h as still unrecorded.
func fetchBlockParticipation(client gnoclient.Client, h int64) (fetchedBlock, bool) {
	for attempt := 1; attempt <= fetchBlockMaxAttempts; attempt++ {
		block, err := client.Block(h)
		if err == nil && block != nil && block.Block != nil && block.Block.LastCommit != nil {
			commit := block.Block.LastCommit
			fb := fetchedBlock{
				PrecommitAddrs: make([]string, 0, len(commit.Precommits)),
				Precommits:     make([]signedPrecommit, 0, len(commit.Precommits)),
				ProposerAddr:   block.Block.Header.ProposerAddress.String(),
				HasTx:          len(block.Block.Data.Txs) > 0,
				Time:           block.Block.Header.Time,
			}
			for _, precommit := range commit.Precommits {
				if precommit == nil {
					continue
				}
				addr := precommit.ValidatorAddress.String()
				fb.PrecommitAddrs = append(fb.PrecommitAddrs, addr)
				if precommit.BlockID.Equals(commit.BlockID) {
					fb.Precommits = append(fb.Precommits, signedPrecommit{Addr: addr, Timestamp: precommit.Timestamp})
				}
			}
			return fb, true
		}
		if attempt < fetchBlockMaxAttempts {
			time.Sleep(fetchBlockRetryDelay * time.Duration(attempt))
		}
	}
	return fetchedBlock{}, false
}
