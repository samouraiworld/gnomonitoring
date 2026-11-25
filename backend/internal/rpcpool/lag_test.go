package rpcpool

import (
	"testing"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLagPool builds a pool over e0..eN-1 served by the given stubs, with the
// lag threshold fixed at maxLag blocks.
func newLagPool(t *testing.T, maxLag int64, conns ...*fakeConn) *Client {
	t.Helper()
	endpoints := make([]string, len(conns))
	byName := make(map[string]*fakeConn, len(conns))
	for i, fc := range conns {
		endpoints[i] = fc.endpoint
		byName[fc.endpoint] = fc
	}
	return New(endpoints,
		WithDialer(fakeDialerFrom(byName)),
		WithMaxLag(func() int64 { return maxLag }),
	)
}

// TestCheckOnce_FailsOverFromFrozenActive reproduces the false "Blockchain
// stuck" defect: the active endpoint still answers Status but is frozen at
// an old height while the backup keeps advancing. The probe used to keep
// only Status's error, so the frozen endpoint stayed healthy and active and
// the realtime loop saw the chain stall.
func TestCheckOnce_FailsOverFromFrozenActive(t *testing.T) {
	frozen := &fakeConn{endpoint: "e0", height: 100}
	live := &fakeConn{endpoint: "e1", height: 160}
	c := newLagPool(t, 10, frozen, live)

	require.Equal(t, "e0", c.ActiveEndpoint())
	assert.Equal(t, 1, c.checkOnce())
	assert.Equal(t, "e1", c.ActiveEndpoint(), "an endpoint lagging the best one by more than the threshold must lose the active slot")
}

func TestCheckOnce_KeepsPrimaryWithinLagThreshold(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 150},
		&fakeConn{endpoint: "e1", height: 160},
	)

	assert.Equal(t, 0, c.checkOnce())
	assert.Equal(t, "e0", c.ActiveEndpoint(), "a primary a few blocks behind is not frozen and keeps priority")
}

func TestCheckOnce_SkipsCatchingUpEndpoint(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 160, catchingUp: true},
		&fakeConn{endpoint: "e1", height: 160},
	)

	assert.Equal(t, 1, c.checkOnce())
	assert.Equal(t, "e1", c.ActiveEndpoint())
}

// When no endpoint is healthy (every answering node is catching up), the
// probe still promotes the most advanced answering one rather than none.
func TestCheckOnce_PromotesMostAdvancedWhenNoneHealthy(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 100, catchingUp: true},
		&fakeConn{endpoint: "e1", height: 140, catchingUp: true},
	)

	assert.Equal(t, 1, c.checkOnce())
	assert.Equal(t, "e1", c.ActiveEndpoint())
}

// The primary comes back from a freeze: once it is within the threshold
// again, the probe returns the pool to it.
func TestCheckOnce_ReturnsToPrimaryOnceItCatchesUp(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", height: 100}
	e1 := &fakeConn{endpoint: "e1", height: 160}
	c := newLagPool(t, 10, e0, e1)

	require.Equal(t, 1, c.checkOnce())
	e0.height = 161
	e1.height = 162
	assert.Equal(t, 0, c.checkOnce())
	assert.Equal(t, "e0", c.ActiveEndpoint())
}

func TestCheckOnce_RotationReportsToObserver(t *testing.T) {
	var events []Event
	var endpoints []string
	c := New([]string{"e0", "e1"},
		WithDialer(fakeDialerFrom(map[string]*fakeConn{
			"e0": {endpoint: "e0", height: 100},
			"e1": {endpoint: "e1", height: 160},
		})),
		WithMaxLag(func() int64 { return 10 }),
		WithObserver(func(ev Event, endpoint string, err error) {
			events = append(events, ev)
			endpoints = append(endpoints, endpoint)
		}),
	)

	require.Equal(t, 1, c.checkOnce())
	assert.Equal(t, []Event{EventRotated}, events)
	assert.Equal(t, []string{"e1"}, endpoints)
}

