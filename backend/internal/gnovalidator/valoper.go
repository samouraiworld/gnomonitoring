package gnovalidator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
	// ServerType is the profile's declared infrastructure ("cloud",
	// "on-prem", "data-center"), or "" if not declared. Parsed from the same
	// profile render as SigningAddress.
	ServerType string
}

// valoperListRe matches a `[Name](/r/gnops/valopers:operatorAddr)` link in the
// valopers list render.
var valoperListRe = regexp.MustCompile(`\[\s*([^\]]+?)\s*]\(/r/gnops/valopers:([a-z0-9]+)\)`)

// Profile field lines of an individual valoper profile render. The realm
// renders the free-form description before the fields, in a fixed order
// (Operator Address, Signing Address, Signing PubKey, Server Type), so a
// description may contain a line that imitates a field. Each pattern is
// therefore anchored to a line start, either a real newline or the escaped
// `\n` that the vm/qeval string literal carries, and lastFieldValue keeps the
// last match, which is always the rendered field. The value classes stop at
// whitespace and at the backslash of an escaped `\n`, and accept an empty
// value so an empty real field never falls back to a description line.
var (
	signingAddrRe = regexp.MustCompile(`(?:^|\n|\\n)- Signing Address:[ \t]*([a-z0-9]*)`)
	serverTypeRe  = regexp.MustCompile(`(?:^|\n|\\n)- Server Type:[ \t]*([A-Za-z0-9_-]*)`)
)

// lastFieldValue returns the value captured by the last match of re in
// render, or "" if there is none.
func lastFieldValue(re *regexp.Regexp, render string) string {
	m := re.FindAllStringSubmatch(render, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

const (
	// maxValoperProfileConcurrency is the number of workers fetching
	// individual profile renders in parallel.
	maxValoperProfileConcurrency = 10
	// valoperProfileAttempts bounds the tries per profile on a transport
	// error. The pool already walks every endpoint within one try; the retry
	// only absorbs a transient blip that hit all of them at once.
	valoperProfileAttempts = 3
	// valoperProfileRetryBackoff is the wait before the second try, doubled
	// before each later one.
	valoperProfileRetryBackoff = 250 * time.Millisecond
	// valoperProfileFetchBudget bounds the whole profile enrichment of one
	// refresh cycle, so a slow endpoint cannot stall InitMonikerMap.
	// Profiles not fetched in time keep their last known values.
	valoperProfileFetchBudget = 30 * time.Second
)

// realmError is an error the realm returned in the ABCI response, as opposed
// to a transport failure. Every node answers the same, so it is not retried.
type realmError struct{ msg string }

func (e *realmError) Error() string { return e.msg }

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
		return "", &realmError{msg: resp.Response.Error.Error()}
	}
	return string(resp.Response.Data), nil
}

// parseSigningAddress extracts the Signing Address from an individual valoper
// profile render, or "" if the profile does not declare one.
func parseSigningAddress(markdown string) string {
	addr := lastFieldValue(signingAddrRe, markdown)
	if !strings.HasPrefix(addr, "g1") {
		return ""
	}
	return addr
}

// parseServerType extracts the Server Type from an individual valoper profile
// render, or "" if the profile does not declare one.
func parseServerType(markdown string) string {
	return lastFieldValue(serverTypeRe, markdown)
}

// profileFetcher returns the individual profile render of an operator address.
type profileFetcher func(operatorAddr string) (string, error)

// fetchValoperProfileWithRetry fetches one profile render, retrying a
// transport error with exponential backoff (starting at backoff) up to
// valoperProfileAttempts
// tries. A realm error, or ctx expiring, ends the retries early; no new try
// starts once ctx has expired.
func fetchValoperProfileWithRetry(ctx context.Context, fetch profileFetcher, operatorAddr string, backoff time.Duration) (string, error) {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		render, err := fetch(operatorAddr)
		if err == nil {
			return render, nil
		}
		var re *realmError
		if errors.As(err, &re) || attempt >= valoperProfileAttempts {
			return "", err
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return "", err
		}
		backoff *= 2
	}
}

// profileResult is one worker's outcome for valopers[idx].
type profileResult struct {
	idx    int
	render string
	err    error
}

