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
}

// checkOnce probes every endpoint and promotes the highest-priority healthy
// one. Returns the promoted index, or -1 when none answered.
//
// An endpoint is healthy when it answers Status within defaultProbeTimeout,
// is not catching up, and trails the most advanced answering endpoint by no
// more than maxLagBlocks. Answering is not enough on its own: a node frozen
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
// The endpoints are probed in parallel so their heights are comparable, and
// the promotion decision exists only once every probe has returned, so
// observedAt is taken at that point (see call's doc comment in pool.go for
// why outcomes are ordered by when they were observed).
func (c *Client) checkOnce() int {
	results := c.probeAll()
	if len(results) == 0 {
		return -1
	}
	observedAt := time.Now()

	pick := pickEndpoint(results, c.maxLagBlocks(), -1)
	if pick < 0 {
		// Every probe failed within defaultProbeTimeout. This is
		// deliberately not reported as EventAllDown — see checkOnce's doc
		// comment — but it is still logged so the condition remains visible
		// to an operator; the data path's own next call() is what decides
		// whether this is a real outage.
		log.Printf("[rpcpool] health probe: every endpoint failed to answer within %s", defaultProbeTimeout)
		return -1
	}
	c.onSuccess(observedAt, pick, true)
	return pick
}

// ConfirmStall reports whether the chain really is stalled at height, as seen
// from the pool's other endpoints. It returns false when any endpoint that
// answers reports a block past height: the chain is moving and only the
// endpoint that served height is behind. In that case it also moves the pool
// to the highest-priority healthy endpoint past height, so the next call
// reads the live chain.
//
// It returns true, keeping the caller's stall verdict, when no endpoint is
// past height, when no other endpoint answers (the stall cannot be checked
// elsewhere), and, without probing, when the pool has a single endpoint.
func (c *Client) ConfirmStall(height int64) bool {
	c.mu.Lock()
	n := len(c.endpoints)
	c.mu.Unlock()
	if n <= 1 {
		return true
	}

	results := c.probeAll()
	observedAt := time.Now()

	pick := pickEndpoint(results, c.maxLagBlocks(), height)
	if pick < 0 {
		return true
	}
	c.mu.Lock()
	endpoint := c.endpoints[pick]
	c.mu.Unlock()
	log.Printf("[rpcpool] stall at height %d disproved: %s is at height %d", height, endpoint, results[pick].height)
	c.onSuccess(observedAt, pick, true)
	return false
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
			if err != nil {
				results[i].err = err
				return
			}
			results[i].height = status.SyncInfo.LatestBlockHeight
			results[i].catchingUp = status.SyncInfo.CatchingUp
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
