// Package chainquery contains the read operations used by monitoring. Keeping
// signing/key-management code out of this client avoids importing Gno's legacy
// OpenPGP armor dependency into the server.
package chainquery

import (
	"fmt"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/errors"
)

var (
	ErrMissingRPCClient   = errors.New("missing RPCClient")
	ErrInvalidBlockHeight = errors.New("invalid block height provided")
)

// Client uses the existing RPC pool without a signer or key store.
type Client struct {
	RPCClient rpcclient.Client
}

type QueryCfg struct {
	Path string
	Data []byte
	rpcclient.ABCIQueryOptions
}

// Query preserves the query options, response and error semantics used by the
// pinned Gno client's read operation, including ABCI failures with a response.
func (c *Client) Query(cfg QueryCfg) (*ctypes.ResultABCIQuery, error) {
	if c.RPCClient == nil {
		return nil, ErrMissingRPCClient
	}
	res, err := c.RPCClient.ABCIQueryWithOptions(cfg.Path, cfg.Data, cfg.ABCIQueryOptions)
	if err != nil {
		return nil, errors.Wrap(err, "query error")
	}
	if res.Response.Error != nil {
		return res, errors.Wrapf(res.Response.Error, "deliver transaction failed: log:%s", res.Response.Log)
	}
	return res, nil
}

func (c *Client) Block(height int64) (*ctypes.ResultBlock, error) {
	if c.RPCClient == nil {
		return nil, ErrMissingRPCClient
	}
	if height <= 0 {
		return nil, ErrInvalidBlockHeight
	}
	block, err := c.RPCClient.Block(&height)
	if err != nil {
		return nil, fmt.Errorf("block query failed: %w", err)
	}
	return block, nil
}

func (c *Client) LatestBlockHeight() (int64, error) {
	if c.RPCClient == nil {
		return 0, ErrMissingRPCClient
	}
	status, err := c.RPCClient.Status()
	if err != nil {
		return 0, fmt.Errorf("block number query failed: %w", err)
	}
	return status.SyncInfo.LatestBlockHeight, nil
}
