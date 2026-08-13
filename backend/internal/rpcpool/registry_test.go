package rpcpool

import (
	"testing"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time proof the pool can stand in for a real RPC client.
var _ rpcclient.Client = (*Client)(nil)

func TestRegistry_RegisterAndGet(t *testing.T) {
	p := New([]string{"e0"})
	Register("test-chain-registry", p)

	got, ok := Get("test-chain-registry")
	require.True(t, ok)
	assert.Same(t, p, got)

	_, ok = Get("unknown-chain")
	assert.False(t, ok)
}

func TestRegistry_ReRegisterReplaces(t *testing.T) {
	first := New([]string{"e0"})
	second := New([]string{"e1"})
	Register("test-chain-replace", first)
	Register("test-chain-replace", second)

	got, ok := Get("test-chain-replace")
	require.True(t, ok)
	assert.Same(t, second, got)
}
