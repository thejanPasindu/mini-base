package ratelimit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/platform/ratelimit"
)

func TestBurstThenRefill(t *testing.T) {
	t.Parallel()

	now := time.Unix(0, 0)
	l := ratelimit.New(60, 3).WithClock(func() time.Time { return now }) // 1 token/sec

	for i := range 3 {
		if ok, _ := l.Allow("ip"); !ok {
			t.Fatalf("request %d within burst rejected", i)
		}
	}
	ok, wait := l.Allow("ip")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("4th request: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("other-ip"); !ok {
		t.Fatal("keys must be independent")
	}

	now = now.Add(time.Second)
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("token should refill after 1s")
	}
}

func TestDisabled(t *testing.T) {
	t.Parallel()

	l := ratelimit.New(0, 1)
	for range 100 {
		if ok, _ := l.Allow("ip"); !ok {
			t.Fatal("disabled limiter rejected")
		}
	}
}

func TestMiddlewareSets429AndRetryAfter(t *testing.T) {
	t.Parallel()

	h := ratelimit.New(1, 1).Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	do := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "203.0.113.7:5555"
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(); rec.Code != 200 {
		t.Fatalf("first = %d", rec.Code)
	}
	rec := do()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second = %d, Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}
