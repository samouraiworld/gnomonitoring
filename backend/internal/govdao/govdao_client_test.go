package govdao

import (
	"testing"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time contract: both accept a pooled client, not an endpoint string.
var (
	_ func(int, *gnoclient.Client) (string, error) = ExtractTitle
	_ func(int, *gnoclient.Client) (string, error) = ExtractProposalRender
)

// clientForChain must resolve the pool registered by main.startChainMonitoring
// rather than dialling RPCEndpoints[0] directly, so GovDAO proposal lookups
// inherit the same failover as validator monitoring.
func TestClientForChain_UsesTheRegisteredPool(t *testing.T) {
	pool := rpcpool.New([]string{"http://endpoint-a", "http://endpoint-b"})
	rpcpool.Register("test-govdao-chain", pool)

	client, err := clientForChain("test-govdao-chain")
	require.NoError(t, err)
	assert.Same(t, pool, client.RPCClient)
}

func TestClientForChain_UnknownChain(t *testing.T) {
	_, err := clientForChain("no-such-chain")
	assert.Error(t, err)
}
