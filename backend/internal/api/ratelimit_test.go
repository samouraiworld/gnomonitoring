package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestLimiter returns a limiter on a frozen clock, so no token is refilled
// between requests and the tests are deterministic.
func newTestLimiter(perSecond float64, burst int, header string) (*ipRateLimiter, *time.Time) {
	l := newIPRateLimiter(perSecond, burst, header)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	return l, &now
}

func doRequest(h http.Handler, method, remoteAddr string, headers map[string]string) *http.Response {
	req := httptest.NewRequest(method, "/uptime", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result()
}

func TestRateLimit_BurstExceededReturns429WithRetryAfterAndCORS(t *testing.T) {
	internal.Config.AllowedOrigins = []string{"https://app.example.com"}
	defer func() { internal.Config.AllowedOrigins = nil }()

	l, _ := newTestLimiter(1, 3, "")
	h := l.middleware(dummyOKHandler)
	headers := map[string]string{"Origin": "https://app.example.com"}

	for i := 0; i < 3; i++ {
		res := doRequest(h, http.MethodGet, "203.0.113.1:1234", headers)
		assert.Equal(t, http.StatusOK, res.StatusCode, "request %d within burst", i+1)
	}

	res := doRequest(h, http.MethodGet, "203.0.113.1:1234", headers)
	assert.Equal(t, http.StatusTooManyRequests, res.StatusCode)
	assert.Equal(t, "1", res.Header.Get("Retry-After"))
	assert.Equal(t, "https://app.example.com", res.Header.Get("Access-Control-Allow-Origin"))
}

func TestRateLimit_TokenRefillsAfterRetryAfter(t *testing.T) {
	l, now := newTestLimiter(0.5, 1, "")
	h := l.middleware(dummyOKHandler)

	assert.Equal(t, http.StatusOK, doRequest(h, http.MethodGet, "203.0.113.1:1", nil).StatusCode)
	res := doRequest(h, http.MethodGet, "203.0.113.1:1", nil)
	require.Equal(t, http.StatusTooManyRequests, res.StatusCode)
	assert.Equal(t, "2", res.Header.Get("Retry-After"))

	// Rejected requests must not consume tokens: after exactly the advertised
	// delay the next request goes through.
	*now = now.Add(2 * time.Second)
	assert.Equal(t, http.StatusOK, doRequest(h, http.MethodGet, "203.0.113.1:1", nil).StatusCode)
}

func TestRateLimit_DifferentIPsHaveSeparateBudgets(t *testing.T) {
	l, _ := newTestLimiter(1, 1, "")
	h := l.middleware(dummyOKHandler)

	assert.Equal(t, http.StatusOK, doRequest(h, http.MethodGet, "203.0.113.1:1", nil).StatusCode)
	assert.Equal(t, http.StatusTooManyRequests, doRequest(h, http.MethodGet, "203.0.113.1:2", nil).StatusCode)
	assert.Equal(t, http.StatusOK, doRequest(h, http.MethodGet, "203.0.113.2:1", nil).StatusCode)
}

func TestRateLimit_TrustedHeaderEnabled(t *testing.T) {
	l, _ := newTestLimiter(1, 1, "X-Real-IP")
	h := l.middleware(dummyOKHandler)
	proxy := "172.18.0.1:5000"

	// Same proxy address, different client IPs in the header: separate budgets.
	assert.Equal(t, http.StatusOK, doRequest(h, http.MethodGet, proxy, map[string]string{"X-Real-IP": "198.51.100.1"}).StatusCode)
	assert.Equal(t, http.StatusOK, doRequest(h, http.MethodGet, proxy, map[string]string{"X-Real-IP": "198.51.100.2"}).StatusCode)
	assert.Equal(t, http.StatusTooManyRequests, doRequest(h, http.MethodGet, proxy, map[string]string{"X-Real-IP": "198.51.100.1"}).StatusCode)
}

func TestRateLimit_TrustedHeaderDisabledIgnoresForgedHeader(t *testing.T) {
	l, _ := newTestLimiter(1, 1, "")
	h := l.middleware(dummyOKHandler)

	assert.Equal(t, http.StatusOK, doRequest(h, http.MethodGet, "203.0.113.1:1", map[string]string{"X-Real-IP": "198.51.100.1"}).StatusCode)
	// A new forged value must not grant a fresh budget.
	assert.Equal(t, http.StatusTooManyRequests, doRequest(h, http.MethodGet, "203.0.113.1:1", map[string]string{"X-Real-IP": "198.51.100.2"}).StatusCode)
}

func TestRateLimit_OptionsNeverLimited(t *testing.T) {
	l, _ := newTestLimiter(1, 1, "")
	h := l.middleware(dummyOKHandler)

	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusOK, doRequest(h, http.MethodOptions, "203.0.113.1:1", nil).StatusCode)
	}
	// Preflights did not consume the budget.
	assert.Equal(t, http.StatusOK, doRequest(h, http.MethodGet, "203.0.113.1:1", nil).StatusCode)
}

func TestRateLimit_CleanupEvictsIdleEntries(t *testing.T) {
	l, now := newTestLimiter(1, 1, "")
	l.allow("203.0.113.1")
	*now = now.Add(l.idleTTL / 2)
	l.allow("203.0.113.2")

	*now = now.Add(l.idleTTL/2 + time.Second)
	l.cleanup()

	l.mu.Lock()
	defer l.mu.Unlock()
	assert.NotContains(t, l.visitors, "203.0.113.1")
	assert.Contains(t, l.visitors, "203.0.113.2")
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		remoteAddr string
		values     []string
		want       string
	}{
		{"remote addr only", "", "203.0.113.7:4321", nil, "203.0.113.7"},
		{"header ignored when disabled", "", "203.0.113.7:4321", []string{"198.51.100.1"}, "203.0.113.7"},
		{"x-real-ip", "X-Real-IP", "172.18.0.1:1", []string{"198.51.100.1"}, "198.51.100.1"},
		{"xff takes proxy-appended last value", "X-Forwarded-For", "172.18.0.1:1", []string{"1.2.3.4, 198.51.100.1"}, "198.51.100.1"},
		{"xff across header lines takes last", "X-Forwarded-For", "172.18.0.1:1", []string{"1.2.3.4", "198.51.100.1"}, "198.51.100.1"},
		{"missing header falls back", "X-Real-IP", "172.18.0.1:1", nil, "172.18.0.1"},
		{"garbage header falls back", "X-Real-IP", "172.18.0.1:1", []string{"not-an-ip"}, "172.18.0.1"},
		{"ipv6 grouped by /64", "", "[2001:db8:1:2:aaaa::1]:443", nil, "2001:db8:1:2::/64"},
		{"ipv6 same /64 same key", "", "[2001:db8:1:2:bbbb::9]:443", nil, "2001:db8:1:2::/64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for _, v := range tt.values {
				req.Header.Add("X-Real-IP", v)
				req.Header.Add("X-Forwarded-For", v)
			}
			assert.Equal(t, tt.want, clientIP(req, tt.header))
		})
	}
}
