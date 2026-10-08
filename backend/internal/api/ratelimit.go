package api

import (
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Defaults used when rate_limit_per_second / rate_limit_burst are absent
// (or <= 0) in config.yaml. A dashboard page load fans out to a dozen public
// endpoints at once, so the burst is sized well above that.
const (
	defaultRateLimitPerSecond = 10
	defaultRateLimitBurst     = 40
	// minRateLimitIdleTTL is the shortest time an IP's limiter is kept after
	// its last request. The effective TTL is never shorter than the time the
	// bucket needs to refill, so evicting an entry never hands out a fresh
	// burst earlier than the limiter itself would have.
	minRateLimitIdleTTL      = 3 * time.Minute
	rateLimitCleanupInterval = time.Minute
	// maxRateLimitVisitors caps the per-IP map. Once full, IPs without an
	// entry share a single overflow bucket until cleanup frees room, so a
	// flood of distinct addresses cannot grow memory without bound.
	maxRateLimitVisitors = 100_000
)

// ipRateLimiter is a per-client-IP token bucket for the public API routes.
// It exists to keep anonymous clients from saturating Postgres, which also
// runs the alerting loop: a flooded API would delay validator alerts.
type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	overflow *visitor
	// maxVisitors is maxRateLimitVisitors outside tests.
	maxVisitors int

	// Logged once each: a missing proxy setup otherwise only shows up as
	// unrelated users sharing one budget.
	warnMissingHeader sync.Once
	warnPrivatePeer   sync.Once

	limit         rate.Limit
	burst         int
	trustedHeader string
	idleTTL       time.Duration
	now           func() time.Time
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newIPRateLimiter builds a limiter allowing perSecond requests per second per
// client IP with the given burst. trustedHeader names the header carrying the
// real client IP set by a trusted reverse proxy; empty means r.RemoteAddr is
// used as is.
func newIPRateLimiter(perSecond float64, burst int, trustedHeader string) *ipRateLimiter {
	// NaN and +Inf would silently disable the limit.
	if perSecond <= 0 || math.IsNaN(perSecond) || math.IsInf(perSecond, 0) {
		perSecond = defaultRateLimitPerSecond
	}
	if burst <= 0 {
		burst = defaultRateLimitBurst
	}
	idleTTL := time.Duration(float64(burst) / perSecond * float64(time.Second))
	if idleTTL < minRateLimitIdleTTL {
		idleTTL = minRateLimitIdleTTL
	}
	return &ipRateLimiter{
		visitors:      make(map[string]*visitor),
		overflow:      &visitor{limiter: rate.NewLimiter(rate.Limit(perSecond), burst)},
		maxVisitors:   maxRateLimitVisitors,
		limit:         rate.Limit(perSecond),
		burst:         burst,
		trustedHeader: strings.TrimSpace(trustedHeader),
		idleTTL:       idleTTL,
		now:           time.Now,
	}
}

// middleware rejects requests over the per-IP budget with 429 and a
// Retry-After header. OPTIONS preflights are never counted: they are cheap
// and a rejected preflight would surface in the browser as a CORS error
// rather than as the 429 it is.
func (l *ipRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if wait, ok := l.allow(l.key(r)); !ok {
			// CORS headers must be on the 429 too, otherwise the browser hides
			// the status from the calling page behind a generic CORS failure.
			EnableCORS(w, r)
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After")
			secs := int(math.Ceil(wait.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allow consumes one token for key. When the bucket is empty it returns the
// time until the next token is available and false.
func (l *ipRateLimiter) allow(key string) (time.Duration, bool) {
	now := l.now()

	l.mu.Lock()
	v, ok := l.visitors[key]
	if !ok {
		if len(l.visitors) >= l.maxVisitors {
			v = l.overflow
		} else {
			v = &visitor{limiter: rate.NewLimiter(l.limit, l.burst)}
			l.visitors[key] = v
		}
	}
	v.lastSeen = now
	l.mu.Unlock()

	// AllowN only takes a token when one is available, so concurrent rejected
	// requests never leave the bucket in debt (unlike Reserve + Cancel, whose
	// cancellation is best effort once later reservations exist).
	if v.limiter.AllowN(now, 1) {
		return 0, true
	}
	missing := 1 - v.limiter.TokensAt(now)
	return time.Duration(missing / float64(l.limit) * float64(time.Second)), false
}

// key returns the rate-limit key for r, logging once when the proxy setup
// looks wrong.
func (l *ipRateLimiter) key(r *http.Request) string {
	if l.trustedHeader != "" {
		if r.Header.Get(l.trustedHeader) == "" {
			l.warnMissingHeader.Do(func() {
				log.Printf("[ratelimit] warning: trusted_proxy_header %q missing on a request from %s; "+
					"falling back to the peer address, so such clients share one budget. Check the reverse proxy config.",
					l.trustedHeader, r.RemoteAddr)
			})
		}
	} else if ip := net.ParseIP(peerHost(r)); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		l.warnPrivatePeer.Do(func() {
			log.Printf("[ratelimit] warning: request from private address %s with trusted_proxy_header unset; "+
				"if the API sits behind a reverse proxy, every client shares one budget. Set trusted_proxy_header.",
				r.RemoteAddr)
		})
	}
	return clientIP(r, l.trustedHeader)
}

// cleanup evicts the limiters of IPs idle for longer than idleTTL so the map
// does not grow without bound.
func (l *ipRateLimiter) cleanup() {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, v := range l.visitors {
		if now.Sub(v.lastSeen) > l.idleTTL {
			delete(l.visitors, key)
		}
	}
}

// startCleanup runs cleanup every interval for the lifetime of the process.
func (l *ipRateLimiter) startCleanup(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			l.cleanup()
		}
	}()
}

// clientIP returns the rate-limit key for r. When trustedHeader is set, the
// last comma-separated value of that header is used: a proxy that appends to
// X-Forwarded-For puts the address it saw last, while anything before it was
// supplied by the client and can be forged. A missing or unparsable header
// falls back to r.RemoteAddr.
//
// trustedHeader must only be set when every request reaches the API through
// that proxy (the API port is not reachable directly); otherwise a client can
// forge the header and get a fresh budget on every request.
//
// IPv6 addresses are grouped by /64, the usual allocation to a single host,
// so a client cannot dodge the limit by rotating addresses within its prefix.
func clientIP(r *http.Request, trustedHeader string) string {
	if trustedHeader != "" {
		if values := r.Header.Values(trustedHeader); len(values) > 0 {
			last := values[len(values)-1]
			if i := strings.LastIndex(last, ","); i >= 0 {
				last = last[i+1:]
			}
			if ip := net.ParseIP(strings.TrimSpace(last)); ip != nil {
				return normalizeIP(ip)
			}
		}
	}
	host := peerHost(r)
	if ip := net.ParseIP(host); ip != nil {
		return normalizeIP(ip)
	}
	return host
}

// peerHost returns the host part of r.RemoteAddr, the TCP peer address.
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func normalizeIP(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
