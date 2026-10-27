package chainquery

import (
	"errors"
	"fmt"
	"testing"

	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/stretchr/testify/require"
)

type readRPC struct {
	rpcclient.Client
	query  func(string, []byte, rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error)
	block  func(*int64) (*ctypes.ResultBlock, error)
	status func() (*ctypes.ResultStatus, error)
}

func (r *readRPC) ABCIQueryWithOptions(path string, data []byte, opts rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	return r.query(path, data, opts)
}

func (r *readRPC) Block(height *int64) (*ctypes.ResultBlock, error) { return r.block(height) }
func (r *readRPC) Status() (*ctypes.ResultStatus, error)            { return r.status() }

func TestQueryPreservesOptionsAndABCIResult(t *testing.T) {
	response := &ctypes.ResultABCIQuery{}
	response.Response.Data = []byte("rendered proposal")
	opts := rpcclient.ABCIQueryOptions{Height: 42, Prove: true}
	client := Client{RPCClient: &readRPC{query: func(path string, data []byte, got rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
		require.Equal(t, "vm/qrender", path)
		require.Equal(t, []byte("gno.land/r/test:proposal"), data)
		require.Equal(t, opts, got)
		return response, nil
	}}}
	got, err := client.Query(QueryCfg{Path: "vm/qrender", Data: []byte("gno.land/r/test:proposal"), ABCIQueryOptions: opts})
	require.NoError(t, err)
	require.Same(t, response, got)

	// Chain-level failures retain their ABCI response and log for the caller.
	response.Response.Error = abci.StringError("realm rejected query")
	response.Response.Log = "proposal not found"
	got, err = client.Query(QueryCfg{Path: "vm/qrender", Data: []byte("gno.land/r/test:proposal"), ABCIQueryOptions: opts})
	require.Same(t, response, got)
	require.Contains(t, fmt.Sprintf("%+v", err), "deliver transaction failed: log:proposal not found")
	require.ErrorContains(t, err, "realm rejected query")
}

func TestQueryTransportFailureKeepsQueryErrorContext(t *testing.T) {
	client := Client{RPCClient: &readRPC{query: func(string, []byte, rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
		return nil, errors.New("RPC unavailable")
	}}}
	got, err := client.Query(QueryCfg{})
	require.Nil(t, got)
	require.Contains(t, fmt.Sprintf("%+v", err), "query error")
	require.ErrorContains(t, err, "RPC unavailable")
}

func TestReadValidationBeforeRPC(t *testing.T) {
	missing := Client{}
	_, err := missing.Query(QueryCfg{})
	require.Equal(t, ErrMissingRPCClient, err)
	_, err = missing.Block(0)
	require.Equal(t, ErrMissingRPCClient, err)
	_, err = missing.LatestBlockHeight()
	require.Equal(t, ErrMissingRPCClient, err)
	// Any RPC call on this fake would panic, so invalid heights must stop first.
	client := Client{RPCClient: &readRPC{}}
	for _, height := range []int64{-1, 0} {
		_, err = client.Block(height)
		require.Equal(t, ErrInvalidBlockHeight, err)
	}
}

func TestBlockAndHeightPreserveResultsAndWrappedErrors(t *testing.T) {
	response := &ctypes.ResultBlock{}
	status := &ctypes.ResultStatus{}
	status.SyncInfo.LatestBlockHeight = 99
	transport := errors.New("connection refused")
	var down bool
	client := Client{RPCClient: &readRPC{
		block: func(height *int64) (*ctypes.ResultBlock, error) {
			require.Equal(t, int64(42), *height)
			if down {
				return nil, transport
			}
			return response, nil
		},
		status: func() (*ctypes.ResultStatus, error) {
			if down {
				return nil, transport
			}
			return status, nil
		},
	}}
	got, err := client.Block(42)
	require.NoError(t, err)
	require.Same(t, response, got)
	height, err := client.LatestBlockHeight()
	require.NoError(t, err)
	require.Equal(t, int64(99), height)
	down = true
	_, err = client.Block(42)
	require.ErrorIs(t, err, transport)
	require.EqualError(t, err, "block query failed: connection refused")
	_, err = client.LatestBlockHeight()
	require.ErrorIs(t, err, transport)
	require.EqualError(t, err, "block number query failed: connection refused")
}
