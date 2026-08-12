package rpcpool

import (
	"context"
	"log"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
)

// defaultProbeTimeout bounds a single health probe. gno's HTTP RPC client
// sets no request timeout of its own, so without this an endpoint that
// accepts the TCP connection and then goes silent would hang the health
// goroutine forever — the same failure mode this whole probe exists to catch.
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
// observedAt is captured right after probeWithTimeout returns for each
// endpoint (or right after a dial failure), never once up front: onSuccess
// and onAllDown order transitions by when the outcome was actually observed,
// not by when checkOnce started, so a single timestamp taken at the top of
// this function would misrepresent every endpoint after the first (see
// call's doc comment in pool.go for the full rationale).
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
			// Discard the connection so the next dial rebuilds it rather than
			// reusing a keep-alive socket to a node that stopped answering.
			c.mu.Lock()
			c.conns[i] = nil
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

	c.onAllDown(observedAt, ErrAllEndpointsDown)
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
