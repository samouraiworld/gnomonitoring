// Synthetic protocol fixtures. This program never forwards a request.
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gnolang/gno/tm2/pkg/amino"
	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/bft/types"
	"github.com/gnolang/gno/tm2/pkg/crypto/ed25519"
	"github.com/gorilla/websocket"
	"github.com/machinebox/graphql"
)

const maxBody = 65536

var chainNames = []string{"synthetic-chain", "synthetic-peer-chain"}
var tokens = map[string]string{"900001:synthetic-validator": "validator", "900002:synthetic-govdao": "govdao"}

type recordedMessage struct {
	Bot    string `json:"bot"`
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
	Method string `json:"method"`
}
type updateMessage struct {
	ID      int `json:"update_id"`
	Message any `json:"message"`
}
type fixture struct {
	mu         sync.Mutex
	reference  time.Time
	heights    map[string]int64
	counters   map[string]int
	unexpected int
	messages   []recordedMessage
	updates    map[string][]updateMessage
	nextUpdate int
	proposals  map[string]int
	validators []*types.Validator
}

func newFixture() *fixture {
	f := &fixture{reference: time.Now().UTC().Truncate(time.Millisecond), heights: map[string]int64{}, counters: map[string]int{}, messages: []recordedMessage{}, updates: map[string][]updateMessage{}, proposals: map[string]int{}}
	for _, c := range chainNames {
		f.heights[c] = 110
	}
	for i := byte(1); i <= 3; i++ {
		var pub ed25519.PubKeyEd25519
		for j := range pub {
			pub[j] = i
		}
		f.validators = append(f.validators, types.NewValidator(pub, 10))
	}
	return f
}
func (f *fixture) count(key string) { f.mu.Lock(); f.counters[key]++; f.mu.Unlock() }
func (f *fixture) reject(w http.ResponseWriter, kind string) {
	f.mu.Lock()
	f.unexpected++
	f.counters["rejected."+kind]++
	f.mu.Unlock()
	http.Error(w, "unexpected synthetic request", http.StatusBadRequest)
}
func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func chainFrom(path, prefix string) (string, string, bool) {
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	for _, c := range chainNames {
		if parts[0] == c {
			suffix := ""
			if len(parts) > 1 {
				suffix = parts[1]
			}
			return c, suffix, true
		}
	}
	return "", "", false
}
func boundedJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, maxBody+1)).Decode(v)
}
func (f *fixture) state(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		f.reject(w, "state_method")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	vals := []any{}
	for i, v := range f.validators {
		vals = append(vals, map[string]any{"address": v.Address.String(), "moniker": []string{"worker-alpha", "worker-beta", "worker-gamma"}[i], "power": v.VotingPower})
	}
	respond(w, map[string]any{"synthetic_only": true, "counters": f.counters, "unexpected": f.unexpected, "messages": f.messages, "validators": vals, "reference_time": f.reference, "heights": f.heights})
}
func (f *fixture) control(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		f.reject(w, "control_method")
		return
	}
	var c struct {
		Action string `json:"action"`
		Chain  string `json:"chain"`
		Height int64  `json:"height"`
		Bot    string `json:"bot"`
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	if boundedJSON(r, &c) != nil {
		f.reject(w, "control_json")
		return
	}
	switch c.Action {
	case "height":
		if c.Height < 1 || c.Height > 2000 || (c.Chain != chainNames[0] && c.Chain != chainNames[1]) {
			f.reject(w, "control_height")
			return
		}
		f.mu.Lock()
		f.heights[c.Chain] = c.Height
		f.mu.Unlock()
	case "telegram":
		if (c.Bot != "validator" && c.Bot != "govdao") || c.ChatID < 1 || len(c.Text) > 4096 || !strings.HasPrefix(c.Text, "/") {
			f.reject(w, "control_telegram")
			return
		}
		command := strings.SplitN(c.Text, " ", 2)[0]
		f.mu.Lock()
		if f.nextUpdate >= 200 {
			f.mu.Unlock()
			f.reject(w, "control_update_budget")
			return
		}
		f.nextUpdate++
		f.updates[c.Bot] = append(f.updates[c.Bot], updateMessage{f.nextUpdate, map[string]any{"message_id": f.nextUpdate, "chat": map[string]any{"id": c.ChatID, "type": "private"}, "text": c.Text, "entities": []any{map[string]any{"type": "bot_command", "offset": 0, "length": len(command)}}}})
		f.mu.Unlock()
	case "proposal":
		if c.Chain != chainNames[0] && c.Chain != chainNames[1] {
			f.reject(w, "control_proposal")
			return
		}
		f.mu.Lock()
		f.proposals[c.Chain]++
		f.mu.Unlock()
	default:
		f.reject(w, "control_action")
		return
	}
	respond(w, map[string]any{"ok": true})
}
func (f *fixture) height(chain string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heights[chain]
}
func (f *fixture) blockTime(height int64) time.Time {
	// All accepted heights (1..2000), including the +600 backfill jump,
	// remain before the captured reference. The extra second also keeps
	// the invented +100/+200ms precommit samples out of the future.
	return f.reference.Add(time.Duration(height-2000)*time.Millisecond - time.Second)
}
func (f *fixture) rpcResult(chain, method string, params map[string]json.RawMessage) (any, error) {
	height := f.height(chain)
	if raw, ok := params["height"]; ok && string(raw) != "null" {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			height, _ = strconv.ParseInt(s, 10, 64)
		} else {
			_ = json.Unmarshal(raw, &height)
		}
	}
	switch method {
	case "status":
		result := &ctypes.ResultStatus{}
		result.SyncInfo.LatestBlockHeight = f.height(chain)
		result.SyncInfo.LatestBlockTime = f.blockTime(f.height(chain))
		result.NodeInfo.Network = chain
		result.NodeInfo.Moniker = "invented-node"
		result.ValidatorInfo.Address = f.validators[0].Address
		result.ValidatorInfo.PubKey = f.validators[0].PubKey
		result.ValidatorInfo.VotingPower = 10
		return result, nil
	case "validators":
		return &ctypes.ResultValidators{BlockHeight: height, Validators: f.validators}, nil
	case "genesis":
		return map[string]any{"genesis": map[string]any{"chain_id": chain, "validators": []any{map[string]any{"address": f.validators[0].Address.String(), "name": "worker-alpha"}, map[string]any{"address": f.validators[1].Address.String(), "name": "worker-beta"}, map[string]any{"address": f.validators[2].Address.String(), "name": "worker-gamma"}}}}, nil
	case "block":
		if height < 1 || height > f.height(chain) {
			return nil, errors.New("invented height unavailable")
		}
		blockID := types.BlockID{Hash: []byte("synthetic-previous-block-hash-0001")}
		commit := &types.Commit{BlockID: blockID}
		for i, v := range f.validators {
			vote := types.Vote{Height: height - 1, Type: types.PrecommitType, BlockID: blockID, Timestamp: f.blockTime(height).Add(time.Duration(i) * 100 * time.Millisecond), ValidatorAddress: v.Address, ValidatorIndex: i}
			sig := types.CommitSig(vote)
			commit.Precommits = append(commit.Precommits, &sig)
		}
		return &ctypes.ResultBlock{Block: &types.Block{Header: types.Header{ChainID: chain, Height: height, Time: f.blockTime(height), ProposerAddress: f.validators[0].Address}, LastCommit: commit}}, nil
	case "net_info":
		return &ctypes.ResultNetInfo{Listening: true, Listeners: []string{}, NPeers: 0, Peers: []ctypes.Peer{}}, nil
	case "num_unconfirmed_txs", "unconfirmed_txs":
		return &ctypes.ResultUnconfirmedTxs{}, nil
	case "consensus_state":
		return map[string]any{"round_state": map[string]any{"height/round/step": fmt.Sprintf("%d/0/1", f.height(chain))}}, nil
	case "dump_consensus_state":
		return &ctypes.ResultDumpConsensusState{Peers: []ctypes.PeerStateInfo{}}, nil
	case "health":
		return &ctypes.ResultHealth{}, nil
	case "abci_info":
		return &ctypes.ResultABCIInfo{}, nil
	case "abci_query":
		var path string
		_ = json.Unmarshal(params["path"], &path)
		var data []byte
		if raw := params["data"]; raw != nil {
			if err := amino.UnmarshalJSON(raw, &data); err != nil {
				var s string
				_ = json.Unmarshal(raw, &s)
				data = []byte(s)
			}
		}
		query := string(data)
		var answer string
		switch {
		case path == "vm/qeval" && strings.Contains(query, "gno.land/r/gnops/valopers.Render("):
			answer = "(\"\" string)"
		case path == "vm/qeval" && strings.Contains(query, "gno.land/r/gov/dao.proposals.GetProposal(") && strings.Contains(query, ".Title()"):
			answer = "(\"Synthetic proposal\" string)"
		case path == "vm/qrender" && strings.HasPrefix(query, "gno.land/r/gov/dao:"):
			answer = "**Proposal is open for votes**"
		case path == "vm/qrender" && strings.HasPrefix(query, "gno.land/r/sys/validators/v2:"):
			answer = "# Synthetic validators\n"
		default:
			return nil, fmt.Errorf("unexpected ABCI query %q", path)
		}
		return &ctypes.ResultABCIQuery{Response: abci.ResponseQuery{ResponseBase: abci.ResponseBase{Data: []byte(answer)}, Height: height}}, nil
	default:
		return nil, fmt.Errorf("unexpected RPC method %q", method)
	}
}
func (f *fixture) rpc(w http.ResponseWriter, r *http.Request) {
	chain, suffix, ok := chainFrom(r.URL.Path, "/rpc/")
	if !ok {
		f.reject(w, "rpc_chain")
		return
	}
	method := suffix
	params := map[string]json.RawMessage{}
	var id any = "synthetic"
	if r.Method == "POST" && suffix == "" {
		var req struct {
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
			ID     any                        `json:"id"`
		}
		if boundedJSON(r, &req) != nil {
			f.reject(w, "rpc_json")
			return
		}
		method = req.Method
		params = req.Params
		id = req.ID
	} else if r.Method == "GET" && suffix != "" {
		for k, v := range r.URL.Query() {
			params[k] = json.RawMessage(strconv.Quote(v[0]))
		}
	} else {
		f.reject(w, "rpc_method")
		return
	}
	result, err := f.rpcResult(chain, method, params)
	if err != nil {
		f.reject(w, "rpc_contract")
		return
	}
	f.count("rpc." + chain + "." + method)
	// Gno's own Amino codec supplies the exact pinned RPC result wire shape.
	var raw []byte
	if _, ok := result.(map[string]any); ok {
		raw, err = json.Marshal(result)
	} else {
		raw, err = amino.MarshalJSON(result)
	}
	if err != nil {
		f.reject(w, "rpc_encode")
		return
	}
	respond(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(raw)})
}
func (f *fixture) rpcForChain(chain string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/rpc/" + chain + r.URL.Path
		r.URL.Path = strings.TrimSuffix(r.URL.Path, "/")
		f.rpc(w, r)
	}
}
func syntheticTransaction(id string) any {
	return map[string]any{"block_height": 110, "index": 0, "response": map[string]any{"events": []any{map[string]any{"type": "ProposalCreated", "attrs": []any{map[string]any{"key": "id", "value": id}}}}}}
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }, ReadBufferSize: 1024, WriteBufferSize: 1024}

