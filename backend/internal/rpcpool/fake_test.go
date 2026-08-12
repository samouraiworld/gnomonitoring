package rpcpool

import (
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/bft/types"
)

// fakeConn is a stub rpcclient.Client whose Status and ABCIInfo behaviour is
// driven by the test. Every other method panics: nothing under test in this
// package calls them.
type fakeConn struct {
	endpoint string
	// statusErr, when non-nil, is what Status() returns.
	statusErr error
	// abciErr, when non-nil, is what ABCIInfo() returns.
	abciErr error
	// calls counts every ABCIInfo() invocation.
	calls *int
}

func (f *fakeConn) Status() (*ctypes.ResultStatus, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return &ctypes.ResultStatus{}, nil
}

func (f *fakeConn) ABCIInfo() (*ctypes.ResultABCIInfo, error) {
	if f.calls != nil {
		*f.calls++
	}
	if f.abciErr != nil {
		return nil, f.abciErr
	}
	return &ctypes.ResultABCIInfo{}, nil
}

func (f *fakeConn) ABCIQuery(path string, data []byte) (*ctypes.ResultABCIQuery, error) {
	panic("not implemented")
}

func (f *fakeConn) ABCIQueryWithOptions(path string, data []byte, opts rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	panic("not implemented")
}
func (f *fakeConn) BroadcastTxCommit(tx types.Tx) (*ctypes.ResultBroadcastTxCommit, error) {
	panic("not implemented")
}
func (f *fakeConn) BroadcastTxAsync(tx types.Tx) (*ctypes.ResultBroadcastTx, error) {
	panic("not implemented")
}
func (f *fakeConn) BroadcastTxSync(tx types.Tx) (*ctypes.ResultBroadcastTx, error) {
	panic("not implemented")
}
func (f *fakeConn) Genesis() (*ctypes.ResultGenesis, error) { panic("not implemented") }
func (f *fakeConn) BlockchainInfo(minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	panic("not implemented")
}
func (f *fakeConn) Block(height *int64) (*ctypes.ResultBlock, error) { panic("not implemented") }
func (f *fakeConn) BlockResults(height *int64) (*ctypes.ResultBlockResults, error) {
	panic("not implemented")
}
func (f *fakeConn) Commit(height *int64) (*ctypes.ResultCommit, error) { panic("not implemented") }
func (f *fakeConn) Tx(hash []byte) (*ctypes.ResultTx, error)           { panic("not implemented") }
func (f *fakeConn) Validators(height *int64) (*ctypes.ResultValidators, error) {
	panic("not implemented")
}
func (f *fakeConn) NetInfo() (*ctypes.ResultNetInfo, error) { panic("not implemented") }
func (f *fakeConn) DumpConsensusState() (*ctypes.ResultDumpConsensusState, error) {
	panic("not implemented")
}
func (f *fakeConn) ConsensusState() (*ctypes.ResultConsensusState, error) { panic("not implemented") }
func (f *fakeConn) ConsensusParams(height *int64) (*ctypes.ResultConsensusParams, error) {
	panic("not implemented")
}
func (f *fakeConn) Health() (*ctypes.ResultHealth, error) { panic("not implemented") }
func (f *fakeConn) UnconfirmedTxs(limit int) (*ctypes.ResultUnconfirmedTxs, error) {
	panic("not implemented")
}
func (f *fakeConn) NumUnconfirmedTxs() (*ctypes.ResultUnconfirmedTxs, error) {
	panic("not implemented")
}

// fakeDialerFrom builds a Dialer serving the given per-endpoint stubs.
func fakeDialerFrom(conns map[string]*fakeConn) Dialer {
	return func(endpoint string) (rpcclient.Client, error) {
		c, ok := conns[endpoint]
		if !ok {
			panic("test asked for an unknown endpoint: " + endpoint)
		}
		return c, nil
	}
}
