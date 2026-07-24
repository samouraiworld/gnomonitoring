package gnovalidator

import (
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
)

const (
	fetchBlockMaxAttempts = 3
	fetchBlockRetryDelay  = 150 * time.Millisecond
)

// fetchBlockParticipation fetches block h from client and extracts the data
// needed to compute participation, retrying on failure (transient RPC
// errors, or a block/LastCommit that isn't populated yet) up to
// fetchBlockMaxAttempts times with a short backoff. ok is false only once
// every attempt has failed; callers are responsible for logging that and
// treating h as still unrecorded.
func fetchBlockParticipation(client gnoclient.Client, h int64) (precommitAddrs []string, proposerAddr string, hasTx bool, timeStp time.Time, ok bool) {
	for attempt := 1; attempt <= fetchBlockMaxAttempts; attempt++ {
		block, err := client.Block(h)
		if err == nil && block != nil && block.Block != nil && block.Block.LastCommit != nil {
			proposerAddr = block.Block.Header.ProposerAddress.String()
			hasTx = len(block.Block.Data.Txs) > 0
			timeStp = block.Block.Header.Time
			precommitAddrs = make([]string, 0, len(block.Block.LastCommit.Precommits))
			for _, precommit := range block.Block.LastCommit.Precommits {
				if precommit != nil {
					precommitAddrs = append(precommitAddrs, precommit.ValidatorAddress.String())
				}
			}
			return precommitAddrs, proposerAddr, hasTx, timeStp, true
		}
		if attempt < fetchBlockMaxAttempts {
			time.Sleep(fetchBlockRetryDelay * time.Duration(attempt))
		}
	}
	return nil, "", false, time.Time{}, false
}
