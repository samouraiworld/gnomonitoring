package gnovalidator

import (
	"errors"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/bft/types"
)

// fakeRPCClient implements rpcclient.Client, stubbing only Block (the sole
// method exercised by fetchBlockParticipation/BackfillHeights). Every other
// method panics if called, since nothing under test should call them.
type fakeRPCClient struct {
	// blockFunc is called for every Block(height) invocation; the test
	// controls success/failure/attempt-counting through it.
	blockFunc func(height int64) (*ctypes.ResultBlock, error)
}

func (f *fakeRPCClient) Block(height *int64) (*ctypes.ResultBlock, error) {
	return f.blockFunc(*height)
}

func (f *fakeRPCClient) ABCIInfo() (*ctypes.ResultABCIInfo, error) { panic("not implemented") }
func (f *fakeRPCClient) ABCIQuery(path string, data []byte) (*ctypes.ResultABCIQuery, error) {
	panic("not implemented")
}

func (f *fakeRPCClient) ABCIQueryWithOptions(path string, data []byte, opts rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) BroadcastTxCommit(tx types.Tx) (*ctypes.ResultBroadcastTxCommit, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) BroadcastTxAsync(tx types.Tx) (*ctypes.ResultBroadcastTx, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) BroadcastTxSync(tx types.Tx) (*ctypes.ResultBroadcastTx, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) Genesis() (*ctypes.ResultGenesis, error) { panic("not implemented") }
func (f *fakeRPCClient) BlockchainInfo(minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) Status() (*ctypes.ResultStatus, error) { panic("not implemented") }
func (f *fakeRPCClient) NetInfo() (*ctypes.ResultNetInfo, error) { panic("not implemented") }
func (f *fakeRPCClient) DumpConsensusState() (*ctypes.ResultDumpConsensusState, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) ConsensusState() (*ctypes.ResultConsensusState, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) ConsensusParams(height *int64) (*ctypes.ResultConsensusParams, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) Health() (*ctypes.ResultHealth, error) { panic("not implemented") }
func (f *fakeRPCClient) UnconfirmedTxs(limit int) (*ctypes.ResultUnconfirmedTxs, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) NumUnconfirmedTxs() (*ctypes.ResultUnconfirmedTxs, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) Tx(hash []byte) (*ctypes.ResultTx, error) { panic("not implemented") }
func (f *fakeRPCClient) Validators(height *int64) (*ctypes.ResultValidators, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) BlockResults(height *int64) (*ctypes.ResultBlockResults, error) {
	panic("not implemented")
}
func (f *fakeRPCClient) Commit(height *int64) (*ctypes.ResultCommit, error) {
	panic("not implemented")
}

var errFakeRPC = errors.New("fake rpc failure")

// minimalValidBlock returns a block that passes fetchBlockParticipation's
// nil-checks, with the given precommit addresses (all non-null/signed).
func minimalValidBlock(height int64, precommitAddrs ...types.Address) *ctypes.ResultBlock {
	precommits := make([]*types.CommitSig, 0, len(precommitAddrs))
	for _, a := range precommitAddrs {
		cs := types.CommitSig(types.Vote{ValidatorAddress: a})
		precommits = append(precommits, &cs)
	}
	return &ctypes.ResultBlock{
		Block: &types.Block{
			Header: types.Header{
				Height: height,
			},
			LastCommit: &types.Commit{
				Precommits: precommits,
			},
		},
	}
}
