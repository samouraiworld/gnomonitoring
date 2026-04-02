package chainmanager

import (
	"context"
	"log"
	"sort"
	"sync"
)

var (
	registry = make(map[string]context.CancelFunc)
	mu       sync.Mutex
)

// Register stores the cancel function for a running chain goroutine.
// If a cancel function for that chain already exists, it is called first (stops the previous instance).
func Register(chainID string, cancel context.CancelFunc) {
	mu.Lock()
	defer mu.Unlock()
	if existing, ok := registry[chainID]; ok {
		log.Printf("[chainmanager] replacing existing goroutine for chain %s", chainID)
		existing()
	}
	registry[chainID] = cancel
}

// Cancel stops the goroutines for the given chain and removes it from the registry.
// Returns true if the chain was found and cancelled, false if it was not registered.
func Cancel(chainID string) bool {
	mu.Lock()
	defer mu.Unlock()
	cancel, ok := registry[chainID]
	if !ok {
		return false
	}
	log.Printf("[chainmanager] cancelling chain %s", chainID)
	cancel()
	delete(registry, chainID)
	return true
}

// ActiveChains returns a sorted list of chain IDs currently registered.
func ActiveChains() []string {
	mu.Lock()
	defer mu.Unlock()
	chains := make([]string, 0, len(registry))
	for id := range registry {
		chains = append(chains, id)
	}
	sort.Strings(chains)
	return chains
}

// IsActive returns true if the chain has a registered goroutine.
func IsActive(chainID string) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := registry[chainID]
	return ok
}
