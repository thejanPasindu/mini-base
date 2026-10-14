package kernel

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mini-base/mini-base/internal/platform/httpx"
)

// Check is a named readiness probe.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// healthHandler serves the kernel's system endpoints.
type healthHandler struct {
	log     *slog.Logger
	checks  []Check
	timeout time.Duration
	info    serverInfo
}

type serverInfo struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Modules []string `json:"modules"`
}

type readiness struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func (h *healthHandler) routes(r *httpx.Router) {
	r.HandleFunc(http.MethodGet, "/{$}", h.root)
	r.HandleFunc(http.MethodGet, "/health/live", h.live)
	r.HandleFunc(http.MethodGet, "/health/ready", h.ready)
}

func (h *healthHandler) root(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, h.info)
}

// live reports that the process is up. It never touches dependencies, so an
// orchestrator does not restart the server because Postgres is briefly down.
func (h *healthHandler) live(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready runs all checks concurrently. Failure details are logged, not
// returned, so the endpoint can be public without leaking internals.
func (h *healthHandler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	res := readiness{Status: "ok", Checks: make(map[string]string, len(h.checks))}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range h.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := c.Fn(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Status = "unavailable"
				res.Checks[c.Name] = "failing"
				h.log.WarnContext(ctx, "readiness check failed", slog.String("check", c.Name), slog.Any("error", err))
				return
			}
			res.Checks[c.Name] = "ok"
		}()
	}
	wg.Wait()

	status := http.StatusOK
	if res.Status != "ok" {
		status = http.StatusServiceUnavailable
	}
	httpx.WriteJSON(w, status, res)
}
