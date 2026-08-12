package rpcpool

import (
	"sync"

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

// flappingConn is a stub rpcclient.Client whose ABCIInfo verdict can flip
// from failing to succeeding mid-test, and whose next call can be parked
// mid-flight so a test can control interleaving between two concurrent
// callers. Every method it does not override panics, via the embedded
// fakeConn. Used to reproduce a call that is still in flight — and has
// already captured a failing verdict — when a second, later-started call
// observes the same endpoint has since recovered and resolves first.
type flappingConn struct {
	*fakeConn

	mu        sync.Mutex
	recovered bool
	// entered/release are set together by arm(). entered is a one-shot
	// marker: the next ABCIInfo call closes it as soon as it is invoked
	// (after capturing its verdict) and nils it out, so later calls
	// proceed straight through. release is left in place so releaseGate,
	// called after that one call has already consumed entered, can still
	// find and close the same channel that call is blocked on.
	entered chan struct{}
	release chan struct{}
}

// arm makes the next ABCIInfo call block after entering, and returns the
// channel that closes once that call is parked inside the block.
func (f *flappingConn) arm() (entered chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entered = make(chan struct{})
	f.release = make(chan struct{})
	return f.entered
}

// releaseGate unblocks the call parked by arm().
func (f *flappingConn) releaseGate() {
	f.mu.Lock()
	release := f.release
	f.mu.Unlock()
	if release != nil {
		close(release)
	}
}

// recover flips the endpoint from failing to succeeding. A call already
// parked by arm() is unaffected: it captures its verdict before blocking,
// not after.
func (f *flappingConn) recover() {
	f.mu.Lock()
	f.recovered = true
	f.mu.Unlock()
}

func (f *flappingConn) ABCIInfo() (*ctypes.ResultABCIInfo, error) {
	f.mu.Lock()
	recovered := f.recovered
	entered, release := f.entered, f.release
	f.entered = nil
	f.mu.Unlock()

	if entered != nil {
		close(entered)
		<-release
	}

	if recovered {
		return &ctypes.ResultABCIInfo{}, nil
	}
	return nil, errDown
}
