package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
)

func TestBlockReaderHTTPQueries(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		methods = append(methods, request.Method)
		var result json.RawMessage
		switch request.Method {
		case "status":
			result = json.RawMessage(`{"sync_info":{"latest_block_height":"42"}}`)
		case "block":
			if string(request.Params["height"]) != `"42"` {
				t.Errorf("height = %s, want 42 encoded as an RPC integer", request.Params["height"])
			}
			result = json.RawMessage(`{"block":{"header":{"height":"42"},"last_commit":{"precommits":[null]}}}`)
		default:
			t.Errorf("unexpected RPC method %q", request.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()
	rpc, err := rpcclient.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	reader := blockReader{rpc: rpc}
	height, err := reader.LatestBlockHeight()
	if err != nil || height != 42 {
		t.Fatalf("height = %d, error = %v", height, err)
	}
	block, err := reader.Block(height)
	if err != nil {
		t.Fatal(err)
	}
	if block.Block == nil || block.Block.Header.Height != height || block.Block.LastCommit == nil || len(block.Block.LastCommit.Precommits) != 1 || block.Block.LastCommit.Precommits[0] != nil {
		t.Fatalf("block response was not preserved: %+v", block)
	}
	if strings.Join(methods, ",") != "status,block" {
		t.Fatalf("RPC methods = %v", methods)
	}
}

type failedBlockRPC struct {
	err   error
	calls int
}

func (c *failedBlockRPC) Status() (*ctypes.ResultStatus, error) {
	c.calls++
	return nil, c.err
}

func (c *failedBlockRPC) Block(*int64) (*ctypes.ResultBlock, error) {
	c.calls++
	return nil, c.err
}

func TestBlockReaderPreservesErrorsAndRejectsInvalidHeights(t *testing.T) {
	sentinel := errors.New("RPC unavailable")
	rpc := &failedBlockRPC{err: sentinel}
	reader := blockReader{rpc: rpc}
	if _, err := reader.LatestBlockHeight(); !errors.Is(err, sentinel) || err.Error() != "block number query failed: RPC unavailable" {
		t.Fatalf("height error = %v", err)
	}
	if _, err := reader.Block(1); !errors.Is(err, sentinel) || err.Error() != "block query failed: RPC unavailable" {
		t.Fatalf("block error = %v", err)
	}
	for _, height := range []int64{0, -1, -9223372036854775808} {
		before := rpc.calls
		if _, err := reader.Block(height); err == nil || err.Error() != "invalid block height provided" {
			t.Fatalf("height %d error = %v", height, err)
		}
		if rpc.calls != before {
			t.Fatalf("invalid height %d reached RPC", height)
		}
	}
	var missing blockReader
	if _, err := missing.LatestBlockHeight(); err == nil || err.Error() != "missing RPCClient" {
		t.Fatalf("missing RPC height error = %v", err)
	}
	if _, err := missing.Block(1); err == nil || err.Error() != "missing RPCClient" {
		t.Fatalf("missing RPC block error = %v", err)
	}
}
