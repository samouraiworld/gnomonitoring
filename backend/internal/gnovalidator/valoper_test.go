package gnovalidator

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	rpctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/lib/types"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/crypto/ed25519"
	p2pTypes "github.com/gnolang/gno/tm2/pkg/p2p/types"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

// newMockRPCServer returns an httptest server that dispatches each JSON-RPC
// request to handlers[req.Method] and replies with the amino-JSON encoding
// of the returned result. A nil result with ok=false replies with an
// RPCInternalError, simulating a failing RPC method.
func newMockRPCServer(t *testing.T, handlers map[string]func() (result any, ok bool)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpctypes.RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		handler, found := handlers[req.Method]
		if !found {
			t.Fatalf("unexpected method %q", req.Method)
		}
		var resp rpctypes.RPCResponse
		if result, ok := handler(); ok {
			resp = rpctypes.NewRPCSuccessResponse(req.ID, result)
		} else {
			resp = rpctypes.RPCInternalError(req.ID, errors.New("mock RPC error"))
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
}

// singleResult returns a handler that always succeeds with result.
func singleResult(result any) func() (any, bool) {
	return func() (any, bool) { return result, true }
}

func TestExtractPort(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"explicit port", "http://127.0.0.1:26657", "26657"},
		{"https no port", "https://rpc.betanet.gno.land", "26657"},
		{"https explicit port", "https://rpc.betanet.gno.land:443", "443"},
		{"invalid url", "://bad", "26657"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPort(tc.url)
			if got != tc.want {
				t.Errorf("extractPort(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestIsPeerReachable(t *testing.T) {
	t.Run("returns true when the TCP port is open", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen() error = %v", err)
		}
		defer ln.Close()

		host, port, err := net.SplitHostPort(ln.Addr().String())
		if err != nil {
			t.Fatalf("SplitHostPort() error = %v", err)
		}

		if !isPeerReachable(host, port, time.Second) {
			t.Errorf("isPeerReachable() = false, want true for an open port")
		}
	})

	t.Run("returns false when the TCP port is closed", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen() error = %v", err)
		}
		host, port, err := net.SplitHostPort(ln.Addr().String())
		if err != nil {
			t.Fatalf("SplitHostPort() error = %v", err)
		}
		ln.Close() // free the port so nothing is listening

		if isPeerReachable(host, port, time.Second) {
			t.Errorf("isPeerReachable() = true, want false for a closed port")
		}
	})
}

func TestFetchMonikerFromStatus(t *testing.T) {
	t.Run("returns addr and moniker from a valid /status response", func(t *testing.T) {
		pubKey := ed25519.GenPrivKey().PubKey()
		wantAddr := crypto.AddressFromPreimage(pubKey.Bytes()).String()

		status := &ctypes.ResultStatus{
			NodeInfo: p2pTypes.NodeInfo{Moniker: "alice-node"},
			ValidatorInfo: ctypes.ValidatorInfo{
				Address: crypto.AddressFromPreimage(pubKey.Bytes()),
				PubKey:  pubKey,
			},
		}
		server := newMockRPCServer(t, map[string]func() (any, bool){
			"status": singleResult(status),
		})
		defer server.Close()

		addr, moniker, err := fetchMonikerFromStatus(server.URL)
		if err != nil {
			t.Fatalf("fetchMonikerFromStatus() error = %v", err)
		}
		if addr != wantAddr {
			t.Errorf("addr = %q, want %q", addr, wantAddr)
		}
		if moniker != "alice-node" {
			t.Errorf("moniker = %q, want %q", moniker, "alice-node")
		}
	})

	t.Run("returns an error when the RPC is unreachable", func(t *testing.T) {
		_, _, err := fetchMonikerFromStatus("http://127.0.0.1:0")
		if err == nil {
			t.Fatal("expected an error for unreachable RPC, got nil")
		}
	})

	t.Run("returns an error when the moniker is empty", func(t *testing.T) {
		pubKey := ed25519.GenPrivKey().PubKey()
		status := &ctypes.ResultStatus{
			NodeInfo: p2pTypes.NodeInfo{Moniker: ""},
			ValidatorInfo: ctypes.ValidatorInfo{
				Address: crypto.AddressFromPreimage(pubKey.Bytes()),
				PubKey:  pubKey,
			},
		}
		server := newMockRPCServer(t, map[string]func() (any, bool){
			"status": singleResult(status),
		})
		defer server.Close()

		_, _, err := fetchMonikerFromStatus(server.URL)
		if err == nil {
			t.Fatal("expected an error for empty moniker, got nil")
		}
	})
}

