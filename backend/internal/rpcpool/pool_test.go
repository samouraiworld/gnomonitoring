package rpcpool

import (
	"errors"
	"testing"

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