func (f *fixture) graphql(w http.ResponseWriter, r *http.Request) {
	chain, tail, ok := chainFrom(r.URL.Path, "/graphql/")
	if !ok || tail != "" {
		f.reject(w, "graphql_chain")
		return
	}
	if websocket.IsWebSocketUpgrade(r) {
		f.websocket(w, r, chain)
		return
	}
	if r.Method != "POST" {
		f.reject(w, "graphql_method")
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if boundedJSON(r, &req) != nil {
		f.reject(w, "graphql_json")
		return
	}
	switch {
	case strings.Contains(req.Query, "query getEvents") && strings.Contains(req.Query, "getTransactions"):
		f.count("graphql." + chain + ".events")
		respond(w, map[string]any{"data": map[string]any{"getTransactions": []any{syntheticTransaction("701")}}})
	case strings.Contains(req.Query, "getSpecificBlocksByHeight") && strings.Contains(req.Query, "getBlocks"):
		f.count("graphql." + chain + ".blocks")
		respond(w, map[string]any{"data": map[string]any{"getBlocks": []any{map[string]any{"txs": []any{map[string]any{"hash": "SYNTHETIC_TRANSACTION_HASH"}}}}}})
	default:
		f.reject(w, "graphql_contract")
	}
}
func (f *fixture) websocket(w http.ResponseWriter, r *http.Request, chain string) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetReadLimit(maxBody)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var init struct {
		Type string `json:"type"`
	}
	if c.ReadJSON(&init) != nil || init.Type != "connection_init" {
		f.count("websocket.invalid")
		return
	}
	_ = c.WriteJSON(map[string]any{"type": "connection_ack"})
	var start struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Payload struct {
			Query string `json:"query"`
		} `json:"payload"`
	}
	if c.ReadJSON(&start) != nil || start.Type != "start" || !strings.Contains(start.Payload.Query, "subscription") || !strings.Contains(start.Payload.Query, "ProposalCreated") {
		f.count("websocket.invalid")
		return
	}
	f.count("websocket." + chain + ".subscribed")
	last := 0
	for {
		f.mu.Lock()
		wanted := f.proposals[chain]
		f.mu.Unlock()
		if wanted > last {
			if c.WriteJSON(map[string]any{"id": start.ID, "type": "data", "payload": map[string]any{"data": map[string]any{"getTransactions": syntheticTransaction("702")}}}) != nil {
				return
			}
			f.count("websocket." + chain + ".proposal")
			last = wanted
		}
		if c.WriteControl(websocket.PingMessage, []byte("invented"), time.Now().Add(time.Second)) != nil {
			return
		}
		time.Sleep(time.Second)
	}
}
func (f *fixture) invalidTelegram() (int, any) {
	f.mu.Lock()
	f.unexpected++
	f.mu.Unlock()
	return 400, map[string]any{"ok": false, "description": "unexpected synthetic Telegram call"}
}
func (f *fixture) telegram(r *http.Request) (int, any) {
	if r.Host != "api.telegram.org" {
		return f.invalidTelegram()
	}
	rest := strings.TrimPrefix(r.URL.Path, "/bot")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		return f.invalidTelegram()
	}
	bot, ok := tokens[parts[0]]
	if !ok {
		return f.invalidTelegram()
	}
	method := parts[1]
	if method != "getMe" && method != "getUpdates" && method != "sendMessage" && method != "editMessageText" && method != "answerCallbackQuery" {
		return f.invalidTelegram()
	}
	f.count("telegram." + bot + "." + method)
	switch method {
	case "getMe":
		if r.Method != "GET" {
			break
		}
		return 200, map[string]any{"ok": true, "result": map[string]any{"id": 900001, "is_bot": true, "username": "synthetic_" + bot + "_bot"}}
	case "getUpdates":
		if r.Method != "GET" {
			break
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		f.mu.Lock()
		updates := []updateMessage{}
		for _, u := range f.updates[bot] {
			if u.ID >= offset {
				updates = append(updates, u)
			}
		}
		f.mu.Unlock()
		if len(updates) == 0 {
			time.Sleep(250 * time.Millisecond)
		}
		return 200, map[string]any{"ok": true, "result": updates}
	case "sendMessage", "editMessageText", "answerCallbackQuery":
		if r.Method != "POST" {
			break
		}
		var body struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if boundedJSON(r, &body) != nil || len(body.Text) > 8192 {
			break
		}
		f.mu.Lock()
		if len(f.messages) >= 200 {
			f.mu.Unlock()
			return 429, map[string]any{"ok": false, "description": "synthetic message budget exceeded"}
		}
		f.messages = append(f.messages, recordedMessage{bot, body.ChatID, body.Text, method})
		f.mu.Unlock()
		return 200, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}}
	}
	return f.invalidTelegram()
}
func makeTLS(directory string) (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial := big.NewInt(time.Now().UnixNano())
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Gnomonitoring synthetic CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(directory+"/ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		return nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leaf := &x509.Certificate{SerialNumber: new(big.Int).Add(serial, big.NewInt(1)), DNSNames: []string{"api.telegram.org"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}
func (f *fixture) proxy(config *tls.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "api.telegram.org:443" {
			f.reject(w, "proxy_destination")
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			f.reject(w, "proxy_hijack")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(120 * time.Second))
		if _, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		secure := tls.Server(conn, config)
		if secure.Handshake() != nil {
			f.count("telegram.tls_failed")
			return
		}
		f.count("telegram.tls_verified_transport")
		reader := bufio.NewReader(secure)
		for i := 0; i < 300; i++ {
			req, err := http.ReadRequest(reader)
			if err != nil {
				return
			}
			code, body := f.telegram(req)
			_ = req.Body.Close()
			payload, _ := json.Marshal(body)
			response := http.Response{StatusCode: code, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload))), ContentLength: int64(len(payload))}
			if response.Write(secure) != nil {
				return
			}
			if req.Close {
				return
			}
		}
	}
}
func main() {
	log.SetOutput(io.Discard)
	if len(os.Args) > 1 && os.Args[1] == "self-test" {
		if err := selfTest(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("synthetic protocol contracts verified")
		return
	}
	directory := "/run/gnomon-test"
	if err := os.MkdirAll(directory, 0700); err != nil {
		panic("synthetic private directory unavailable")
	}
	config, err := makeTLS(directory)
	if err != nil {
		panic("synthetic TLS preparation failed")
	}
	f := newFixture()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]any{"ready": true, "synthetic_only": true})
	})
	mux.HandleFunc("/state", f.state)
	mux.HandleFunc("/control", f.control)
	servers := []struct {
		address string
		handler http.Handler
	}{{"127.0.0.1:18080", mux}, {"127.0.0.1:26657", f.rpcForChain(chainNames[0])}, {"127.0.0.1:26658", f.rpcForChain(chainNames[1])}, {"127.0.0.1:8080", http.HandlerFunc(f.graphql)}, {"127.0.0.1:8443", f.proxy(config)}}
	for _, s := range servers {
		listener, e := net.Listen("tcp", s.address)
		if e != nil {
			panic("synthetic listener unavailable")
		}
		server := &http.Server{Handler: s.handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
		go func() {
			if server.Serve(listener) != nil {
				os.Exit(1)
			}
		}()
	}
	select {}
}
func selfTest() error {
	f := newFixture()
	stateResponse := httptest.NewRecorder()
	f.state(stateResponse, httptest.NewRequest("GET", "/state", nil))
	var initialState map[string]any
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &initialState); err != nil {
		return err
	}
	if messages, ok := initialState["messages"].([]any); !ok || len(messages) != 0 {
		return errors.New("initial message state must be an empty array")
	}
	for _, method := range []string{"status", "validators", "block", "net_info", "abci_info", "health", "num_unconfirmed_txs", "dump_consensus_state"} {
		result, err := f.rpcResult(chainNames[0], method, nil)
		if err != nil {
			return err
		}
		wire, err := amino.MarshalJSON(result)
		if err != nil {
			return err
		}
		switch method {
		case "status":
			var target ctypes.ResultStatus
			err = amino.UnmarshalJSON(wire, &target)
			if target.SyncInfo.LatestBlockHeight != 110 {
				return errors.New("status height differs")
			}
		case "validators":
			var target ctypes.ResultValidators
			err = amino.UnmarshalJSON(wire, &target)
			if len(target.Validators) != 3 {
				return errors.New("validator set differs")
			}
		case "block":
			var target ctypes.ResultBlock
			err = amino.UnmarshalJSON(wire, &target)
			if target.Block == nil || target.Block.LastCommit == nil || len(target.Block.LastCommit.Precommits) != 3 {
				return errors.New("block commit differs")
			}
		}
		if err != nil {
			return err
		}
	}
	server := httptest.NewServer(f.rpcForChain(chainNames[0]))
	defer server.Close()
	client, err := rpcclient.NewHTTPClient(server.URL)
	if err != nil {
		return err
	}
	status, err := client.Status()
	if err != nil || status.SyncInfo.LatestBlockHeight != 110 {
		return fmt.Errorf("real RPC status decoder: %v", err)
	}
	height := int64(109)
	block, err := client.Block(&height)
	if err != nil || block.Block.Header.Height != height || len(block.Block.LastCommit.Precommits) != 3 {
		return fmt.Errorf("real RPC block decoder: %v", err)
	}
	validators, err := client.Validators(&height)
	if err != nil || len(validators.Validators) != 3 {
		return fmt.Errorf("real RPC validator decoder: %v", err)
	}
	query, err := client.ABCIQuery("vm/qeval", []byte("gno.land/r/gov/dao.proposals.GetProposal(701).Title()"))
	if err != nil || string(query.Response.Data) != "(\"Synthetic proposal\" string)" {
		return fmt.Errorf("real ABCI data decoder: %v", err)
	}
	if _, err = client.NetInfo(); err != nil {
		return err
	}
	if _, err = client.ConsensusState(); err != nil {
		return err
	}
	if _, err = client.DumpConsensusState(); err != nil {
		return err
	}
	gql := httptest.NewServer(http.HandlerFunc(f.graphql))
	defer gql.Close()
	gqlClient := graphql.NewClient(gql.URL + "/graphql/" + chainNames[0])
	req := graphql.NewRequest("query getEvents { getTransactions { block_height index response { events { type attrs { key value } } } } }")
	var eventResult struct {
		GetTransactions []struct {
			BlockHeight int `json:"block_height"`
		} `json:"getTransactions"`
	}
	if err = gqlClient.Run(context.Background(), req, &eventResult); err != nil || len(eventResult.GetTransactions) != 1 || eventResult.GetTransactions[0].BlockHeight != 110 {
		return fmt.Errorf("real GraphQL decoder: %v", err)
	}
	ws, _, err := websocket.DefaultDialer.Dial(strings.Replace(gql.URL, "http", "ws", 1)+"/graphql/"+chainNames[0], nil)
	if err != nil {
		return err
	}
	defer ws.Close()
	if err = ws.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		return err
	}
	var event map[string]any
	if err = ws.ReadJSON(&event); err != nil || event["type"] != "connection_ack" {
		return fmt.Errorf("WebSocket init: %v", err)
	}
	f.mu.Lock()
	f.proposals[chainNames[0]] = 1
	f.mu.Unlock()
	if err = ws.WriteJSON(map[string]any{"id": "1", "type": "start", "payload": map[string]any{"query": "subscription { getTransactions ProposalCreated }"}}); err != nil {
		return err
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err = ws.ReadJSON(&event); err != nil || event["type"] != "data" {
		return fmt.Errorf("WebSocket proposal delivery: %v", err)
	}
	directory, err := os.MkdirTemp("", "gnomon-protocol-tls-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	tlsConfig, err := makeTLS(directory)
	if err != nil {
		return err
	}
	proxy := httptest.NewServer(f.proxy(tlsConfig))
	defer proxy.Close()
	caBytes, err := os.ReadFile(directory + "/ca.crt")
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caBytes) {
		return errors.New("synthetic CA parse failed")
	}
	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := httpClient.Get("https://api.telegram.org/bot900001:synthetic-validator/getMe")
	if err != nil {
		return fmt.Errorf("Telegram TLS proxy: %v", err)
	}
	var me struct {
		Ok     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	err = json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if err != nil || !me.Ok || me.Result.Username != "synthetic_validator_bot" {
		return errors.New("Telegram getMe shape differs")
	}
	controlRequest := httptest.NewRequest("POST", "/control", strings.NewReader(`{"action":"telegram","bot":"validator","chat_id":71001,"text":"/validator alpha"}`))
	controlResponse := httptest.NewRecorder()
	f.control(controlResponse, controlRequest)
	if controlResponse.Code != 200 {
		return errors.New("Telegram queue control failed")
	}
	resp, err = httpClient.Get("https://api.telegram.org/bot900001:synthetic-validator/getUpdates?offset=1")
	if err != nil {
		return err
	}
	var updates struct {
		Result []updateMessage `json:"result"`
	}
	err = json.NewDecoder(resp.Body).Decode(&updates)
	resp.Body.Close()
	if err != nil || len(updates.Result) != 1 {
		return errors.New("Telegram update queue differs")
	}
	resp, err = httpClient.Post("https://api.telegram.org/bot900001:synthetic-validator/sendMessage", "application/json", strings.NewReader(`{"chat_id":71001,"text":"invented reply"}`))
	if err != nil {
		return err
	}
	resp.Body.Close()
	f.mu.Lock()
	received := len(f.messages) == 1 && f.messages[0].Text == "invented reply"
	f.mu.Unlock()
	if !received {
		return errors.New("Telegram reply not captured")
	}
	if _, err = httpClient.Get("https://example.invalid/forbidden"); err == nil {
		return errors.New("proxy allowed an external destination")
	}
	untrusted := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer untrusted.CloseIdleConnections()
	untrustedClient := &http.Client{Transport: untrusted, Timeout: 5 * time.Second}
	if _, err = untrustedClient.Get("https://api.telegram.org/bot900001:synthetic-validator/getMe"); err == nil {
		return errors.New("TLS succeeded without synthetic CA trust")
	}
	return nil
}
