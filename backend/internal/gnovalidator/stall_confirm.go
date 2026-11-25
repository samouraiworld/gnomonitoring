package gnovalidator

import "github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"

// confirmStall reports whether a stall at height, seen through the active RPC
// endpoint, holds on the chain's other endpoints. A single endpoint can be
// frozen at an old height while still answering; alerting on it would tell
// every subscriber the chain is stuck while it runs. The pool returns false
// when another endpoint is past height, and also moves itself onto that
// endpoint so the next poll reads the live chain. With no registered pool or
// a single endpoint the stall stands, which is the behavior before this
// check existed.
func confirmStall(chainID string, height int64) bool {
	pool, ok := rpcpool.Get(chainID)
	if !ok {
		return true
	}
	return pool.ConfirmStall(height)
}
