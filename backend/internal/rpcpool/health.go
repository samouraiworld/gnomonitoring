package rpcpool

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
)

// errEmptyStatus is recorded when an endpoint answers Status with neither a
// result nor an error, which leaves its height unknown.
var errEmptyStatus = errors.New("rpcpool: health probe got an empty status")

// errWrongNetwork and errFrozen mark, inside pick, an answering endpoint that
// must not be chosen. They never leave this package.
var (
	errWrongNetwork = errors.New("rpcpool: endpoint is on another network")
	errFrozen       = errors.New("rpcpool: endpoint is still at its frozen height")
)

// defaultProbeTimeout bounds a single health probe. gno's HTTP RPC client
// does set its own request timeout (60s, see
// tm2/pkg/bft/rpc/client/client.go), but that budget is sized for the data
// path, which is expected to tolerate a genuinely slow-but-alive node. The
// probe's only job is to find the highest-priority endpoint that answers
// quickly enough to be worth promoting, so it deliberately bounds itself far
// tighter than that 60s: a hung or merely slow endpoint must not be allowed
// to stall the promotion loop. A probe timeout here is not proof the
// endpoint is down — see checkOnce, which does not treat it as one.
const defaultProbeTimeout = 5 * time.Second

// StartHealthChecks probes the endpoints every interval until ctx is done. It
// promotes the highest-priority endpoint that answers, which both returns the
// pool to its primary once that primary recovers and moves it off an active
// endpoint that stopped answering between two calls. A non-positive interval
// disables the probe.
func (c *Client) StartHealthChecks(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("[rpcpool] health checks disabled (interval=%v)", interval)
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Printf("[rpcpool] health checks stopped")
				return
			case <-ticker.C:
				c.checkOnce()
			}
		}
	}()
}

// probeResult is what one health probe learned about one endpoint.
type probeResult struct {
	err        error
	height     int64
	catchingUp bool
	// network is the chain ID the node reports (NodeInfo.Network).
	network string
	// observedAt is when this endpoint's probe returned. Promotions are
	// ordered by it (see call's doc comment in pool.go), so it is taken per
	// endpoint, not once after every probe has returned: a fast answer must
	// not be passed off as newer than a call() outcome observed while a
	// slower probe was still pending.
	observedAt time.Time
}

// checkOnce probes every endpoint and promotes the highest-priority healthy
// one. Returns the promoted index, or -1 when none answered or the promotion
// was stale (a newer switch was applied while the probes ran).
//
// Only endpoints on the chain's pinned network (see referenceNetwork) are
// considered: a backup left on another network, or on an old
// chain ID after a reset, may report a higher height that has nothing to do
// with this chain. Among them, an endpoint is healthy when it answers Status
// within defaultProbeTimeout, is not catching up, trails the most advanced
// one by no more than maxLagBlocks, and is not held back as frozen by an
// earlier ConfirmStall (see markFrozen). Answering is not enough on its own: a node frozen
// at an old height keeps answering Status, and if it stayed active the
// realtime loop would see the chain stall and send a false "Blockchain
// stuck" while the chain runs. When no endpoint is healthy but some answered
// (every answering node is catching up, say), the most advanced answering
// one is promoted instead, which is still the best the pool can serve.
//
// checkOnce is promotion-only: it may only ever call onSuccess, never
// onAllDown. A probe failure is bounded by defaultProbeTimeout (5s), far
// tighter than the 60s the data path tolerates per call (see that const's
// doc comment), so "every probe failed" is not proof of a total outage —
// only that no endpoint answered within the probe's much narrower budget. A
// single transient >5s response on an otherwise healthy single-endpoint
// deployment used to be enough to make checkOnce declare EventAllDown on its
// own authority, firing a false CRITICAL "block collection and validator
// alerts are stopped" for a chain that never actually stopped collecting.
// Detecting a genuine total outage remains call()'s job in pool.go: it
// already walks every endpoint on the real data path, under the data path's
// own more tolerant timeout, and calls onAllDown when they all genuinely
// fail — so removing the call here loses no detection capability.
//
// The endpoints are probed in parallel so their heights are comparable. The
// promotion carries the observedAt of the chosen endpoint's own probe.
func (c *Client) checkOnce() int {
	results := c.probeAll()
	if len(results) == 0 {
		return -1
	}

	network, known := c.referenceNetwork(results)
	pick := c.pick(results, -1, network, known)
	if pick < 0 {
		// Every probe failed within defaultProbeTimeout. This is
		// deliberately not reported as EventAllDown — see checkOnce's doc
		// comment — but it is still logged so the condition remains visible
		// to an operator; the data path's own next call() is what decides
		// whether this is a real outage.
		log.Printf("[rpcpool] health probe: every endpoint failed to answer within %s", defaultProbeTimeout)
		return -1
	}
	if !c.onSuccess(results[pick].observedAt, pick, true) {
		return -1
	}
	return pick
}

