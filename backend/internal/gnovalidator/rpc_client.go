package gnovalidator

import (
	"context"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
	"gorm.io/gorm"
)

// FallbackRPCClient is the per-chain pooled RPC client. It is an alias rather
// than a wrapper so existing signatures in health.go and api.go keep working
// unchanged while the implementation lives in internal/rpcpool, where govdao
// can reach it too.
type FallbackRPCClient = rpcpool.Client

// SetChainRPCClient publishes the pool serving chainID.
func SetChainRPCClient(chainID string, client *FallbackRPCClient) {
	rpcpool.Register(chainID, client)
}

// GetChainRPCClient returns the pool serving chainID.
func GetChainRPCClient(chainID string) (*FallbackRPCClient, bool) {
	return rpcpool.Get(chainID)
}

// StartRPCPool builds the RPC pool for chainID, registers it in the shared
// rpcpool registry, and starts its background health checks bound to ctx.
//
// It must run before any goroutine that looks the pool up (StartValidatorMonitoring
// via GetChainRPCClient, or govdao via rpcpool.Get) is launched — both the
// main-process startup path (main.startChainMonitoring) and the admin-API
// chain-restart path (api.adminStartChain) call this first, in that order,
// so a chain restarted at runtime gets the exact same setup as one started
// at boot.
func StartRPCPool(ctx context.Context, db *gorm.DB, chainID string, chainCfg *internal.ChainConfig) {
	pool := rpcpool.New(chainCfg.RPCEndpoints, rpcpool.WithObserver(NewRPCObserver(ctx, db, chainID)))
	rpcpool.Register(chainID, pool)
	pool.StartHealthChecks(ctx, GetThresholds().RPCHealthCheckInterval())
}
