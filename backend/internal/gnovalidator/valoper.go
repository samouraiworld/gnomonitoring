package gnovalidator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

type Valoper struct {
	Name string
	// Address is the operator address that registered the valoper profile
	// (the `g1...` linked in the valopers list render).
	Address string
	// SigningAddress is the on-chain consensus address that signs blocks and
	// appears in the `/validators` set and in block precommits. It is exposed
	// only by the individual profile render, not the list render, and may
	// differ from Address (validators increasingly use dedicated signing keys
	// separate from their operator account). Empty if the profile does not
	// declare one.
	SigningAddress string
}

// valoperListRe matches a `[Name](/r/gnops/valopers:operatorAddr)` link in the
// valopers list render.
var valoperListRe = regexp.MustCompile(`\[\s*([^\]]+?)\s*]\(/r/gnops/valopers:([a-z0-9]+)\)`)

// signingAddrRe matches the `Signing Address: g1...` line in an individual
// valoper profile render.
var signingAddrRe = regexp.MustCompile(`Signing Address:\s*(g1[a-z0-9]+)`)

// maxValoperProfileConcurrency bounds how many individual profile renders are
// fetched in parallel when enriching valopers with their signing address.
const maxValoperProfileConcurrency = 10

// qevalRender evaluates `gno.land/r/gnops/valopers.Render(arg)` over the RPC
// client and returns the raw rendered output. arg is quoted as a Go string
// literal, matching what the realm's Render entrypoint expects.
func qevalRender(client gnoclient.Client, arg string) (string, error) {
	cmd := fmt.Sprintf("gno.land/r/gnops/valopers.Render(%q)", arg)
	resp, err := client.RPCClient.ABCIQuery("vm/qeval", []byte(cmd))
	if err != nil {
		return "", err
	}
	if resp.Response.Error != nil && resp.Response.Error.Error() != "" {
		return "", errors.New(resp.Response.Error.Error())
	}
	return string(resp.Response.Data), nil
}

// parseSigningAddress extracts the Signing Address from an individual valoper
// profile render, or "" if the profile does not declare one.
func parseSigningAddress(markdown string) string {
	m := signingAddrRe.FindStringSubmatch(markdown)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// fetchValoperSigningAddress fetches the individual profile render for
// operatorAddr and returns its declared Signing Address (possibly "").
func fetchValoperSigningAddress(client gnoclient.Client, operatorAddr string) (string, error) {
	data, err := qevalRender(client, operatorAddr)
	if err != nil {
		return "", err
	}
	return parseSigningAddress(data), nil
}

// enrichValoperSigningAddresses populates the SigningAddress field of each
// valoper by fetching its individual profile render. Fetches run with bounded
// concurrency; per-valoper failures are logged and leave SigningAddress empty
// so a single unreachable profile never fails the whole refresh.
func enrichValoperSigningAddresses(client gnoclient.Client, valopers []Valoper) {
	sem := make(chan struct{}, maxValoperProfileConcurrency)
	var wg sync.WaitGroup
	for i := range valopers {
		wg.Add(1)
		go func(v *Valoper) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			signing, err := fetchValoperSigningAddress(client, v.Address)
			if err != nil {
				log.Printf("[valoper] failed to fetch signing address for %s (%s): %v", v.Name, v.Address, err)
				return
			}
			v.SigningAddress = signing
		}(&valopers[i])
	}
	wg.Wait()
}

func GetValopers(client gnoclient.Client) ([]Valoper, error) {
	var allValopers []Valoper
	page := 1

	for {
		data, err := qevalRender(client, fmt.Sprintf("?page=%d", page))
		if err != nil {
			return nil, err
		}

		matches := valoperListRe.FindAllStringSubmatch(data, -1)

		// If no result, we stop the loop
		if len(matches) == 0 {
			break
		}

		// Adding the valopers to this page
		for _, m := range matches {
			allValopers = append(allValopers, Valoper{
				Name:    m[1],
				Address: m[2],
			})
		}

		page++
	}

	// The list render only exposes operator addresses; fetch each profile to
	// learn its consensus Signing Address, which is what matches the validator
	// set and block precommits.
	enrichValoperSigningAddresses(client, allValopers)

	log.Printf("[valoper] fetched %d valopers", len(allValopers))
	return allValopers, nil
}
func GetGenesisMonikers(rpcURL string) (map[string]string, error) {
	url := fmt.Sprintf("%s/genesis", strings.TrimRight(rpcURL, "/"))

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch genesis: %w", err)
	}
	defer resp.Body.Close()

	type Validator struct {
		Address string `json:"address"`
		Name    string `json:"name"`
	}

	monikers := make(map[string]string)

	decoder := json.NewDecoder(resp.Body)

	// Go to the "validators" key
	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("error scanning JSON tokens: %w", err)
		}
		if key, ok := tok.(string); ok && key == "validators" {
			break
		}
	}

	// Check that the next token is indeed an array [
	tok, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("error reading validators array start: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return nil, errors.New("expected start of array for validators")
	}

	for decoder.More() {
		var current Validator
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("error decoding validator: %w", err)
		}
		monikers[current.Address] = current.Name
	}

	return monikers, nil
}