// ConfirmStall reports whether the chain really is stalled at height, as seen
// from the pool's other endpoints. It returns false when any endpoint that
// answers reports a block past height: the chain is moving and only the
// endpoint that served height is behind. In that case it also moves the pool
// to the highest-priority healthy endpoint past height, so the next call
// reads the live chain.
//
// It returns true, keeping the caller's stall verdict, when no endpoint on
// the chain's network is past height, when no other endpoint answers (the
// stall cannot be checked elsewhere), and, without probing, when the pool has
// a single endpoint.
//
// When the stall is disproved, the active endpoint and the endpoints still
// exactly at height are marked frozen, so the next health probe does not move the pool back to one
// of them merely because it is within maxLagBlocks of the best endpoint.
func (c *Client) ConfirmStall(height int64) bool {
	c.mu.Lock()
	n := len(c.endpoints)
	c.mu.Unlock()
	if n <= 1 {
		return true
	}

	results := c.probeAll()

	network, known := c.referenceNetwork(results)
	if !known {
		// The active endpoint has not answered a probe yet and the
		// endpoints do not all agree: no height can be trusted to be on
		// this chain.
		return true
	}
	pick := c.pick(results, height, network, true)
	if pick < 0 {
		return true
	}
	c.markFrozen(results, height, network)
	c.mu.Lock()
	endpoint := c.endpoints[pick]
	c.mu.Unlock()
	log.Printf("[rpcpool] stall at height %d disproved: %s is at height %d", height, endpoint, results[pick].height)
	if !c.onSuccess(results[pick].observedAt, pick, true) {
		// A newer outcome was applied while the probes ran. The chain is
		// still moving; the frozen marks let the next probe move the pool.
		log.Printf("[rpcpool] not switching to %s now: a newer outcome was applied meanwhile", endpoint)
	}
	return false
}