func TestDiscoverMonikersFromPeers(t *testing.T) {
	t.Run("discovers and persists a new moniker from a connected peer", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		pubKey := ed25519.GenPrivKey().PubKey()
		addr := crypto.AddressFromPreimage(pubKey.Bytes()).String()

		netInfo := &ctypes.ResultNetInfo{
			Peers: []ctypes.Peer{
				{RemoteIP: "127.0.0.1", NodeInfo: p2pTypes.NodeInfo{Moniker: "peer-net-info-moniker"}},
			},
		}
		status := &ctypes.ResultStatus{
			NodeInfo: p2pTypes.NodeInfo{Moniker: "alice-node"},
			ValidatorInfo: ctypes.ValidatorInfo{
				Address: crypto.AddressFromPreimage(pubKey.Bytes()),
				PubKey:  pubKey,
			},
		}
		server := newMockRPCServer(t, map[string]func() (any, bool){
			"net_info": singleResult(netInfo),
			"status":   singleResult(status),
		})
		defer server.Close()

		existing := map[string]string{}
		discovered := discoverMonikersFromPeers(db, testoutils.TestChainID, []string{server.URL}, existing)

		if got := discovered[addr]; got != "alice-node" {
			t.Errorf("discovered[%s] = %q, want %q", addr, got, "alice-node")
		}
		dbMap, err := database.GetMoniker(db, testoutils.TestChainID)
		if err != nil {
			t.Fatalf("GetMoniker() error = %v", err)
		}
		if got := dbMap[addr]; got != "alice-node" {
			t.Errorf("DB moniker for %s = %q, want %q", addr, got, "alice-node")
		}
	})

	t.Run("does not overwrite an existing moniker", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		pubKey := ed25519.GenPrivKey().PubKey()
		addr := crypto.AddressFromPreimage(pubKey.Bytes()).String()

		if err := database.UpsertAddrMoniker(db, testoutils.TestChainID, addr, "admin-override"); err != nil {
			t.Fatalf("seed UpsertAddrMoniker() error = %v", err)
		}

		netInfo := &ctypes.ResultNetInfo{
			Peers: []ctypes.Peer{
				{RemoteIP: "127.0.0.1", NodeInfo: p2pTypes.NodeInfo{Moniker: "peer-net-info-moniker"}},
			},
		}
		status := &ctypes.ResultStatus{
			NodeInfo: p2pTypes.NodeInfo{Moniker: "alice-node"},
			ValidatorInfo: ctypes.ValidatorInfo{
				Address: crypto.AddressFromPreimage(pubKey.Bytes()),
				PubKey:  pubKey,
			},
		}
		server := newMockRPCServer(t, map[string]func() (any, bool){
			"net_info": singleResult(netInfo),
			"status":   singleResult(status),
		})
		defer server.Close()

		existing := map[string]string{addr: "admin-override"}
		discovered := discoverMonikersFromPeers(db, testoutils.TestChainID, []string{server.URL}, existing)

		if _, ok := discovered[addr]; ok {
			t.Errorf("discovered[%s] = %q, want not present", addr, discovered[addr])
		}
		dbMap, err := database.GetMoniker(db, testoutils.TestChainID)
		if err != nil {
			t.Fatalf("GetMoniker() error = %v", err)
		}
		if got := dbMap[addr]; got != "admin-override" {
			t.Errorf("DB moniker for %s = %q, want unchanged %q", addr, got, "admin-override")
		}
	})

	t.Run("treats a stale 'unknown' placeholder as not-yet-found and retries discovery", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		pubKey := ed25519.GenPrivKey().PubKey()
		addr := crypto.AddressFromPreimage(pubKey.Bytes()).String()

		if err := database.UpsertAddrMoniker(db, testoutils.TestChainID, addr, "unknown"); err != nil {
			t.Fatalf("seed UpsertAddrMoniker() error = %v", err)
		}

		netInfo := &ctypes.ResultNetInfo{
			Peers: []ctypes.Peer{
				{RemoteIP: "127.0.0.1", NodeInfo: p2pTypes.NodeInfo{Moniker: "peer-net-info-moniker"}},
			},
		}
		status := &ctypes.ResultStatus{
			NodeInfo: p2pTypes.NodeInfo{Moniker: "alice-node"},
			ValidatorInfo: ctypes.ValidatorInfo{
				Address: crypto.AddressFromPreimage(pubKey.Bytes()),
				PubKey:  pubKey,
			},
		}
		server := newMockRPCServer(t, map[string]func() (any, bool){
			"net_info": singleResult(netInfo),
			"status":   singleResult(status),
		})
		defer server.Close()

		existing := map[string]string{addr: "unknown"}
		discovered := discoverMonikersFromPeers(db, testoutils.TestChainID, []string{server.URL}, existing)

		if got := discovered[addr]; got != "alice-node" {
			t.Errorf("discovered[%s] = %q, want %q", addr, got, "alice-node")
		}
		dbMap, err := database.GetMoniker(db, testoutils.TestChainID)
		if err != nil {
			t.Fatalf("GetMoniker() error = %v", err)
		}
		if got := dbMap[addr]; got != "alice-node" {
			t.Errorf("DB moniker for %s = %q, want %q", addr, got, "alice-node")
		}
	})

	t.Run("skips a peer whose /status call fails, without error", func(t *testing.T) {
		db := testoutils.NewTestDB(t)

		netInfo := &ctypes.ResultNetInfo{
			Peers: []ctypes.Peer{
				{RemoteIP: "127.0.0.1", NodeInfo: p2pTypes.NodeInfo{Moniker: "peer-net-info-moniker"}},
			},
		}
		server := newMockRPCServer(t, map[string]func() (any, bool){
			"net_info": singleResult(netInfo),
			"status":   func() (any, bool) { return nil, false }, // simulate RPC error
		})
		defer server.Close()

		existing := map[string]string{}
		discovered := discoverMonikersFromPeers(db, testoutils.TestChainID, []string{server.URL}, existing)

		if len(discovered) != 0 {
			t.Errorf("discovered = %v, want empty", discovered)
		}
		dbMap, err := database.GetMoniker(db, testoutils.TestChainID)
		if err != nil {
			t.Fatalf("GetMoniker() error = %v", err)
		}
		if len(dbMap) != 0 {
			t.Errorf("DB monikers = %v, want empty", dbMap)
		}
	})

	t.Run("ignores an unreachable rpc endpoint without error", func(t *testing.T) {
		db := testoutils.NewTestDB(t)

		existing := map[string]string{}
		discovered := discoverMonikersFromPeers(db, testoutils.TestChainID, []string{"http://127.0.0.1:0"}, existing)

		if len(discovered) != 0 {
			t.Errorf("discovered = %v, want empty", discovered)
		}
	})

	t.Run("skips a peer whose RPC port is unreachable, without calling /status", func(t *testing.T) {
		db := testoutils.NewTestDB(t)

		netInfo := &ctypes.ResultNetInfo{
			Peers: []ctypes.Peer{
				// 127.0.0.2 has nothing listening on the mock server's port:
				// httptest.NewServer binds to 127.0.0.1 only.
				{RemoteIP: "127.0.0.2", NodeInfo: p2pTypes.NodeInfo{Moniker: "ghost-peer"}},
			},
		}
		statusCalls := 0
		server := newMockRPCServer(t, map[string]func() (any, bool){
			"net_info": singleResult(netInfo),
			"status": func() (any, bool) {
				statusCalls++
				return nil, false
			},
		})
		defer server.Close()

		existing := map[string]string{}
		discovered := discoverMonikersFromPeers(db, testoutils.TestChainID, []string{server.URL}, existing)

		if statusCalls != 0 {
			t.Errorf("/status called %d times, want 0 (unreachable peer should be skipped)", statusCalls)
		}
		if len(discovered) != 0 {
			t.Errorf("discovered = %v, want empty", discovered)
		}
	})

	t.Run("dedupes peers sharing the same remote IP and skips peers with empty IP", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		pubKey := ed25519.GenPrivKey().PubKey()
		addr := crypto.AddressFromPreimage(pubKey.Bytes()).String()

		netInfo := &ctypes.ResultNetInfo{
			Peers: []ctypes.Peer{
				{RemoteIP: "", NodeInfo: p2pTypes.NodeInfo{Moniker: "no-ip-peer"}},
				{RemoteIP: "127.0.0.1", NodeInfo: p2pTypes.NodeInfo{Moniker: "peer-1"}},
				{RemoteIP: "127.0.0.1", NodeInfo: p2pTypes.NodeInfo{Moniker: "peer-2"}},
			},
		}
		status := &ctypes.ResultStatus{
			NodeInfo: p2pTypes.NodeInfo{Moniker: "bob-node"},
			ValidatorInfo: ctypes.ValidatorInfo{
				Address: crypto.AddressFromPreimage(pubKey.Bytes()),
				PubKey:  pubKey,
			},
		}
		statusCalls := 0
		server := newMockRPCServer(t, map[string]func() (any, bool){
			"net_info": singleResult(netInfo),
			"status": func() (any, bool) {
				statusCalls++
				return status, true
			},
		})
		defer server.Close()

		existing := map[string]string{}
		discovered := discoverMonikersFromPeers(db, testoutils.TestChainID, []string{server.URL}, existing)

		if statusCalls != 1 {
			t.Errorf("/status called %d times, want 1 (deduped)", statusCalls)
		}
		if got := discovered[addr]; got != "bob-node" {
			t.Errorf("discovered[%s] = %q, want %q", addr, got, "bob-node")
		}
	})
}

