// Package rpcpool provides a priority-ordered pool of Gno.land RPC endpoints
// behind a single rpcclient.Client. It fails over to the next endpoint when
// the active one stops answering, and returns to the highest-priority healthy
// endpoint on its own once that endpoint recovers.
//
// The package deliberately depends on nothing else under internal/, so both
// gnovalidator and govdao can import it without an import cycle.
package rpcpool

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
)

var (
	// ErrNoEndpoints is returned by every call when the pool was built with
	// an empty endpoint list.
	ErrNoEndpoints = errors.New("rpcpool: no RPC endpoints configured")

	// ErrProbeTimeout is recorded when a health probe exceeded probeTimeout.
	ErrProbeTimeout = errors.New("rpcpool: health probe timed out")

	// ErrAllEndpointsDown is recorded when every endpoint failed a probe.
	ErrAllEndpointsDown = errors.New("rpcpool: all endpoints failed the health probe")
)

// endpointErrorSubstrings are the error texts gno's HTTP RPC transport
// produces when the node (or whatever sits in front of it) is at fault
// rather than the chain. gno wraps these with fmt.Errorf and no sentinel,
// so substring matching is the only option; see
// tm2/pkg/bft/rpc/lib/client/http/client.go.
var endpointErrorSubstrings = []string{
	"unable to send request",           // dial / TLS / timeout
	"invalid status code received",     // 5xx from the node or its proxy
	"unable to read response body",     // truncated response
	"unable to unmarshal response body", // HTML error page from a proxy
	"connection refused",
	"connection reset",
	"no such host",
	"i/o timeout",
	"tls handshake",
	"is not available, lowest height is", // pruned node; an archive node may have it
}

// isEndpointError reports whether err means the endpoint we asked is at
// fault, so another endpoint may well answer correctly. It returns false for
// chain-level answers (unknown realm, future height, VM panic): those are the
// same on every node, and rotating on them would drain the endpoint list and
// leave the pool sitting on a worse node for no reason.
//
// Misclassification is not fatal in either direction: a chain-level error
// wrongly kept here only costs one extra round-trip, and an endpoint failure
// wrongly classified as chain-level is still caught by the background health
// probe (see health.go), which switches away from a silently broken endpoint
// regardless of what any individual call returned.
func isEndpointError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	switch {
	case errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, syscall.ENETUNREACH),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, ErrProbeTimeout):
		return true
	}

	msg := strings.ToLower(err.Error())
	for _, s := range endpointErrorSubstrings {
		if strings.Contains(msg, strings.ToLower(s)) {
			return true
		}
	}
	return false
}
