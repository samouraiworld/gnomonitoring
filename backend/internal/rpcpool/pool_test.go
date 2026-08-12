package rpcpool

import (
	"errors"
	"sync"
	"testing"
	"time"

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

// flappingDialer builds a Dialer that always fails on e0 and defers to
// flap for e1. Shared by the two concurrency regression tests below.
func flappingDialer(e0 *fakeConn, flap *flappingConn) Dialer {
	return func(endpoint string) (rpcclient.Client, error) {
		switch endpoint {
		case "e0":
			return e0, nil
		case "e1":
			return flap, nil
		default:
			panic("unexpected endpoint: " + endpoint)
		}
	}
}

// TestCall_ConcurrentSuccessNotOverwrittenByStaleFailure reproduces the
// race a code review found in onSuccess/onAllDown: a call still in flight
// when a fresher call has already resolved the pool must not be allowed to
// override that fresher result with its own, older observation.
//
// The ordering that decides which observation is authoritative is when
// each call actually got its answer (call's doc comment: observedAt), not
// when it started or which one's onSuccess/onAllDown happens to reach the
// mutex first. To exercise that honestly, G2 (the recovering call) is the
// one left in flight, and G1 (the stale failure) is the one that runs to
// completion first:
//
//  1. G2 starts and immediately parks inside e1's ABCIInfo call — it is
//     "in flight" from this point on, before G1 even starts.
//  2. G1 starts, fails on e0 and e1 (e1 has not recovered yet) and applies
//     its failure — a genuine EventAllDown, since the pool was healthy
//     until now. This is the older of the two observations, timestamp-wise,
//     even though it is the one whose onAllDown call actually runs first.
//  3. e1 recovers, and G2 is released. Its success is observed strictly
//     after G1's failure, so it must apply and fire EventRecovered.
//
// A generation/entry-order guard (round 1 of this fix) gets this backwards:
// it would key on which call *started* first (G2, since it was launched
// before G1) and discard G1's — actually-older — result for the wrong
// reason. The observedAt guard gets it right for the right reason: G1's
// result is discarded because it was observed first, period.
func TestCall_ConcurrentSuccessNotOverwrittenByStaleFailure(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", abciErr: errDown} // always down
	e1 := &flappingConn{fakeConn: &fakeConn{endpoint: "e1"}}

	var evMu sync.Mutex
	var events []Event
	c := New([]string{"e0", "e1"}, WithDialer(flappingDialer(e0, e1)), WithObserver(func(ev Event, endpoint string, err error) {
		evMu.Lock()
		events = append(events, ev)
		evMu.Unlock()
	}))

	// Arm e1 so the next call to it parks right after being invoked, and
	// resolves with whatever e1.recover()/fail() says at release time.
	entered := e1.arm()

	// G2: starts first and immediately parks inside e1 — in flight before
	// G1 even starts.
	g2Done := make(chan error, 1)
	go func() {
		_, err := c.ABCIInfo()
		g2Done <- err
	}()
	<-entered // G2 is parked inside e1's call.

	// G1: dispatched after G2 is already in flight, but concludes
	// immediately — e1's arm slot was already consumed by G2, so G1 sees
	// the endpoint exactly as it is right now: down. This is call() at its
	// most ordinary, no blocking involved for G1 at all.
	_, err := c.ABCIInfo()
	require.Error(t, err, "G1: e0 and e1 are both down right now")

	evMu.Lock()
	afterG1 := append([]Event(nil), events...)
	evMu.Unlock()
	require.Equal(t, []Event{EventAllDown}, afterG1,
		"G1's genuine, immediate observation must apply: the pool really is down")

	// The endpoint actually recovers, and G2 — parked since before G1 even
	// started — is released and observes it.
	e1.recover()
	e1.releaseGate()
	g2Err := <-g2Done
	require.NoError(t, g2Err, "G2: e1 has recovered by the time its call finally concludes")

	evMu.Lock()
	defer evMu.Unlock()
	assert.Equal(t, []Event{EventAllDown, EventRecovered}, events,
		"G2's later observation must apply and recover the pool")
	assert.Equal(t, "e1", c.ActiveEndpoint())
}

// TestCall_SlowGenuineOutageAppliesOverEarlierFastSuccess is the symmetric
// case a follow-up review found the round-1 fix (call-entry-order
// generation guard) got backwards: a slow call that has genuinely observed
// every endpoint down must not be discarded merely because a call that
// started later happened to finish first with a success.
//
//  1. e1 starts healthy. G1 starts and immediately parks inside e1's
//     ABCIInfo call — in flight before G2 even starts.
//  2. G2, dispatched after G1, sails straight through (e1's arm slot is
//     already consumed) and observes e1 as healthy right now — a fast
//     "keep-alive connection that answers immediately". It applies:
//     EventRotated.
//  3. e1 actually goes down for real, and G1 — still parked since before
//     G2 started — is released and observes it. G1's failure is the
//     later, fresher observation (it is released, and so concludes, after
//     G2 already returned) and must apply: EventAllDown must fire, even
//     though G1 started before G2 and finished after it.
//
// A call-entry-order guard would key on G1 having started first and
// discard its result as "not the latest call" — silently hiding a real,
// ongoing outage. The observedAt guard applies it, because it really is
// the freshest information the pool has.
func TestCall_SlowGenuineOutageAppliesOverEarlierFastSuccess(t *testing.T) {
	e0 := &fakeConn{endpoint: "e0", abciErr: errDown} // always down
	e1 := &flappingConn{fakeConn: &fakeConn{endpoint: "e1"}}
	e1.recover() // starts healthy.

	var evMu sync.Mutex
	var events []Event
	c := New([]string{"e0", "e1"}, WithDialer(flappingDialer(e0, e1)), WithObserver(func(ev Event, endpoint string, err error) {
		evMu.Lock()
		events = append(events, ev)
		evMu.Unlock()
	}))

	// Arm e1 so the next call to it parks right after being invoked.
	entered := e1.arm()

	// G1: a slow probe that starts now and parks inside e1's call.
	g1Done := make(chan error, 1)
	go func() {
		_, err := c.ABCIInfo()
		g1Done <- err
	}()
	<-entered // G1 is parked inside e1's call.

	// G2: dispatched after G1 is already in flight, but faster — e1's arm
	// slot is already consumed, so G2 sails straight through and observes
	// the endpoint exactly as it is right now: healthy.
	_, err := c.ABCIInfo()
	require.NoError(t, err, "G2: e1 is healthy right now")
	assert.Equal(t, "e1", c.ActiveEndpoint())

	evMu.Lock()
	afterG2 := append([]Event(nil), events...)
	evMu.Unlock()
	require.Equal(t, []Event{EventRotated}, afterG2, "G2's fast success must apply")

	// e1 genuinely goes down, and G1 — parked since before G2 even
	// started — is released and observes it.
	e1.fail()
	e1.releaseGate()
	g1Err := <-g1Done
	require.Error(t, g1Err, "G1: e1 is genuinely down by the time its call finally concludes")

	evMu.Lock()
	defer evMu.Unlock()
	assert.Equal(t, []Event{EventRotated, EventAllDown}, events,
		"a slow-but-fresher outage must not be discarded by an earlier, now-stale success")
}

// TestOnAllDown_OlderObservationDiscardedAfterNewerApplied directly
// exercises the observedAt guard's comparison, independent of goroutine
// scheduling.
//
// In this implementation observedAt is captured immediately adjacent to
// the mutex operation in call() (right after fn(conn) returns, right
// before onSuccess/onAllDown is invoked with it) — so for any call whose
// completion is driven by blocking a fake conn, release order and
// observedAt order are the same thing by construction: releasing a call
// is what makes its fn(conn) return, which is what produces its
// observedAt. Neither of the two call()-driven tests above can therefore
// exhibit an observedAt value out of step with arrival-at-the-mutex order
// — that coupling is exactly what makes this implementation robust against
// scheduler reordering in the first place. This test instead invokes
// onSuccess/onAllDown directly with an explicitly out-of-order timestamp,
// to verify the comparison itself rather than rely on scheduling to
// produce one.
func TestOnAllDown_OlderObservationDiscardedAfterNewerApplied(t *testing.T) {
	c := New([]string{"e0", "e1"}, WithDialer(fakeDialerFrom(map[string]*fakeConn{
		"e0": {endpoint: "e0"}, "e1": {endpoint: "e1"},
	})))

	var events []Event
	c.obs = func(ev Event, endpoint string, err error) { events = append(events, ev) }

	older := time.Now()
	newer := older.Add(time.Second)

	c.onSuccess(newer, 1, true)
	require.Equal(t, []Event{EventRotated}, events)
	require.Equal(t, "e1", c.ActiveEndpoint())

	c.onAllDown(older, errDown)
	assert.Equal(t, []Event{EventRotated}, events,
		"an observation timestamped before what's already applied must be discarded")
	assert.Equal(t, "e1", c.ActiveEndpoint(), "state must be untouched by the discarded observation")
	assert.False(t, c.allDown, "allDown must remain as the newer call left it")
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