// enrichValoperProfiles fills SigningAddress and ServerType of each valoper
// from its individual profile render, one fetch per profile.
//
// Each valoper first inherits the values of the same operator address in
// prev (the last known-good snapshot), so a profile whose fetch fails, or is
// still in flight when ctx expires, keeps its last known values instead of
// being reset to "". A successful fetch always wins, even when it declares
// no signing address.
//
// A fixed pool of workers bounds both the goroutines spawned and the RPCs in
// flight. Only this function writes to valopers; workers that are still
// blocked on a slow endpoint when ctx expires finish later into a buffered
// channel nobody reads, without touching valopers.
func enrichValoperProfiles(ctx context.Context, fetch profileFetcher, valopers []Valoper, prev []Valoper, backoff time.Duration) {
	if len(valopers) == 0 {
		return
	}

	prevByOperator := make(map[string]Valoper, len(prev))
	for _, p := range prev {
		prevByOperator[p.Address] = p
	}
	addrs := make([]string, len(valopers))
	hasPrev := make([]bool, len(valopers))
	for i := range valopers {
		addrs[i] = valopers[i].Address
		if p, ok := prevByOperator[addrs[i]]; ok {
			valopers[i].SigningAddress = p.SigningAddress
			valopers[i].ServerType = p.ServerType
			hasPrev[i] = true
		}
	}

	jobs := make(chan int)
	results := make(chan profileResult, len(valopers))
	workers := min(maxValoperProfileConcurrency, len(valopers))
	for w := 0; w < workers; w++ {
		go func() {
			for idx := range jobs {
				render, err := fetchValoperProfileWithRetry(ctx, fetch, addrs[idx], backoff)
				results <- profileResult{idx: idx, render: render, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range addrs {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	apply := func(r profileResult) {
		v := &valopers[r.idx]
		if r.err != nil {
			if hasPrev[r.idx] {
				log.Printf("[valoper] failed to fetch profile for %s (%s), keeping last known values: %v", v.Name, v.Address, r.err)
			} else {
				log.Printf("[valoper] failed to fetch profile for %s (%s), no last known values: %v", v.Name, v.Address, r.err)
			}
			return
		}
		v.SigningAddress = parseSigningAddress(r.render)
		v.ServerType = parseServerType(r.render)
	}

	for received := 0; received < len(valopers); received++ {
		select {
		case r := <-results:
			apply(r)
		case <-ctx.Done():
			// select picks at random among ready cases: apply the results
			// already delivered before giving up on the rest.
		drain:
			for {
				select {
				case r := <-results:
					apply(r)
					received++
				default:
					break drain
				}
			}
			log.Printf("[valoper] profile fetch budget exhausted: %d/%d profiles answered, the rest keep their last known values", received, len(valopers))
			return
		}
	}
}

// GetValopers lists every registered valoper, then fetches each profile for
// its signing address and server type. prev is the last known-good snapshot;
// it supplies the values of any profile that cannot be fetched this cycle.
func GetValopers(client gnoclient.Client, prev []Valoper) ([]Valoper, error) {
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
	// set and block precommits, and its Server Type.
	ctx, cancel := context.WithTimeout(context.Background(), valoperProfileFetchBudget)
	defer cancel()
	fetch := func(operatorAddr string) (string, error) { return qevalRender(client, operatorAddr) }
	enrichValoperProfiles(ctx, fetch, allValopers, prev, valoperProfileRetryBackoff)

	log.Printf("[valoper] fetched %d valopers", len(allValopers))
	return allValopers, nil
}

// valoperIndex keys each valoper by its signing address, then by its operator
// address as a fallback for profiles where the operator account is itself the
// signer or no signing address is declared yet. Signing keys are written
// first and an operator key never replaces an existing entry, so one
// valoper's operator address can never shadow another's signing address.
func valoperIndex(valopers []Valoper) map[string]Valoper {
	idx := make(map[string]Valoper, 2*len(valopers))
	for _, v := range valopers {
		if v.SigningAddress != "" {
			idx[v.SigningAddress] = v
		}
	}
	for _, v := range valopers {
		if _, taken := idx[v.Address]; !taken {
			idx[v.Address] = v
		}
	}
	return idx
}

// GetGenesisMonikers fetches /genesis from the first endpoint that answers
// and extracts the validator name for each genesis validator address. The
// response is streamed rather than buffered: a genesis file can be large.
func GetGenesisMonikers(endpoints []string) (map[string]string, error) {
	resp, used, err := getWithFailover(endpoints, "/genesis")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch genesis: %w", err)
	}
	defer resp.Body.Close()
	log.Printf("[valoper] genesis monikers fetched from %s", used)

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

func InitMonikerMap(db *gorm.DB, chainID string, client gnoclient.Client, chainCfg *internal.ChainConfig) []Valoper {
	type Validator struct {
		Address     string `json:"address"`
		VotingPower string `json:"voting_power"`
	}
	type ValidatorsResponse struct {
		Result struct {
			BlockHeight string      `json:"block_height"`
			Validators  []Validator `json:"validators"`
		} `json:"result"`
	}
	// Step 1 — Retrieve active validators from the first RPC endpoint that answers
	resp, usedEndpoint, err := getWithFailover(chainCfg.RPCEndpoints, "/validators")
	if err != nil {
		log.Printf("[valoper][%s] failed to retrieve validators from any endpoint: %v", chainID, err)
		return nil
	}
	defer resp.Body.Close()
	log.Printf("[valoper][%s] validators fetched from %s", chainID, usedEndpoint)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("❌ Error reading validator response: %v", err)
		return nil
	}

	if !json.Valid(body) {
		log.Printf("❌ Invalid JSON received from /validators:\n%s", string(body))
		return nil
	}
	var validatorsResp ValidatorsResponse
	if err := json.Unmarshal(body, &validatorsResp); err != nil {
		log.Printf("❌ Error decoding validator JSON: %v\nRaw body: %s", err, string(body))
		return nil
	}

	// A node still syncing, or a proxy serving a degraded response, can answer
	// 200 with an empty validator set. Replacing MonikerMap with that would
	// make classifyValsetChanges report every validator as having left the
	// valset — one alert each — and stop SaveParticipation from writing any
	// row at all. Keep the previous map and retry on the next cycle.
	if len(validatorsResp.Result.Validators) == 0 {
		log.Printf("[valoper][%s] /validators returned an empty validator set from %s; keeping the previous MonikerMap", chainID, usedEndpoint)
		return nil
	}

	// Step 2 — Use passed client for valopers.Render
	valopers, err := GetValopers(client, getCachedValopers(chainID))
	if err != nil {
		log.Printf("⚠️ Failed to get valopers: %v", err)
	}

	// Key the valoper map by the consensus Signing Address, since that is what
	// appears in the validator set and block precommits, with the operator
	// address as a non-shadowing fallback (see valoperIndex).
	valoperMap := make(map[string]string)
	for addr, v := range valoperIndex(valopers) {
		valoperMap[addr] = v.Name
	}

	// Step 3 — Genesis monikers
	genesisMap, err := GetGenesisMonikers(chainCfg.RPCEndpoints)
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

	ReplaceMonikerMap(chainID, tempMonikers)

	// joinHeight is the block height this /validators snapshot was taken at,
	// used as the best available proxy for "the block a brand-new address
	// joined the valset" (UpsertAddrMonikerVPBatch only applies it on the
	// very first INSERT for an addr; existing rows keep whatever height they
	// were first recorded at). -1 ("unknown") if the RPC omitted or returned
	// an unparseable block_height, matching addr_monikers' sentinel default.
	joinHeight, err := strconv.ParseInt(validatorsResp.Result.BlockHeight, 10, 64)
	if err != nil {
		joinHeight = -1
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
	// Refresh the in-memory quorum denominator used for late_for_quorum.
	// Skipped when no VP could be parsed, keeping the previous snapshot rather
	// than blanking it (which would null every late_for_quorum until the next
	// successful refresh).
	if len(vpRows) > 0 {
		vpByAddr := make(map[string]int64, len(vpRows))
		for _, r := range vpRows {
			vpByAddr[r.Addr] = r.VotingPower
		}
		setValsetVotingPower(chainID, vpByAddr)
	}
	if err := database.UpsertAddrMonikerVPBatch(db, chainID, vpRows, joinHeight); err != nil {
		log.Printf("[valoper][%s] failed to persist voting power batch: %v", chainID, err)
	}

	// Zero out voting_power for any addr no longer in the live valset, so
	// voting_power > 0 stays a reliable "currently bonded" signal (otherwise a
	// departed validator's last VP would stay frozen at a stale nonzero value
	// forever, since the upsert above only ever touches currently-bonded addrs).
	currentAddrs := make([]string, 0, len(tempMonikers))
	for addr := range tempMonikers {
		currentAddrs = append(currentAddrs, addr)
	}
	if err := database.ZeroDepartedVotingPower(context.Background(), db, chainID, currentAddrs); err != nil {
		log.Printf("[valoper][%s] failed to zero departed voting power: %v", chainID, err)
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

	// Sync MonikerMap to addr_monikers table. Never persist the "unknown"
	// placeholder: a stored "unknown" pollutes every COALESCE(am.moniker, …)
	// read (it wins over the frozen row moniker) and absence is the clean
	// "re-resolve next run" signal resolveMoniker already understands.
	resolved, unresolved := 0, 0
	for addr, moniker := range tempMonikers {
		if !isResolvedMoniker(moniker) {
			unresolved++
			continue
		}
		resolved++
		if err := database.UpsertAddrMoniker(db, chainID, addr, moniker); err != nil {
			log.Printf("[valoper][%s] failed to upsert moniker for %s: %v", chainID, addr, err)
		}
	}
	log.Printf("[valoper][%s] moniker sync: %d resolved persisted, %d unresolved (not persisted)",
		chainID, resolved, unresolved)

	return valopers
}
