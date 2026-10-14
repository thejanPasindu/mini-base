// Package ratelimit is an in-memory token-bucket limiter keyed by an arbitrary
// string (usually the client IP). It is per-process: a multi-node deployment
// needs a shared store behind the same interface.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/internal/platform/httpx"
)

// ErrRateLimited is returned when a key exceeds its budget.
var ErrRateLimited = errs.New(errs.KindRateLimited, "request.rate_limited", "Too many requests, slow down")

// maxKeys bounds memory use; idle buckets are swept when it is reached.
const maxKeys = 100_000

// Limiter allows `perMinute` events per key with bursts up to `burst`.
type Limiter struct {
	ratePerSec float64
	burst      float64
	now        func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a Limiter. perMinute <= 0 disables limiting.
func New(perMinute, burst int) *Limiter {
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		ratePerSec: float64(perMinute) / 60,
		burst:      float64(burst),
		now:        time.Now,
		buckets:    make(map[string]*bucket),
	}
}

// WithClock replaces the time source (tests).
func (l *Limiter) WithClock(now func() time.Time) *Limiter { l.now = now; return l }

// Allow consumes one token for key. When it returns false, retryAfter says
// how long until a token is available.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	if l.ratePerSec <= 0 {
		return true, 0
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= maxKeys {
			l.sweep(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.ratePerSec)
	b.last = now
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / l.ratePerSec * float64(time.Second))
		return false, wait
	}
	b.tokens--
	return true, 0
}

// sweep drops buckets that have refilled completely; they carry no state.
func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.ratePerSec >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// Middleware limits requests per client IP.
func (l *Limiter) Middleware() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, wait := l.Allow(ClientIP(r))
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
				httpx.WriteError(w, r, nil, ErrRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns the TCP peer address. Forwarded headers are ignored until
// trusted-proxy configuration exists, because they are trivially spoofed.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
