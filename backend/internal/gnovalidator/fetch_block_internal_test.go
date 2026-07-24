package gnovalidator

import (
	"testing"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
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

	_, _, _, _, ok := fetchBlockParticipation(client, 42)

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

	_, _, _, _, ok := fetchBlockParticipation(client, 42)

	if ok {
		t.Fatal("want ok=false when every attempt fails")
	}
	if attempts != 3 {
		t.Fatalf("want exactly 3 attempts (retry budget), got %d", attempts)
	}
}
