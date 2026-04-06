package gnovalidator

import (
	"fmt"
	"log"
	"sync"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/bft/types"
)

// FallbackRPCClient wraps an rpcclient.RPCClient and automatically retries
// on the next endpoint when a call fails.
type FallbackRPCClient struct {
	mu        sync.Mutex
	endpoints []string
	activeIdx int
	inner     *rpcclient.RPCClient
}

// NewFallbackRPCClient creates a FallbackRPCClient that tries each endpoint in
// order when a call fails. The first endpoint is used initially.
func NewFallbackRPCClient(endpoints []string) *FallbackRPCClient {
	if len(endpoints) == 0 {
		log.Printf("[rpc_fallback] no endpoints provided")
		return &FallbackRPCClient{endpoints: endpoints}
	}
	inner, err := rpcclient.NewHTTPClient(endpoints[0])
	if err != nil {
		log.Printf("[rpc_fallback] failed to create initial client for %s: %v", endpoints[0], err)
	}
	return &FallbackRPCClient{
		endpoints: endpoints,
		activeIdx: 0,
		inner:     inner,
	}
}

// rotate advances to the next endpoint and recreates the inner client.
// Must be called with f.mu held.
func (f *FallbackRPCClient) rotate() {
	if len(f.endpoints) == 0 {
		return
	}
	f.activeIdx = (f.activeIdx + 1) % len(f.endpoints)
	next := f.endpoints[f.activeIdx]
	log.Printf("[rpc_fallback] rotating to endpoint #%d: %s", f.activeIdx, next)
	inner, err := rpcclient.NewHTTPClient(next)
	if err != nil {
		log.Printf("[rpc_fallback] failed to create client for %s: %v", next, err)
		return
	}
	f.inner = inner
}

// withRetry calls fn with the current inner client, and if it fails, rotates to
// the next endpoint and retries once.
func (f *FallbackRPCClient) withRetry(fn func(c *rpcclient.RPCClient) error) error {
	f.mu.Lock()
	inner := f.inner
	f.mu.Unlock()

	if inner == nil {
		return fmt.Errorf("no RPC client available")
	}

	err := fn(inner)
	if err == nil {
		return nil
	}

	if len(f.endpoints) <= 1 {
		return err
	}

	f.mu.Lock()
	f.rotate()
	inner = f.inner
	f.mu.Unlock()

	if inner == nil {
		return err
	}

	return fn(inner)
}

// Status implements rpcclient.Client.
func (f *FallbackRPCClient) Status() (*ctypes.ResultStatus, error) {
	var result *ctypes.ResultStatus
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.Status()
		return e
	})
	return result, err
}

// ABCIInfo implements rpcclient.Client.
func (f *FallbackRPCClient) ABCIInfo() (*ctypes.ResultABCIInfo, error) {
	var result *ctypes.ResultABCIInfo
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.ABCIInfo()
		return e
	})
	return result, err
}

// ABCIQuery implements rpcclient.Client.
func (f *FallbackRPCClient) ABCIQuery(path string, data []byte) (*ctypes.ResultABCIQuery, error) {
	var result *ctypes.ResultABCIQuery
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.ABCIQuery(path, data)
		return e
	})
	return result, err
}

// ABCIQueryWithOptions implements rpcclient.Client.
func (f *FallbackRPCClient) ABCIQueryWithOptions(path string, data []byte, opts rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	var result *ctypes.ResultABCIQuery
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.ABCIQueryWithOptions(path, data, opts)
		return e
	})
	return result, err
}

// BroadcastTxCommit implements rpcclient.Client.
func (f *FallbackRPCClient) BroadcastTxCommit(tx types.Tx) (*ctypes.ResultBroadcastTxCommit, error) {
	var result *ctypes.ResultBroadcastTxCommit
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.BroadcastTxCommit(tx)
		return e
	})
	return result, err
}

// BroadcastTxAsync implements rpcclient.Client.
func (f *FallbackRPCClient) BroadcastTxAsync(tx types.Tx) (*ctypes.ResultBroadcastTx, error) {
	var result *ctypes.ResultBroadcastTx
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.BroadcastTxAsync(tx)
		return e
	})
	return result, err
}

