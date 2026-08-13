package rpcpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckOnce_ReturnsToPrimaryOnceItRecovers(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", statusErr: errDown, abciErr: errDown}
	e1 := &fakeConn{endpoint: "e1"}
	c := New([]string{"e0", "e1"}, WithDialer(fakeDialerFrom(map[string]*fakeConn{
		"e0": e0, "e1": e1,
	})))

	// Fail over to the backup.
	_, err := c.ABCIInfo()
	require.NoError(t, err)
	require.Equal(t, "e1", c.ActiveEndpoint())

	// Primary comes back.
	e0.statusErr = nil
	e0.abciErr = nil
	assert.Equal(t, 0, c.checkOnce())
	assert.Equal(t, "e0", c.ActiveEndpoint(), "health check must promote the recovered primary")
}

func TestCheckOnce_KeepsBackupWhilePrimaryIsStillDown(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", statusErr: errDown, abciErr: errDown}
	e1 := &fakeConn{endpoint: "e1"}
	c := New([]string{"e0", "e1"}, WithDialer(fakeDialerFrom(map[string]*fakeConn{
		"e0": e0, "e1": e1,
	})))

	_, err := c.ABCIInfo()
	require.NoError(t, err)
	assert.Equal(t, 1, c.checkOnce())
	assert.Equal(t, "e1", c.ActiveEndpoint())
}

func TestCheckOnce_SwitchesAwayFromSilentlyBrokenActive(t *testing.T) {
	// e0 answers ABCIInfo but fails Status: the per-call path would never
	// notice, the probe must.
	e0 := &fakeConn{endpoint: "e0", statusErr: errDown}
	e1 := &fakeConn{endpoint: "e1"}
	c := New([]string{"e0", "e1"}, WithDialer(fakeDialerFrom(map[string]*fakeConn{
		"e0": e0, "e1": e1,
	})))

	require.Equal(t, "e0", c.ActiveEndpoint())
	assert.Equal(t, 1, c.checkOnce())
	assert.Equal(t, "e1", c.ActiveEndpoint())
}

// TestCheckOnce_PreservesActiveConnectionOnProbeFailure covers the judgment
// call made alongside making checkOnce promotion-only: a probe failure on
// the endpoint currently serving the data path must not discard its
// connection. The probe's defaultProbeTimeout (5s) is deliberately tighter
// than what the data path tolerates (60s, see that const's doc comment), so
// a probe failure there is not proof the endpoint is actually broken —
// nil'ing it would throw away a warm keep-alive pool the data path may
// still be using successfully, for no benefit.
func TestCheckOnce_PreservesActiveConnectionOnProbeFailure(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", statusErr: errDown} // active, fails its probe
	e1 := &fakeConn{endpoint: "e1"}                     // backup, answers and gets promoted
	c := New([]string{"e0", "e1"}, WithDialer(fakeDialerFrom(map[string]*fakeConn{
		"e0": e0, "e1": e1,
	})))

	require.Equal(t, "e0", c.ActiveEndpoint())
	c.mu.Lock()
	warmConn, err := c.connAt(0) // dial e0 so its slot is populated before the probe runs
	c.mu.Unlock()
	require.NoError(t, err)
	require.NotNil(t, warmConn)

	assert.Equal(t, 1, c.checkOnce())

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Same(t, warmConn, c.conns[0],
		"a probe failure on the (then-)active endpoint must not discard its connection — only a genuine call() failure may")
}

