package rpcpool

import (
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/bft/types"
)

// ABCIQuery implements rpcclient.Client.
func (c *Client) ABCIQuery(path string, data []byte) (*ctypes.ResultABCIQuery, error) {
	var result *ctypes.ResultABCIQuery
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.ABCIQuery(path, data)
		return e
	})
	return result, err
}

// ABCIQueryWithOptions implements rpcclient.Client.
func (c *Client) ABCIQueryWithOptions(path string, data []byte, opts rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	var result *ctypes.ResultABCIQuery
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.ABCIQueryWithOptions(path, data, opts)
		return e
	})
	return result, err
}

// BroadcastTxCommit implements rpcclient.Client.
func (c *Client) BroadcastTxCommit(tx types.Tx) (*ctypes.ResultBroadcastTxCommit, error) {
	var result *ctypes.ResultBroadcastTxCommit
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.BroadcastTxCommit(tx)
		return e
	})
	return result, err
}

// BroadcastTxAsync implements rpcclient.Client.
func (c *Client) BroadcastTxAsync(tx types.Tx) (*ctypes.ResultBroadcastTx, error) {
	var result *ctypes.ResultBroadcastTx
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.BroadcastTxAsync(tx)
		return e
	})
	return result, err
}

// BroadcastTxSync implements rpcclient.Client.
func (c *Client) BroadcastTxSync(tx types.Tx) (*ctypes.ResultBroadcastTx, error) {
	var result *ctypes.ResultBroadcastTx
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.BroadcastTxSync(tx)
		return e
	})
	return result, err
}

// UnconfirmedTxs implements rpcclient.Client.
func (c *Client) UnconfirmedTxs(limit int) (*ctypes.ResultUnconfirmedTxs, error) {
	var result *ctypes.ResultUnconfirmedTxs
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.UnconfirmedTxs(limit)
		return e
	})
	return result, err
}

// NumUnconfirmedTxs implements rpcclient.Client.
func (c *Client) NumUnconfirmedTxs() (*ctypes.ResultUnconfirmedTxs, error) {
	var result *ctypes.ResultUnconfirmedTxs
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.NumUnconfirmedTxs()
		return e
	})
	return result, err
}

// NetInfo implements rpcclient.Client.
func (c *Client) NetInfo() (*ctypes.ResultNetInfo, error) {
	var result *ctypes.ResultNetInfo
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.NetInfo()
		return e
	})
	return result, err
}

// DumpConsensusState implements rpcclient.Client.
func (c *Client) DumpConsensusState() (*ctypes.ResultDumpConsensusState, error) {
	var result *ctypes.ResultDumpConsensusState
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.DumpConsensusState()
		return e
	})
	return result, err
}

// ConsensusState implements rpcclient.Client.
func (c *Client) ConsensusState() (*ctypes.ResultConsensusState, error) {
	var result *ctypes.ResultConsensusState
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.ConsensusState()
		return e
	})
	return result, err
}

// ConsensusParams implements rpcclient.Client.
func (c *Client) ConsensusParams(height *int64) (*ctypes.ResultConsensusParams, error) {
	var result *ctypes.ResultConsensusParams
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.ConsensusParams(height)
		return e
	})
	return result, err
}

// Health implements rpcclient.Client.
func (c *Client) Health() (*ctypes.ResultHealth, error) {
	var result *ctypes.ResultHealth
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.Health()
		return e
	})
	return result, err
}

// BlockchainInfo implements rpcclient.Client.
func (c *Client) BlockchainInfo(minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	var result *ctypes.ResultBlockchainInfo
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.BlockchainInfo(minHeight, maxHeight)
		return e
	})
	return result, err
}

// Genesis implements rpcclient.Client.
func (c *Client) Genesis() (*ctypes.ResultGenesis, error) {
	var result *ctypes.ResultGenesis
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.Genesis()
		return e
	})
	return result, err
}

// Block implements rpcclient.Client.
func (c *Client) Block(height *int64) (*ctypes.ResultBlock, error) {
	var result *ctypes.ResultBlock
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.Block(height)
		return e
	})
	return result, err
}

// BlockResults implements rpcclient.Client.
func (c *Client) BlockResults(height *int64) (*ctypes.ResultBlockResults, error) {
	var result *ctypes.ResultBlockResults
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.BlockResults(height)
		return e
	})
	return result, err
}

// Commit implements rpcclient.Client.
func (c *Client) Commit(height *int64) (*ctypes.ResultCommit, error) {
	var result *ctypes.ResultCommit
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.Commit(height)
		return e
	})
	return result, err
}

// Tx implements rpcclient.Client.
func (c *Client) Tx(hash []byte) (*ctypes.ResultTx, error) {
	var result *ctypes.ResultTx
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.Tx(hash)
		return e
	})
	return result, err
}

// Validators implements rpcclient.Client.
func (c *Client) Validators(height *int64) (*ctypes.ResultValidators, error) {
	var result *ctypes.ResultValidators
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.Validators(height)
		return e
	})
	return result, err
}