func TestMaxLagBlocks_DefaultsOnMissingOrNonPositive(t *testing.T) {
	assert.Equal(t, DefaultMaxLagBlocks, New(nil).maxLagBlocks())
	assert.Equal(t, DefaultMaxLagBlocks, New(nil, WithMaxLag(func() int64 { return 0 })).maxLagBlocks())
	assert.Equal(t, DefaultMaxLagBlocks, New(nil, WithMaxLag(func() int64 { return -3 })).maxLagBlocks())
	assert.Equal(t, int64(4), New(nil, WithMaxLag(func() int64 { return 4 })).maxLagBlocks())
}

func TestConfirmStall_DisprovedByAdvancingEndpoint(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 100},
		&fakeConn{endpoint: "e1", height: 105},
	)

	assert.False(t, c.ConfirmStall(100), "another endpoint past the stall height proves the chain is moving")
	assert.Equal(t, "e1", c.ActiveEndpoint(),
		"a disproved stall must move the pool to an endpoint past the stall height, even one within the lag threshold")
}

func TestConfirmStall_ConfirmedWhenEveryEndpointAgrees(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 100},
		&fakeConn{endpoint: "e1", height: 100},
	)

	assert.True(t, c.ConfirmStall(100))
	assert.Equal(t, "e0", c.ActiveEndpoint())
}

// A lower backup (e.g. still syncing) does not disprove the stall.
func TestConfirmStall_ConfirmedWhenOtherEndpointIsBehind(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 100},
		&fakeConn{endpoint: "e1", height: 90},
	)

	assert.True(t, c.ConfirmStall(100))
}

// With no other endpoint reachable the stall cannot be checked elsewhere:
// keep today's behavior and let the alert go out.
func TestConfirmStall_ConfirmedWhenNoOtherEndpointAnswers(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 100},
		&fakeConn{endpoint: "e1", statusErr: errDown},
	)

	assert.True(t, c.ConfirmStall(100))
	assert.Equal(t, "e0", c.ActiveEndpoint())
}

// With a single endpoint there is nothing to compare against: today's
// behavior, the stall is confirmed without probing.
func TestConfirmStall_SingleEndpointKeepsTodayBehavior(t *testing.T) {
	conn := &countingConn{fakeConn: fakeConn{endpoint: "e0", height: 100}}
	c := New([]string{"e0"}, WithDialer(func(string) (rpcclient.Client, error) { return conn, nil }))

	assert.True(t, c.ConfirmStall(100))
	assert.Zero(t, conn.count(), "a single-endpoint pool must not probe to confirm a stall")
}

// A call that started on the frozen endpoint before the probe moved the pool
// away, and that succeeds after the move, must not put the frozen endpoint
// back in the active slot.
func TestOnSuccess_InFlightCallDoesNotUndoProbeSwitch(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 100},
		&fakeConn{endpoint: "e1", height: 160},
	)

	require.Equal(t, 1, c.checkOnce())

	// The call on e0 returns after the switch, on its first attempt.
	c.onSuccess(c.lastObservedAt.Add(1), 0, false)
	assert.Equal(t, "e1", c.ActiveEndpoint(), "a non-rotating success must not move the active endpoint")
}

// A backup on another network (or on an old chain ID after a reset) may
// report a far higher height. It must neither take the active slot nor
// disprove a real stall.
func TestCheckOnce_IgnoresEndpointOnAnotherNetwork(t *testing.T) {
	c := newLagPool(t, 10,
		&fakeConn{endpoint: "e0", height: 100, network: "dev"},
		&fakeConn{endpoint: "e1", height: 5000, network: "other"},
	)

	assert.Equal(t, 0, c.checkOnce())
	assert.Equal(t, "e0", c.ActiveEndpoint())
	assert.True(t, c.ConfirmStall(100), "a height on another network says nothing about this chain")
	assert.Equal(t, "e0", c.ActiveEndpoint())
}

