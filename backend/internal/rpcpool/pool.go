package rpcpool

import (
	"fmt"
	"log"
	"sync"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
)

// Event describes a pool state transition reported to an Observer.
type Event int

const (
	// EventRotated: a call failed on the active endpoint and succeeded on a
	// different one. The pool is serving requests normally again.
	EventRotated Event = iota
	// EventAllDown: a call was attempted against every endpoint and all of
	// them failed. Emitted once per outage, not once per call.
	EventAllDown
	// EventRecovered: a call succeeded after a previous EventAllDown.
	EventRecovered
)

func (e Event) String() string {
	switch e {
	case EventRotated:
		return "rotated"
	case EventAllDown:
		return "all_down"
	case EventRecovered:
		return "recovered"
	default:
		return "unknown"
	}
}

// Observer is notified of pool state transitions. It runs synchronously on
// the calling goroutine and must not block or call back into the pool.
type Observer func(ev Event, endpoint string, err error)

// Dialer builds an RPC client for endpoint. Overridden in tests.
type Dialer func(endpoint string) (rpcclient.Client, error)

func defaultDialer(endpoint string) (rpcclient.Client, error) {
	return rpcclient.NewHTTPClient(endpoint)
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithDialer overrides how endpoints are turned into RPC clients.
func WithDialer(d Dialer) Option { return func(c *Client) { c.dial = d } }

// WithObserver registers a state-transition callback.
func WithObserver(o Observer) Option { return func(c *Client) { c.obs = o } }

// Client is a priority-ordered pool of RPC endpoints that satisfies
// rpcclient.Client. Index 0 is the primary; later indexes are backups tried
// in order. It is safe for concurrent use.
type Client struct {
	mu        sync.Mutex
	endpoints []string
	conns     []rpcclient.Client // index-aligned with endpoints; nil until dialed
	activeIdx int
	allDown   bool

	dial Dialer
	obs  Observer
}

// New builds a pool over endpoints, in priority order.
func New(endpoints []string, opts ...Option) *Client {
	c := &Client{
		endpoints: append([]string(nil), endpoints...),
		dial:      defaultDialer,
	}
	c.conns = make([]rpcclient.Client, len(c.endpoints))
	for _, o := range opts {
		o(c)
	}
	if len(c.endpoints) == 0 {
		log.Printf("[rpcpool] built with no endpoints; every call will fail")
	}
	return c
}

// ActiveEndpoint returns the endpoint currently serving requests, or "" when
// the pool has no endpoints.
func (c *Client) ActiveEndpoint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.endpoints) == 0 {
		return ""
	}
	return c.endpoints[c.activeIdx]
}

// Endpoints returns a copy of the configured endpoints, in priority order.
func (c *Client) Endpoints() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.endpoints...)
}

// connAt returns the client for endpoint i, dialing it on first use.
// Must be called with c.mu held.
func (c *Client) connAt(i int) (rpcclient.Client, error) {
	if c.conns[i] != nil {
		return c.conns[i], nil
	}
	conn, err := c.dial(c.endpoints[i])
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.endpoints[i], err)
	}
	c.conns[i] = conn
	return conn, nil
}

// call runs fn against the active endpoint. On an endpoint-level failure it
// walks the remaining endpoints in priority order, trying each at most once,
// and sticks to the first that answers. A chain-level error (see
// isEndpointError) is returned immediately: every endpoint would answer the
// same, so rotating would only strand the pool on a worse node.
func (c *Client) call(fn func(rpcclient.Client) error) error {
	c.mu.Lock()
	n := len(c.endpoints)
	start := c.activeIdx
	c.mu.Unlock()

	if n == 0 {
		return ErrNoEndpoints
	}

	var firstErr error
	record := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for attempt := 0; attempt < n; attempt++ {
		idx := (start + attempt) % n

		c.mu.Lock()
		conn, dialErr := c.connAt(idx)
		endpoint := c.endpoints[idx]
		c.mu.Unlock()

		if dialErr != nil {
			record(dialErr)
			log.Printf("[rpcpool] endpoint %s: %v", endpoint, dialErr)
			continue
		}

		err := fn(conn)
		if err == nil {
			c.onSuccess(idx, attempt > 0)
			return nil
		}
		record(err)

		if !isEndpointError(err) {
			return err
		}

		// Drop the connection so the next attempt on this endpoint dials a
		// fresh one instead of reusing a poisoned keep-alive pool.
		c.mu.Lock()
		c.conns[idx] = nil
		c.mu.Unlock()

		log.Printf("[rpcpool] endpoint %s failed (%v), trying the next one", endpoint, err)
	}

	c.onAllDown(firstErr)
	return fmt.Errorf("all %d RPC endpoints failed, first error: %w", n, firstErr)
}

// onSuccess promotes idx to active and emits the matching transition event.
func (c *Client) onSuccess(idx int, switched bool) {
	c.mu.Lock()
	prev := c.activeIdx
	wasAllDown := c.allDown
	c.activeIdx = idx
	c.allDown = false
	endpoint := c.endpoints[idx]
	obs := c.obs
	c.mu.Unlock()

	if wasAllDown {
		log.Printf("[rpcpool] recovered on endpoint #%d %s", idx, endpoint)
		if obs != nil {
			obs(EventRecovered, endpoint, nil)
		}
		return
	}
	if switched && prev != idx {
		log.Printf("[rpcpool] active endpoint is now #%d %s (was #%d)", idx, endpoint, prev)
		if obs != nil {
			obs(EventRotated, endpoint, nil)
		}
	}
}

// onAllDown records a full outage, emitting EventAllDown only on the
// transition into it so a sustained outage does not spam the observer.
func (c *Client) onAllDown(err error) {
	c.mu.Lock()
	already := c.allDown
	c.allDown = true
	obs := c.obs
	active := ""
	if len(c.endpoints) > 0 {
		active = c.endpoints[c.activeIdx]
	}
	c.mu.Unlock()

	if already {
		return
	}
	log.Printf("[rpcpool] every endpoint is down, last active was %s: %v", active, err)
	if obs != nil {
		obs(EventAllDown, active, err)
	}
}

// ABCIInfo implements rpcclient.Client.
func (c *Client) ABCIInfo() (*ctypes.ResultABCIInfo, error) {
	var result *ctypes.ResultABCIInfo
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.ABCIInfo()
		return e
	})
	return result, err
}

// Status implements rpcclient.Client.
func (c *Client) Status() (*ctypes.ResultStatus, error) {
	var result *ctypes.ResultStatus
	err := c.call(func(conn rpcclient.Client) error {
		var e error
		result, e = conn.Status()
		return e
	})
	return result, err
}
