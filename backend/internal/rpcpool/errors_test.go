package rpcpool

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsEndpointError_TransportFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"connection refused", fmt.Errorf("unable to send request, %w", &url.Error{
			Op: "Post", URL: "http://x", Err: syscall.ECONNREFUSED,
		})},
		{"dns failure", fmt.Errorf("unable to send request, %w", &url.Error{
			Op: "Post", URL: "http://x", Err: &net.DNSError{Err: "no such host", IsNotFound: true},
		})},
		{"http 502", errors.New("invalid status code received, 502")},
		{"http 503", errors.New("invalid status code received, 503")},
		{"garbage body", errors.New("unable to unmarshal response body, invalid character '<'")},
		{"truncated body", errors.New("unable to read response body, unexpected EOF")},
		{"pruned history", errors.New("height 42 is not available, lowest height is 1000")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, isEndpointError(tc.err), "expected %v to be an endpoint error", tc.err)
		})
	}
}

func TestIsEndpointError_ChainLevelAnswers(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"future height", errors.New("height must be less than or equal to the current blockchain height")},
		{"unknown realm", errors.New("unknown import path gno.land/r/does/not/exist")},
		{"vm panic", errors.New("VM panic: cannot parse proposal id")},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.False(t, isEndpointError(tc.err), "expected %v NOT to be an endpoint error", tc.err)
		})
	}
}

func TestIsEndpointError_NetTimeout(t *testing.T) {
	// A *net.OpError carrying a timeout is what a hung endpoint produces.
	err := fmt.Errorf("unable to send request, %w", &net.OpError{
		Op: "dial", Net: "tcp", Err: errTimeoutStub{},
	})
	assert.True(t, isEndpointError(err))
}

type errTimeoutStub struct{}

func (errTimeoutStub) Error() string   { return "i/o timeout" }
func (errTimeoutStub) Timeout() bool   { return true }
func (errTimeoutStub) Temporary() bool { return true }