// fetchMonikerFromStatus calls /status on rpcURL and returns the node's
// consensus validator address and moniker. Returns an error if the RPC is
// unreachable or the response is incomplete.
func fetchMonikerFromStatus(rpcURL string) (addr, moniker string, err error) {
	c, err := rpcclient.NewHTTPClient(rpcURL)
	if err != nil {
		return "", "", fmt.Errorf("NewHTTPClient: %w", err)
	}
	result, err := c.Status()
	if err != nil || result == nil {
		return "", "", fmt.Errorf("Status(): %w", err)
	}
	addr = result.ValidatorInfo.Address.String()
	moniker = result.NodeInfo.Moniker
	if addr == "" || moniker == "" {
		return "", "", fmt.Errorf("incomplete status response: addr=%q moniker=%q", addr, moniker)
	}
	return addr, moniker, nil
}

// extractPort returns the port from rawURL, or "26657" if none is present
// (e.g. the configured RPC is behind a reverse proxy on 443/80).
func extractPort(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Port() == "" {
		return "26657"
	}
	return u.Port()
}

// peerDialTimeout bounds the TCP reachability pre-check in
// discoverMonikersFromPeers, since gno's RPC client does not enforce its own
// request timeout during the TCP-connect phase (see isPeerReachable).
const peerDialTimeout = 2 * time.Second

