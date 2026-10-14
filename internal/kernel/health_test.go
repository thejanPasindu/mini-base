package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/platform/httpx"
)

func newHealthRouter(checks ...Check) *httpx.Router {
	r := httpx.NewRouter()
	(&healthHandler{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		checks:  checks,
		timeout: time.Second,
		info:    serverInfo{Name: "mini-base", Version: "test", Modules: []string{"example"}},
	}).routes(r)
	return r
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestLiveNeverTouchesDependencies(t *testing.T) {
	t.Parallel()

	r := newHealthRouter(Check{Name: "database", Fn: func(context.Context) error { return errors.New("down") }})
	if rec := get(r, "/health/live"); rec.Code != http.StatusOK {
		t.Fatalf("live = %d", rec.Code)
	}
}

func TestReadyReportsFailingChecksWithoutDetails(t *testing.T) {
	t.Parallel()

	r := newHealthRouter(
		Check{Name: "database", Fn: func(context.Context) error { return nil }},
		Check{Name: "storage", Fn: func(context.Context) error { return errors.New("secret-host:9000 refused") }},
	)
	rec := get(r, "/health/ready")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-host") {
		t.Fatal("readiness leaked error details")
	}
	var body readiness
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Checks["database"] != "ok" || body.Checks["storage"] != "failing" {
		t.Fatalf("checks = %v", body.Checks)
	}
}

func TestReadyOK(t *testing.T) {
	t.Parallel()

	r := newHealthRouter(Check{Name: "database", Fn: func(context.Context) error { return nil }})
	if rec := get(r, "/health/ready"); rec.Code != http.StatusOK {
		t.Fatalf("ready = %d", rec.Code)
	}
}

func TestRootReturnsInfoAndOnlyMatchesExactly(t *testing.T) {
	t.Parallel()

	r := newHealthRouter()
	rec := get(r, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"modules":["example"]`) {
		t.Fatalf("root = %d %s", rec.Code, rec.Body)
	}
	if rec := get(r, "/unknown"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", rec.Code)
	}
}
