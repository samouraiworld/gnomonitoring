package gnovalidator

import (
	"testing"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
)

// statusAt returns a fake endpoint that answers Status at a fixed height.
func statusAt(height int64) *fakeRPCClient {
	return &fakeRPCClient{statusFunc: func() (*ctypes.ResultStatus, error) {
		return &ctypes.ResultStatus{SyncInfo: ctypes.SyncInfo{LatestBlockHeight: height}}, nil
	}}
}

// registerTestPool registers a pool for chainID over the given endpoints.
// Each test uses its own chainID, since the registry is process-global.
func registerTestPool(t *testing.T, chainID string, conns map[string]rpcclient.Client, order ...string) *rpcpool.Client {
	t.Helper()
	pool := rpcpool.New(order, rpcpool.WithDialer(func(endpoint string) (rpcclient.Client, error) {
		c, ok := conns[endpoint]
		require.True(t, ok, "unknown endpoint %s", endpoint)
		return c, nil
	}))
	rpcpool.Register(chainID, pool)
	return pool
}

// The realtime loop read a stall through a frozen active endpoint while the
// other endpoint kept advancing: no stuck alert, and the next poll reads the
// live endpoint.
func TestConfirmStall_FrozenActiveEndpointIsNotAChainHalt(t *testing.T) {
	pool := registerTestPool(t, "test-stall-frozen-rpc", map[string]rpcclient.Client{
		"frozen": statusAt(100),
		"live":   statusAt(140),
	}, "frozen", "live")

	require.Equal(t, "frozen", pool.ActiveEndpoint())
	assert.False(t, confirmStall("test-stall-frozen-rpc", 100))

	status, err := pool.Status()
	require.NoError(t, err)
	assert.Equal(t, int64(140), status.SyncInfo.LatestBlockHeight, "the next poll must read the live endpoint")
}

func TestConfirmStall_RealHaltStillAlerts(t *testing.T) {
	registerTestPool(t, "test-stall-real-halt", map[string]rpcclient.Client{
		"a": statusAt(100),
		"b": statusAt(100),
	}, "a", "b")

	assert.True(t, confirmStall("test-stall-real-halt", 100))
}

func TestConfirmStall_NoPoolKeepsTodayBehavior(t *testing.T) {
	assert.True(t, confirmStall("test-stall-no-pool-registered", 100))
}