// isPeerReachable reports whether a TCP connection to host:port can be
// established within timeout. gno's RPC client does not bound the
// TCP-connect phase with its request timeout, so a peer whose RPC port is
// firewalled (SYN dropped) can otherwise block for the OS TCP connect
// timeout (~127s on Linux defaults) before fetchMonikerFromStatus fails.
func isPeerReachable(host, port string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isResolvedMoniker reports whether m is a real moniker, as opposed to the
// "unknown" placeholder persisted by InitMonikerMap for validators that no
// source could resolve yet.
func isResolvedMoniker(m string) bool {
	return m != "" && m != "unknown"
}

// discoverMonikersFromPeers queries /net_info on each rpcEndpoint, then calls
// /status on every distinct connected peer to learn its validator address and
// moniker. Newly discovered pairs are persisted to the addr_monikers table and
// returned in the result map. Addresses already resolved in existingMonikers
// (i.e. present with a value other than "" or "unknown") are skipped,
// preserving DB admin overrides and other moniker sources; addresses still
// marked "unknown" are retried on every call.
func discoverMonikersFromPeers(db *gorm.DB, chainID string, rpcEndpoints []string, existingMonikers map[string]string) map[string]string {
	discovered := make(map[string]string)
	seenIPs := make(map[string]bool)

	for _, endpoint := range rpcEndpoints {
		port := extractPort(endpoint)

		c, err := rpcclient.NewHTTPClient(endpoint)
		if err != nil {
			log.Printf("[valoper][%s] discovery: NewHTTPClient(%s): %v", chainID, endpoint, err)
			continue
		}

		netInfo, err := c.NetInfo()
		if err != nil || netInfo == nil {
			log.Printf("[valoper][%s] discovery: NetInfo(%s): %v", chainID, endpoint, err)
			continue
		}

		for _, peer := range netInfo.Peers {
			if peer.RemoteIP == "" || seenIPs[peer.RemoteIP] {
				continue
			}
			seenIPs[peer.RemoteIP] = true

			if !isPeerReachable(peer.RemoteIP, port, peerDialTimeout) {
				continue
			}

			peerRPC := fmt.Sprintf("http://%s:%s", peer.RemoteIP, port)
			addr, moniker, err := fetchMonikerFromStatus(peerRPC)
			if err != nil {
				log.Printf("[valoper][%s] discovery: fetchMonikerFromStatus(%s): %v", chainID, peerRPC, err)
				continue
			}

			if isResolvedMoniker(existingMonikers[addr]) || isResolvedMoniker(discovered[addr]) {
				continue
			}

			discovered[addr] = moniker
			if err := database.UpsertAddrMoniker(db, chainID, addr, moniker); err != nil {
				log.Printf("[valoper][%s] discovery: failed to upsert moniker for %s: %v", chainID, addr, err)
			}
		}
	}

	return discovered
}

// resolveMoniker picks the display moniker for addr using the documented
// priority order: DB override > valoper realm > genesis file > peer
// discovery (last resort) > "unknown". A "unknown" placeholder in dbMap
// (persisted by a previous run) is treated as not-yet-resolved, so a
// validator can still pick up a name from valoper, genesis or peer discovery
// once one becomes available.
func resolveMoniker(addr string, dbMap, valoperMap, genesisMap, discoveredMap map[string]string) string {
	if m, ok := dbMap[addr]; ok && isResolvedMoniker(m) {
		return m
	}
	if m, ok := valoperMap[addr]; ok {
		return m
	}
	if m, ok := genesisMap[addr]; ok {
		return m
	}
	if m, ok := discoveredMap[addr]; ok {
		return m
	}
	return "unknown"
}

func InitMonikerMap(db *gorm.DB, chainID string, client gnoclient.Client, chainCfg *internal.ChainConfig) {
	type Validator struct {
		Address     string `json:"address"`
		VotingPower string `json:"voting_power"`
	}
	type ValidatorsResponse struct {
		Result struct {
			Validators []Validator `json:"validators"`
		} `json:"result"`
	}
	// Step 1 — Retrieve active validators from the RPC endpoint `/validators`
	url := fmt.Sprintf("%s/validators", strings.TrimRight(chainCfg.RPCEndpoint(), "/"))
	var resp *http.Response
	err := doWithRetry(3, 2*time.Second, func() error {
		var e error
		resp, e = http.Get(url) // nolint:bodyclose // closed via defer resp.Body.Close() below
		return e
	})
	if err != nil {
		log.Printf("[valoper][%s] failed to retrieve validators after retries: %v", chainID, err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("❌ Error reading validator response: %v", err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("❌ Invalid HTTP status %d from /validators: %s", resp.StatusCode, string(body))
		return
	}

	if !json.Valid(body) {
		log.Printf("❌ Invalid JSON received from /validators:\n%s", string(body))
		return
	}
	var validatorsResp ValidatorsResponse
	if err := json.Unmarshal(body, &validatorsResp); err != nil {
		log.Printf("❌ Error decoding validator JSON: %v\nRaw body: %s", err, string(body))
		return
	}

	// Step 2 — Use passed client for valopers.Render
	valopers, err := GetValopers(client)
	if err != nil {
		log.Printf("⚠️ Failed to get valopers: %v", err)
	}

	// Key the valoper map by the consensus Signing Address, since that is what
	// appears in the validator set and block precommits. Also keep an
	// operator-address entry as a fallback for chains/profiles where the
	// operator account is itself the signer (or no signing address is
	// declared yet).
	valoperMap := make(map[string]string)
	for _, v := range valopers {
		if v.SigningAddress != "" {
			valoperMap[v.SigningAddress] = v.Name
		}
		valoperMap[v.Address] = v.Name
	}

	// Step 3 — Genesis monikers
	genesisMap, err := GetGenesisMonikers(chainCfg.RPCEndpoint())
	if err != nil {
		log.Printf("⚠️ Failed to get genesis monikers: %v", err)
	}
	// Step 4 — Monikers from DB
	dbMap, err := database.GetMoniker(db, chainID)
	if err != nil {
		log.Printf("⚠️ Failed to get monikers from DB: %v", err)
	}
	if dbMap == nil {
		dbMap = make(map[string]string)
	}

	// Step 4.5 — Discover monikers of still-unresolved validators via peer /net_info + /status,
	// persisting newly found pairs to the addr_monikers table. Used as a last resort in Step 5,
	// below valoperMap and genesisMap.
	discoveredMap := discoverMonikersFromPeers(db, chainID, chainCfg.RPCEndpoints, dbMap)

	// Step 5 — Building a Complete and Prioritized MonikerMap
	tempMonikers := make(map[string]string)

	for _, val := range validatorsResp.Result.Validators {
		addr := val.Address
		tempMonikers[addr] = resolveMoniker(addr, dbMap, valoperMap, genesisMap, discoveredMap)
	}

	for addr, moniker := range tempMonikers {
		SetMoniker(chainID, addr, moniker)
	}

	// Persist current voting power for score severity weighting (best-effort),
	// in a single batched upsert rather than one round-trip per validator.
	vpRows := make([]database.AddrVP, 0, len(validatorsResp.Result.Validators))
	for _, val := range validatorsResp.Result.Validators {
		if val.VotingPower == "" {
			continue
		}
		vp, err := strconv.ParseInt(val.VotingPower, 10, 64)
		if err != nil {
			continue
		}
		vpRows = append(vpRows, database.AddrVP{Addr: val.Address, VotingPower: vp})
	}
	if err := database.UpsertAddrMonikerVPBatch(db, chainID, vpRows); err != nil {
		log.Printf("[valoper][%s] failed to persist voting power batch: %v", chainID, err)
	}

	// Load first_active_block from DB into FirstActiveBlockMap
	fabMap, err := database.GetFirstActiveBlocksMap(db, chainID)
	if err != nil {
		log.Printf("[valoper][%s] failed to load first_active_block map: %v", chainID, err)
	} else {
		for addr, fab := range fabMap {
			SetFirstActiveBlock(chainID, addr, fab)
		}
	}

	log.Printf("[valoper][%s] moniker map initialized: %d validators", chainID, len(tempMonikers))

	// Sync MonikerMap to addr_monikers table
	for addr, moniker := range tempMonikers {
		if err := database.UpsertAddrMoniker(db, chainID, addr, moniker); err != nil {
			log.Printf("[valoper][%s] failed to upsert moniker for %s: %v", chainID, addr, err)
		}
	}
}
func doWithRetry(attempts int, sleep time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		log.Printf("[valoper] retry %d/%d: %v", i+1, attempts, err)
		time.Sleep(sleep)
		sleep *= 2 // backoff
	}
	return fmt.Errorf("all retries failed: %w", err)
}
