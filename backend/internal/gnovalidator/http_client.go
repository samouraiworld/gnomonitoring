package gnovalidator

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// rpcHTTPClient bounds the plain-HTTP RPC calls (/validators, /genesis) that
// do not go through the rpcpool client. http.DefaultClient has no timeout at
// all, so an endpoint that completes the TCP handshake and then goes silent
// would block InitMonikerMap indefinitely — and with it the whole
// StartValidatorMonitoring startup, before CollectParticipation and
// WatchValidatorAlerts are ever launched.
var rpcHTTPClient = &http.Client{Timeout: 15 * time.Second}

// getWithFailover issues GET <endpoint><path> against each endpoint in
// priority order and returns the first 2xx response, along with the endpoint
// that served it. The caller owns the response body and must close it.
//
// This replaces the previous doWithRetry loop, which retried the same URL
// three times: a dead primary took the valset refresh, the moniker map, the
// voting-power snapshot and the valset-change alerts down with it, even when
// a healthy backup was configured.
func getWithFailover(endpoints []string, path string) (*http.Response, string, error) {
	if len(endpoints) == 0 {
		return nil, "", fmt.Errorf("no RPC endpoints configured")
	}

	var firstErr error
	for _, endpoint := range endpoints {
		url := strings.TrimRight(endpoint, "/") + path

		resp, err := rpcHTTPClient.Get(url)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[rpc-http] GET %s failed: %v", url, err)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			resp.Body.Close()
			err := fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[rpc-http] %v", err)
			continue
		}
		return resp, endpoint, nil
	}

	return nil, "", fmt.Errorf("all %d endpoints failed for %s, first error: %w", len(endpoints), path, firstErr)
}
