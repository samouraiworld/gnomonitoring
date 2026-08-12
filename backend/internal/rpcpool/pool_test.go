package rpcpool

import (
	"errors"
	"sync"
	"testing"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errDown = errors.New("unable to send request, connection refused")

func TestCall_WalksEveryEndpointUntilOneAnswers(t *testing.T) {
	// Endpoints 0 and 1 are down; the old one-shot rotation would have given
	// up here even though endpoint 2 is healthy.
	conns := map[string]*fakeConn{
		"e0": {endpoint: "e0", abciErr: errDown},
		"e1": {endpoint: "e1", abciErr: errDown},
		"e2": {endpoint: "e2"},
	}
	c := New([]string{"e0", "e1", "e2"}, WithDialer(fakeDialerFrom(conns)))

	_, err := c.ABCIInfo()
	require.NoError(t, err)
	assert.Equal(t, "e2", c.ActiveEndpoint(), "pool must stick to the endpoint that answered")
}

func TestCall_SticksToTheHealthyEndpointOnLaterCalls(t *testing.T) {
	calls0, calls1 := 0, 0
	conns := map[string]*fakeConn{
		"e0": {endpoint: "e0", abciErr: errDown, calls: &calls0},
		"e1": {endpoint: "e1", calls: &calls1},
	}
	c := New([]string{"e0", "e1"}, WithDialer(fakeDialerFrom(conns)))

	for i := 0; i < 3; i++ {
		_, err := c.ABCIInfo()
		require.NoError(t, err)
	}
	assert.Equal(t, 1, calls0, "the dead primary must be tried only on the first call")
	assert.Equal(t, 3, calls1)
}

func TestCall_DoesNotRotateOnChainLevelError(t *testing.T) {
	calls0, calls1 := 0, 0
	chainErr := errors.New("unknown import path gno.land/r/nope")
	conns := map[string]*fakeConn{
		"e0": {endpoint: "e0", abciErr: chainErr, calls: &calls0},
		"e1": {endpoint: "e1", calls: &calls1},
	}
	c := New([]string{"e0", "e1"}, WithDialer(fakeDialerFrom(conns)))

	_, err := c.ABCIInfo()
	require.ErrorIs(t, err, chainErr)
	assert.Equal(t, 1, calls0)
	assert.Equal(t, 0, calls1, "a chain-level answer must not burn another endpoint")
	assert.Equal(t, "e0", c.ActiveEndpoint())
}

func TestCall_AllDownReportsOnceThenRecovers(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", abciErr: errDown}
	e1 := &fakeConn{endpoint: "e1", abciErr: errDown}
	conns := map[string]*fakeConn{"e0": e0, "e1": e1}

	var events []Event
	c := New([]string{"e0", "e1"},
		WithDialer(fakeDialerFrom(conns)),
		WithObserver(func(ev Event, endpoint string, err error) {
			events = append(events, ev)
		}),
	)

	_, err := c.ABCIInfo()
	require.Error(t, err)
	_, err = c.ABCIInfo()
	require.Error(t, err)
	assert.Equal(t, []Event{EventAllDown}, events, "EventAllDown must not repeat while still down")

	e1.abciErr = nil
	_, err = c.ABCIInfo()
	require.NoError(t, err)
	assert.Equal(t, []Event{EventAllDown, EventRecovered}, events)
}

// TestCall_ConcurrentSuccessNotOverwrittenByStaleFailure reproduces the
// race a code review found in onSuccess/onAllDown: a call that has been in
// flight since before the pool recovered must not be allowed to re-report
// EventAllDown once a later, faster call has already recovered it.
//
//  1. A first call establishes a real outage: both endpoints down.
//  2. G1 starts, fails on e0, and gets parked inside e1's ABCIInfo call
//     having already captured a failing verdict (the endpoint had not
//     recovered yet when G1 asked).
//  3. e1 recovers, and G2 — dispatched only now, strictly after G1 — fails
//     on e0 and succeeds on e1, recovering the pool and firing
//     EventRecovered.
//  4. G1 is released. It concludes its own call as a total failure (its
//     captured verdict never changed), but that conclusion must be
//     discarded as stale: no second EventAllDown, and the pool must stay
//     on the endpoint G2 already recovered.
func TestCall_ConcurrentSuccessNotOverwrittenByStaleFailure(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", abciErr: errDown} // always down
	e1 := &flappingConn{fakeConn: &fakeConn{endpoint: "e1"}}

	dialer := func(endpoint string) (rpcclient.Client, error) {
		switch endpoint {
		case "e0":
			return e0, nil
		case "e1":
			return e1, nil
		default:
			panic("unexpected endpoint: " + endpoint)
		}
	}

	var evMu sync.Mutex
	var events []Event
	c := New([]string{"e0", "e1"}, WithDialer(dialer), WithObserver(func(ev Event, endpoint string, err error) {
		evMu.Lock()
		events = append(events, ev)
		evMu.Unlock()
	}))

	// Baseline: a real outage, both endpoints down.
	_, err := c.ABCIInfo()
	require.Error(t, err)

	// Arm e1 so the next call to it parks right after being invoked. G1
	// will get stuck here, its failing verdict already captured.
	entered := e1.arm()

	g1Done := make(chan error, 1)
	go func() {
		_, err := c.ABCIInfo() // G1: e0 fails, then parks inside e1.
		g1Done <- err
	}()

	// Wait until G1 is parked inside e1's call. Receiving from a channel
	// G1 closed establishes happens-before ordering, so G1's call() has
	// already captured its generation number by the time we proceed.
	<-entered

	// The endpoint actually recovers, and a second, independently
	// dispatched call (G2) observes it and completes before G1 does.
	e1.recover()
	_, err = c.ABCIInfo() // G2: e0 fails, e1 succeeds.
	require.NoError(t, err)
	assert.Equal(t, "e1", c.ActiveEndpoint())

	evMu.Lock()
	afterG2 := append([]Event(nil), events...)
	evMu.Unlock()
	require.Equal(t, []Event{EventAllDown, EventRecovered}, afterG2,
		"G2 must have already recovered the pool before G1 concludes")

	// Release G1. It observes the failing verdict it captured before the
	// recovery and must complete without corrupting the state G2 already
	// established.
	e1.releaseGate()
	g1Err := <-g1Done
	require.Error(t, g1Err, "G1's own view of the world was a total outage")

	evMu.Lock()
	defer evMu.Unlock()
	assert.Equal(t, []Event{EventAllDown, EventRecovered}, events,
		"a stale failing call must not re-emit EventAllDown after the pool already recovered")
	assert.Equal(t, "e1", c.ActiveEndpoint(),
		"the stale failure must not knock the pool off the endpoint G2 already recovered")
}

func TestCall_EmptyEndpointList(t *testing.T) {
	c := New(nil)
	_, err := c.ABCIInfo()
	assert.ErrorIs(t, err, ErrNoEndpoints)
}

func TestNew_CopiesTheEndpointSlice(t *testing.T) {
	in := []string{"e0", "e1"}
	c := New(in, WithDialer(fakeDialerFrom(map[string]*fakeConn{
		"e0": {endpoint: "e0"}, "e1": {endpoint: "e1"},
	})))
	in[0] = "mutated"
	assert.Equal(t, []string{"e0", "e1"}, c.Endpoints())
}
