package rpcpool

import (
	"context"
	"log"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
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

// checkOnce probes endpoints in priority order and promotes the first one
// that answers. Returns the promoted index, or -1 when none answered.
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
// observedAt is captured right after probeWithTimeout returns for each
// endpoint (or right after a dial failure), never once up front: onSuccess
// orders transitions by when the outcome was actually observed, not by when
// checkOnce started, so a single timestamp taken at the top of this function
// would misrepresent every endpoint after the first (see call's doc comment
// in pool.go for the full rationale).
func (c *Client) checkOnce() int {
	c.mu.Lock()
	n := len(c.endpoints)
	c.mu.Unlock()

	if n == 0 {
		return -1
	}

	var observedAt time.Time

	for i := 0; i < n; i++ {
		c.mu.Lock()
		conn, dialErr := c.connAt(i)
		endpoint := c.endpoints[i]
		c.mu.Unlock()

		if dialErr != nil {
			observedAt = time.Now()
			log.Printf("[rpcpool] health probe: %v", dialErr)
			continue
		}

		err := probeWithTimeout(conn, defaultProbeTimeout)
		observedAt = time.Now()
		if err != nil {
			log.Printf("[rpcpool] health probe failed for %s: %v", endpoint, err)
			// Discard the connection so the next dial rebuilds it rather
			// than reusing a keep-alive socket to a node that stopped
			// answering — but never for the endpoint currently serving the
			// data path. A probe failure only proves the endpoint missed
			// this probe's tight defaultProbeTimeout budget, not that it is
			// actually down (see checkOnce's doc comment); nil'ing the
			// active endpoint's connection on that basis would throw away a
			// warm keep-alive pool the data path may still be using
			// successfully under its own, far more tolerant timeout, for no
			// benefit — the active endpoint stays active regardless of what
			// this probe found.
			c.mu.Lock()
			if i != c.activeIdx {
				c.conns[i] = nil
			}
			c.mu.Unlock()
			continue
		}

		// Re-read activeIdx right before the call instead of using a
		// snapshot taken at the top of the loop: with defaultProbeTimeout at
		// several seconds and several endpoints, a concurrent call() can
		// have rotated the real active endpoint while this probe was in
		// flight, and a stale snapshot could silently miss a genuine
		// transition (onSuccess still re-derives prev itself, so state
		// cannot be corrupted either way — this only affects whether the
		// EventRotated log/observer call fires).
		c.mu.Lock()
		currentActive := c.activeIdx
		c.mu.Unlock()

		c.onSuccess(observedAt, i, i != currentActive)
		return i
	}

	// Every probe failed within defaultProbeTimeout. This is deliberately
	// not reported as EventAllDown — see checkOnce's doc comment — but it is
	// still logged so the condition remains visible to an operator; the data
	// path's own next call() is what decides whether this is a real outage.
	log.Printf("[rpcpool] health probe: every endpoint failed to answer within %s", defaultProbeTimeout)
	return -1
}

// probeWithTimeout calls Status on conn, giving up after timeout. The probe
// goroutine is left to finish on its own once the underlying HTTP call
// returns; the buffered channel keeps it from blocking forever.
func probeWithTimeout(conn rpcclient.Client, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		_, err := conn.Status()
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return ErrProbeTimeout
	}
}
