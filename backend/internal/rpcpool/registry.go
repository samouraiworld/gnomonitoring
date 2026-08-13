package rpcpool

import "sync"

var (
	registryMu sync.RWMutex
	registry   = make(map[string]*Client)
)

// Register publishes the pool serving chainID, replacing any previous entry.
// Call it before starting any goroutine that will look the pool up, so no
// consumer can observe a missing entry.
func Register(chainID string, c *Client) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[chainID] = c
}

// Get returns the pool registered for chainID.
func Get(chainID string) (*Client, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	c, ok := registry[chainID]
	return c, ok
}