// referenceNetwork returns the network this pool's chain is on, and whether
// it is known. The network is pinned the first time the active endpoint
// answers a probe (at startup that is the primary, as configured by the
// operator), and pinned again only when every endpoint answers with the
// same network, as after a chain-ID upgrade. A vote among whichever
// endpoints happened to answer is deliberately not used: two backups left on
// an old chain ID would outvote a healthy primary, and a halted active
// endpoint that hangs its RPC would hand the vote to a backup on another
// network.
func (c *Client) referenceNetwork(results []probeResult) (string, bool) {
	answered, first, same := 0, "", true
	for _, r := range results {
		if r.err != nil {
			continue
		}
		if answered == 0 {
			first = r.network
		} else if r.network != first {
			same = false
		}
		answered++
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if answered > 0 && answered == len(results) && same {
		c.network, c.networkKnown = first, true
	}
	if !c.networkKnown && c.activeIdx < len(results) && results[c.activeIdx].err == nil {
		c.network, c.networkKnown = results[c.activeIdx].network, true
	}
	return c.network, c.networkKnown
}

// markFrozen records as frozen the active endpoint, which served the stalled
// height, and every answering endpoint on network still exactly at height.
// An endpoint merely lagging below height is still advancing and is left to
// the lag check.
func (c *Client) markFrozen(results []probeResult, height int64, network string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, r := range results {
		if r.err != nil || r.network != network {
			continue
		}
		if r.height == height || (i == c.activeIdx && r.height <= height) {
			c.frozenAt[i] = r.height
		}
	}
}

// pick chooses the endpoint to serve from among those on network that
// answered and report a height above floor (see pickEndpoint). When network
// is not known, every answering endpoint is considered, which is what the
// probe did before it compared heights. Endpoints marked frozen are left out
// while they stay at their frozen height, and their mark is cleared once
// they move. A frozen endpoint is never chosen, even when nothing else
// qualifies: the live endpoint missing one probe does not make the frozen
// one worth serving from again.
func (c *Client) pick(results []probeResult, floor int64, network string, known bool) int {
	candidates := make([]probeResult, len(results))
	copy(candidates, results)

	c.mu.Lock()
	for i, r := range results {
		if r.err != nil {
			continue
		}
		if known && r.network != network {
			log.Printf("[rpcpool] health probe: %s reports network %q, expected %q; ignored",
				c.endpoints[i], r.network, network)
			candidates[i].err = errWrongNetwork
			continue
		}
		if h, ok := c.frozenAt[i]; ok {
			// Any move clears the mark: past the frozen height the node is
			// advancing again, and below it the node was reset or resynced
			// from an older snapshot, which the lag check judges on its own.
			if r.height != h {
				delete(c.frozenAt, i)
			} else {
				candidates[i].err = errFrozen
			}
		}
	}
	c.mu.Unlock()

	return pickEndpoint(candidates, c.maxLagBlocks(), floor)
}

// pickEndpoint chooses which endpoint to serve from, among those that
// answered and report a height above floor. It returns the highest-priority
// healthy one (not catching up, within maxLag of the most advanced answering
// endpoint), or else the most advanced one, or -1 when none qualifies.
func pickEndpoint(results []probeResult, maxLag, floor int64) int {
	best := int64(-1)
	bestIdx := -1
	for i, r := range results {
		if r.err == nil && r.height > best {
			best, bestIdx = r.height, i
		}
	}
	if bestIdx < 0 || best <= floor {
		return -1
	}
	for i, r := range results {
		if r.err != nil || r.height <= floor || r.catchingUp {
			continue
		}
		if best-r.height <= maxLag {
			return i
		}
	}
	return bestIdx
}

// probeAll probes every endpoint in parallel and returns the results,
// index-aligned with c.endpoints.
//
// A failed probe discards that endpoint's connection so the next dial
// rebuilds it rather than reusing a keep-alive socket to a node that stopped
// answering — but never for the endpoint currently serving the data path. A
// probe failure only proves the endpoint missed this probe's tight
// defaultProbeTimeout budget, not that it is actually down (see checkOnce's
// doc comment); nil'ing the active endpoint's connection on that basis would
// throw away a warm keep-alive pool the data path may still be using
// successfully under its own, far more tolerant timeout.
func (c *Client) probeAll() []probeResult {
	c.mu.Lock()
	n := len(c.endpoints)
	endpoints := append([]string(nil), c.endpoints...)
	conns := make([]rpcclient.Client, n)
	results := make([]probeResult, n)
	for i := 0; i < n; i++ {
		conns[i], results[i].err = c.connAt(i)
	}
	c.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		if results[i].err != nil {
			log.Printf("[rpcpool] health probe: %v", results[i].err)
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, err := probeWithTimeout(conns[i], defaultProbeTimeout)
			results[i].observedAt = time.Now()
			if err != nil {
				results[i].err = err
				return
			}
			results[i].height = status.SyncInfo.LatestBlockHeight
			results[i].catchingUp = status.SyncInfo.CatchingUp
			results[i].network = status.NodeInfo.Network
		}(i)
	}
	wg.Wait()

	c.mu.Lock()
	for i, r := range results {
		if r.err == nil {
			continue
		}
		log.Printf("[rpcpool] health probe failed for %s: %v", endpoints[i], r.err)
		// Only drop the connection this probe used: a concurrent call() may
		// already have replaced it.
		if i != c.activeIdx && c.conns[i] == conns[i] {
			c.conns[i] = nil
		}
	}
	c.mu.Unlock()
	return results
}

// probeWithTimeout calls Status on conn, giving up after timeout. The probe
// goroutine is left to finish on its own once the underlying HTTP call
// returns; the buffered channel keeps it from blocking forever.
func probeWithTimeout(conn rpcclient.Client, timeout time.Duration) (*ctypes.ResultStatus, error) {
	type answer struct {
		status *ctypes.ResultStatus
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		status, err := conn.Status()
		done <- answer{status, err}
	}()
	select {
	case a := <-done:
		if a.err == nil && a.status == nil {
			return nil, errEmptyStatus
		}
		return a.status, a.err
	case <-time.After(timeout):
		return nil, ErrProbeTimeout
	}
}