// BroadcastTxSync implements rpcclient.Client.
func (f *FallbackRPCClient) BroadcastTxSync(tx types.Tx) (*ctypes.ResultBroadcastTx, error) {
	var result *ctypes.ResultBroadcastTx
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.BroadcastTxSync(tx)
		return e
	})
	return result, err
}

// UnconfirmedTxs implements rpcclient.Client.
func (f *FallbackRPCClient) UnconfirmedTxs(limit int) (*ctypes.ResultUnconfirmedTxs, error) {
	var result *ctypes.ResultUnconfirmedTxs
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.UnconfirmedTxs(limit)
		return e
	})
	return result, err
}

// NumUnconfirmedTxs implements rpcclient.Client.
func (f *FallbackRPCClient) NumUnconfirmedTxs() (*ctypes.ResultUnconfirmedTxs, error) {
	var result *ctypes.ResultUnconfirmedTxs
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.NumUnconfirmedTxs()
		return e
	})
	return result, err
}

// NetInfo implements rpcclient.Client.
func (f *FallbackRPCClient) NetInfo() (*ctypes.ResultNetInfo, error) {
	var result *ctypes.ResultNetInfo
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.NetInfo()
		return e
	})
	return result, err
}

// DumpConsensusState implements rpcclient.Client.
func (f *FallbackRPCClient) DumpConsensusState() (*ctypes.ResultDumpConsensusState, error) {
	var result *ctypes.ResultDumpConsensusState
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.DumpConsensusState()
		return e
	})
	return result, err
}

// ConsensusState implements rpcclient.Client.
func (f *FallbackRPCClient) ConsensusState() (*ctypes.ResultConsensusState, error) {
	var result *ctypes.ResultConsensusState
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.ConsensusState()
		return e
	})
	return result, err
}

// ConsensusParams implements rpcclient.Client.
func (f *FallbackRPCClient) ConsensusParams(height *int64) (*ctypes.ResultConsensusParams, error) {
	var result *ctypes.ResultConsensusParams
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.ConsensusParams(height)
		return e
	})
	return result, err
}

// Health implements rpcclient.Client.
func (f *FallbackRPCClient) Health() (*ctypes.ResultHealth, error) {
	var result *ctypes.ResultHealth
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.Health()
		return e
	})
	return result, err
}

// BlockchainInfo implements rpcclient.Client.
func (f *FallbackRPCClient) BlockchainInfo(minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	var result *ctypes.ResultBlockchainInfo
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.BlockchainInfo(minHeight, maxHeight)
		return e
	})
	return result, err
}

// Genesis implements rpcclient.Client.
func (f *FallbackRPCClient) Genesis() (*ctypes.ResultGenesis, error) {
	var result *ctypes.ResultGenesis
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.Genesis()
		return e
	})
	return result, err
}

// Block implements rpcclient.Client.
func (f *FallbackRPCClient) Block(height *int64) (*ctypes.ResultBlock, error) {
	var result *ctypes.ResultBlock
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.Block(height)
		return e
	})
	return result, err
}

// BlockResults implements rpcclient.Client.
func (f *FallbackRPCClient) BlockResults(height *int64) (*ctypes.ResultBlockResults, error) {
	var result *ctypes.ResultBlockResults
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.BlockResults(height)
		return e
	})
	return result, err
}

// Commit implements rpcclient.Client.
func (f *FallbackRPCClient) Commit(height *int64) (*ctypes.ResultCommit, error) {
	var result *ctypes.ResultCommit
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.Commit(height)
		return e
	})
	return result, err
}

// Tx implements rpcclient.Client.
func (f *FallbackRPCClient) Tx(hash []byte) (*ctypes.ResultTx, error) {
	var result *ctypes.ResultTx
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.Tx(hash)
		return e
	})
	return result, err
}

// Validators implements rpcclient.Client.
func (f *FallbackRPCClient) Validators(height *int64) (*ctypes.ResultValidators, error) {
	var result *ctypes.ResultValidators
	err := f.withRetry(func(c *rpcclient.RPCClient) error {
		var e error
		result, e = c.Validators(height)
		return e
	})
	return result, err
}