// TestCheckOnce_DiscardsBackupConnectionOnProbeFailure is the other half of
// the judgment call above: a probe failure on an endpoint that is NOT
// currently active still discards the connection, since nothing on the data
// path depends on it and forcing a fresh dial before it could be promoted
// (or probed again) is harmless.
func TestCheckOnce_DiscardsBackupConnectionOnProbeFailure(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", statusErr: errDown} // active, fails
	e1 := &fakeConn{endpoint: "e1", statusErr: errDown} // backup, fails
	e2 := &fakeConn{endpoint: "e2"}                     // backup, answers and gets promoted
	c := New([]string{"e0", "e1", "e2"}, WithDialer(fakeDialerFrom(map[string]*fakeConn{
		"e0": e0, "e1": e1, "e2": e2,
	})))

	c.mu.Lock()
	_, err := c.connAt(1) // dial e1 so its slot is populated before the probe runs
	c.mu.Unlock()
	require.NoError(t, err)

	assert.Equal(t, 2, c.checkOnce())

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Nil(t, c.conns[1], "a probe failure on a non-active endpoint must still discard its connection")
}

// TestCheckOnce_AllProbesFailingNeverReportsAllDown covers the promotion-only
// contract: checkOnce may never call onAllDown itself, no matter how many
// times every probe fails in a row. A probe failure only proves an endpoint
// missed the probe's tight defaultProbeTimeout budget, not that it is
// genuinely down (the data path tolerates far more); declaring EventAllDown
// on that basis used to fabricate a CRITICAL "every RPC endpoint is
// unreachable" alert for an endpoint that was merely slow. Detecting a
// genuine total outage remains call()'s job (see TestCall_AllDownReportsOnceThenRecovers).
func TestCheckOnce_AllProbesFailingNeverReportsAllDown(t *testing.T) {
	var events []Event
	c := New([]string{"e0", "e1"},
		WithDialer(fakeDialerFrom(map[string]*fakeConn{
			"e0": {endpoint: "e0", statusErr: errDown},
			"e1": {endpoint: "e1", statusErr: errDown},
		})),
		WithObserver(func(ev Event, endpoint string, err error) { events = append(events, ev) }),
	)

	assert.Equal(t, -1, c.checkOnce())
	assert.Equal(t, -1, c.checkOnce())
	assert.Empty(t, events, "checkOnce must never emit EventAllDown on its own authority")
	assert.False(t, c.allDown, "checkOnce must never flip allDown either")
}

func TestProbe_TimesOutOnAHangingEndpoint(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)

	conn := &hangingConn{fakeConn: fakeConn{endpoint: "e0"}, block: blocked}
	err := probeWithTimeout(conn, 50*time.Millisecond)
	assert.ErrorIs(t, err, ErrProbeTimeout)
}

func TestStartHealthChecks_StopsWithContext(t *testing.T) {
	conn := &countingConn{fakeConn: fakeConn{endpoint: "e0"}}
	c := New([]string{"e0"}, WithDialer(func(endpoint string) (rpcclient.Client, error) {
		return conn, nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	c.StartHealthChecks(ctx, 10*time.Millisecond)

	// Confirm probing actually started before we cancel, so a later
	// "count did not move" assertion cannot pass merely because nothing
	// ever ran in the first place.
	require.Eventually(t, func() bool { return conn.count() > 0 }, 200*time.Millisecond, 5*time.Millisecond,
		"health checks must have probed at least once before cancellation")

	cancel()

	// Give any tick already in flight time to land, then take a baseline.
	time.Sleep(20 * time.Millisecond)
	afterCancel := conn.count()

	// Wait comfortably longer than several ticker intervals: if the ctx.Done
	// arm were not honoured, the ticker (10ms) would have fired many more
	// times by now and moved the count.
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, afterCancel, conn.count(), "health checks must stop probing once ctx is cancelled")
}

// hangingConn never returns from Status until block is closed. It models an
// endpoint that completes the TCP handshake and then goes silent.
type hangingConn struct {
	fakeConn
	block chan struct{}
}

func (h *hangingConn) Status() (*ctypes.ResultStatus, error) {
	<-h.block
	return nil, errors.New("unreachable")
}

// countingConn counts Status() invocations so a test can observe whether the
// health-check goroutine is still probing. Everything else delegates to the
// embedded fakeConn.
type countingConn struct {
	fakeConn
	mu    sync.Mutex
	calls int
}

func (c *countingConn) Status() (*ctypes.ResultStatus, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.fakeConn.Status()
}

func (c *countingConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}
