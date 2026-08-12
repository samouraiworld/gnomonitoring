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

func TestCheckOnce_AllDownEmitsEventOnce(t *testing.T) {
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
	assert.Equal(t, []Event{EventAllDown}, events)
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