// After ConfirmStall moved the pool off a frozen endpoint that is still
// within the lag threshold, the next probe must not move it back while that
// endpoint has not moved past its frozen height.
func TestCheckOnce_DoesNotReturnToEndpointFrozenByConfirmStall(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", height: 100}
	e1 := &fakeConn{endpoint: "e1", height: 105}
	c := newLagPool(t, 10, e0, e1)

	require.False(t, c.ConfirmStall(100))
	require.Equal(t, "e1", c.ActiveEndpoint())

	e1.height = 106
	assert.Equal(t, 1, c.checkOnce(), "e0 is still frozen at 100")
	assert.Equal(t, "e1", c.ActiveEndpoint())

	e0.height = 107
	e1.height = 107
	assert.Equal(t, 0, c.checkOnce(), "once e0 moves past its frozen height it is healthy again")
	assert.Equal(t, "e0", c.ActiveEndpoint())
}

// A probe answered fast by e0 and slowly by e1. While the slow probe was
// pending, the data path rotated to e1. The probe's promotion of e0 rests on
// an answer older than that rotation and must not override it.
func TestCheckOnce_PromotionUsesEachEndpointsOwnObservationTime(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", height: 100}
	e1 := &slowStatusConn{fakeConn: fakeConn{endpoint: "e1", height: 100}, delay: 100 * time.Millisecond}
	c := New([]string{"e0", "e1"}, WithDialer(func(endpoint string) (rpcclient.Client, error) {
		if endpoint == "e0" {
			return e0, nil
		}
		return e1, nil
	}))

	go func() {
		time.Sleep(50 * time.Millisecond)
		c.onSuccess(time.Now(), 1, true) // data-path rotation while e1's probe is pending
	}()

	assert.Equal(t, 0, c.checkOnce())
	assert.Equal(t, "e1", c.ActiveEndpoint(), "a probe answer older than the rotation must not undo it")
}

// A success that ends a full outage reports and activates the endpoint that
// actually answered, even when the active endpoint changed while that call
// was in flight.
func TestOnSuccess_RecoveryActivatesTheEndpointThatAnswered(t *testing.T) {
	var recovered []string
	c := New([]string{"e0", "e1"},
		WithDialer(fakeDialerFrom(map[string]*fakeConn{"e0": {endpoint: "e0"}, "e1": {endpoint: "e1"}})),
		WithObserver(func(ev Event, endpoint string, err error) {
			if ev == EventRecovered {
				recovered = append(recovered, endpoint)
			}
		}),
	)
	c.activeIdx = 1
	c.onAllDown(time.Now(), errDown)

	require.True(t, c.onSuccess(time.Now().Add(time.Millisecond), 0, false))
	assert.Equal(t, "e0", c.ActiveEndpoint())
	assert.Equal(t, []string{"e0"}, recovered)
}

// End to end through call(): a data-path call parked on the frozen endpoint
// while the probe moves the pool away, then succeeding, leaves the pool on
// the live endpoint.
func TestCall_InFlightSuccessOnFrozenEndpointDoesNotUndoProbeSwitch(t *testing.T) {
	frozen := &flappingConn{fakeConn: &fakeConn{endpoint: "e0", height: 100}, recovered: true}
	live := &fakeConn{endpoint: "e1", height: 160}
	c := New([]string{"e0", "e1"},
		WithDialer(func(endpoint string) (rpcclient.Client, error) {
			if endpoint == "e0" {
				return frozen, nil
			}
			return live, nil
		}),
		WithMaxLag(func() int64 { return 10 }),
	)

	entered := frozen.arm()
	done := make(chan error, 1)
	go func() {
		_, err := c.ABCIInfo()
		done <- err
	}()
	<-entered

	require.Equal(t, 1, c.checkOnce())
	frozen.releaseGate()
	require.NoError(t, <-done)
	assert.Equal(t, "e1", c.ActiveEndpoint())
}

// slowStatusConn answers Status after delay.
type slowStatusConn struct {
	fakeConn
	delay time.Duration
}

func (s *slowStatusConn) Status() (*ctypes.ResultStatus, error) {
	time.Sleep(s.delay)
	return s.fakeConn.Status()
}
