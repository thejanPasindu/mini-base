//go:build integration

// Integration tests need a real PostgreSQL. Run with:
//
//	make test-integration
//
// which starts Postgres via Docker Compose and sets MINIBASE_TEST_DATABASE_URL.
package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/kernel"
	"github.com/mini-base/mini-base/internal/modules/example"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("MINIBASE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MINIBASE_TEST_DATABASE_URL not set")
	}
	return url
}

// startKernel runs a kernel with the example module on an ephemeral port and
// returns its base URL. The kernel stops when the test ends.
func startKernel(t *testing.T, modules ...string) string {
	t.Helper()

	cfg, err := kernel.LoadConfig(func(k string) string {
		switch k {
		case "MINIBASE_DATABASE_URL":
			return testDatabaseURL(t)
		case "MINIBASE_MODULES":
			return strings.Join(modules, ",")
		case "MINIBASE_HTTP_SHUTDOWN_TIMEOUT":
			return "2s"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}

	reg := kernel.NewRegistry()
	reg.MustRegister(example.New())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan net.Addr, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	k := kernel.New(cfg, reg, log, "test", func(string) string { return "" },
		kernel.WithListener(ln), kernel.WithReadyNotify(ready))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("kernel shutdown: %v", err)
		}
	})

	select {
	case addr := <-ready:
		return "http://" + addr.String()
	case err := <-done:
		t.Fatalf("kernel exited during startup: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("kernel did not start")
	}
	return ""
}

func TestKernelServesHealthAndExampleModule(t *testing.T) {
	base := startKernel(t, "example")

	resp, err := http.Get(base + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready = %d", resp.StatusCode)
	}

	resp, err = http.Post(base+"/example/v1/greetings", "application/json", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}

	resp, err = http.Get(base + "/example/v1/greetings?limit=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct{ Message string } `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].Message != "hello" {
		t.Fatalf("list = %+v", body)
	}
}

func TestDisabledModuleHasNoRoutes(t *testing.T) {
	base := startKernel(t) // kernel only

	resp, err := http.Get(base + "/example/v1/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled module route = %d, want 404", resp.StatusCode)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	for range 2 {
		cfg, err := kernel.LoadConfig(func(k string) string {
			switch k {
			case "MINIBASE_DATABASE_URL":
				return testDatabaseURL(t)
			case "MINIBASE_MODULES":
				return "example"
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		reg := kernel.NewRegistry()
		reg.MustRegister(example.New())
		k := kernel.New(cfg, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", func(string) string { return "" })
		if err := k.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
