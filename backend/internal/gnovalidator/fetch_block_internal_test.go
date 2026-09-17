package gnovalidator

import (
	"testing"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/bft/types"
)

func TestFetchBlockParticipation_RetriesThenSucceeds(t *testing.T) {
	attempts := 0
	fake := &fakeRPCClient{blockFunc: func(height int64) (*ctypes.ResultBlock, error) {
		attempts++
		if attempts < 3 {
			return nil, errFakeRPC
		}
		return minimalValidBlock(height), nil
	}}
	client := gnoclient.Client{RPCClient: fake}

	_, ok := fetchBlockParticipation(client, 42)

	if !ok {
		t.Fatalf("want ok=true after succeeding within retry budget, got false (attempts=%d)", attempts)
	}
	if attempts != 3 {
		t.Fatalf("want 3 attempts (2 failures + 1 success), got %d", attempts)
	}
}

func TestFetchBlockParticipation_GivesUpAfterExhaustingRetries(t *testing.T) {
	attempts := 0
	fake := &fakeRPCClient{blockFunc: func(height int64) (*ctypes.ResultBlock, error) {
		attempts++
		return nil, errFakeRPC
	}}
	client := gnoclient.Client{RPCClient: fake}

	_, ok := fetchBlockParticipation(client, 42)

	if ok {
		t.Fatal("want ok=false when every attempt fails")
	}
	if attempts != 3 {
		t.Fatalf("want exactly 3 attempts (retry budget), got %d", attempts)
	}
}

func TestFetchBlockParticipation_ExtractsCommittedPrecommits(t *testing.T) {
	committed := types.BlockID{Hash: []byte("committed-hash")}
	other := types.BlockID{Hash: []byte("other-hash")}
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	addrA := types.Address{0x0a}
	addrB := types.Address{0x0b}
	addrC := types.Address{0x0c}
	sig := func(addr types.Address, id types.BlockID, ts time.Time) *types.CommitSig {
		cs := types.CommitSig(types.Vote{ValidatorAddress: addr, BlockID: id, Timestamp: ts})
		return &cs
	}

	fake := &fakeRPCClient{blockFunc: func(height int64) (*ctypes.ResultBlock, error) {
		return &ctypes.ResultBlock{Block: &types.Block{
			Header: types.Header{Height: height, Time: t0},
			LastCommit: &types.Commit{
				BlockID: committed,
				Precommits: []*types.CommitSig{
					sig(addrA, committed, t0.Add(5*time.Millisecond)),
					nil,
					sig(addrB, other, t0.Add(7*time.Millisecond)),
					sig(addrC, committed, t0.Add(9*time.Millisecond)),
				},
			},
		}}, nil
	}}

	fb, ok := fetchBlockParticipation(gnoclient.Client{RPCClient: fake}, 42)
	if !ok {
		t.Fatal("want ok=true")
	}

	if len(fb.PrecommitAddrs) != 3 {
		t.Errorf("PrecommitAddrs = %v, want 3 non-nil precommits (participation semantics unchanged)", fb.PrecommitAddrs)
	}
	if len(fb.Precommits) != 2 {
		t.Fatalf("Precommits = %+v, want only the 2 for the committed BlockID", fb.Precommits)
	}
	if fb.Precommits[0].Addr != addrA.String() || !fb.Precommits[0].Timestamp.Equal(t0.Add(5*time.Millisecond)) {
		t.Errorf("Precommits[0] = %+v", fb.Precommits[0])
	}
	if fb.Precommits[1].Addr != addrC.String() {
		t.Errorf("Precommits[1] = %+v", fb.Precommits[1])
	}
	if !fb.Time.Equal(t0) {
		t.Errorf("Time = %v, want %v", fb.Time, t0)
	}
}
