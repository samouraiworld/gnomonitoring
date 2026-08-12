package rpcpool

import (
	"context"
	"errors"
	"testing"
	"time"

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
	c := New([]string{"e0"}, WithDialer(fakeDialerFrom(map[string]*fakeConn{
		"e0": {endpoint: "e0"},
	})))
	ctx, cancel := context.WithCancel(context.Background())
	c.StartHealthChecks(ctx, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	cancel()
	// No assertion beyond "does not panic and does not leak past cancel";
	// -race in CI is what actually guards this.
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