func TestResolveMoniker(t *testing.T) {
	cases := []struct {
		name          string
		dbMap         map[string]string
		valoperMap    map[string]string
		genesisMap    map[string]string
		discoveredMap map[string]string
		want          string
	}{
		{
			name:          "db override wins over valoper, genesis and discovered",
			dbMap:         map[string]string{"addr1": "db-name"},
			valoperMap:    map[string]string{"addr1": "valoper-name"},
			genesisMap:    map[string]string{"addr1": "genesis-name"},
			discoveredMap: map[string]string{"addr1": "discovered-name"},
			want:          "db-name",
		},
		{
			name:          "stale 'unknown' in db falls through to valoper",
			dbMap:         map[string]string{"addr1": "unknown"},
			valoperMap:    map[string]string{"addr1": "valoper-name"},
			genesisMap:    map[string]string{"addr1": "genesis-name"},
			discoveredMap: map[string]string{"addr1": "discovered-name"},
			want:          "valoper-name",
		},
		{
			name:          "valoper wins over genesis and discovered",
			dbMap:         map[string]string{},
			valoperMap:    map[string]string{"addr1": "valoper-name"},
			genesisMap:    map[string]string{"addr1": "genesis-name"},
			discoveredMap: map[string]string{"addr1": "discovered-name"},
			want:          "valoper-name",
		},
		{
			name:          "genesis wins over discovered",
			dbMap:         map[string]string{},
			valoperMap:    map[string]string{},
			genesisMap:    map[string]string{"addr1": "genesis-name"},
			discoveredMap: map[string]string{"addr1": "discovered-name"},
			want:          "genesis-name",
		},
		{
			name:          "discovered is the last resort",
			dbMap:         map[string]string{},
			valoperMap:    map[string]string{},
			genesisMap:    map[string]string{},
			discoveredMap: map[string]string{"addr1": "discovered-name"},
			want:          "discovered-name",
		},
		{
			name:          "falls back to unknown when no source has the address",
			dbMap:         map[string]string{},
			valoperMap:    map[string]string{},
			genesisMap:    map[string]string{},
			discoveredMap: map[string]string{},
			want:          "unknown",
		},
		{
			name:          "stale 'unknown' in db with no other source resolves to unknown",
			dbMap:         map[string]string{"addr1": "unknown"},
			valoperMap:    map[string]string{},
			genesisMap:    map[string]string{},
			discoveredMap: map[string]string{},
			want:          "unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveMoniker("addr1", tc.dbMap, tc.valoperMap, tc.genesisMap, tc.discoveredMap)
			if got != tc.want {
				t.Errorf("resolveMoniker() = %q, want %q", got, tc.want)
			}
		})
	}
}
