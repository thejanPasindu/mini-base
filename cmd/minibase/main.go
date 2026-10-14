// Command minibase is the mini-base server and CLI.
//
//	minibase serve        start the server (default)
//	minibase migrate      apply migrations for enabled modules and exit
//	minibase modules      list modules compiled into this binary
//	minibase healthcheck  probe /health/live (for Docker HEALTHCHECK)
//	minibase version      print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mini-base/mini-base/internal/kernel"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run is main without process globals, so it can be tested.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && (!strings.HasPrefix(args[0], "-") || args[0] == "-h" || args[0] == "--help") {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, args, getenv, stderr)
	case "migrate":
		err = migrateCmd(ctx, args, getenv, stderr)
	case "modules":
		for _, name := range registry().Names() {
			fmt.Fprintln(stdout, name)
		}
	case "healthcheck":
		err = healthcheck(ctx, getenv)
	case "version":
		fmt.Fprintln(stdout, version)
	case "help", "-h", "--help":
		usage(stdout)
	default:
		usage(stderr)
		return 2
	}

	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "minibase %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: minibase <command> [flags]

Commands:
  serve        Start the server (default)
  migrate      Apply migrations for enabled modules and exit
  modules      List modules compiled into this binary
  healthcheck  Exit 0 if the local server is live
  version      Print the version

Configuration is read from MINIBASE_* environment variables.
See docs/architecture.md.
`)
}

// loadConfig parses flags shared by serve and migrate. --modules overrides
// MINIBASE_MODULES.
func loadConfig(name string, args []string, getenv func(string) string, stderr io.Writer) (kernel.Config, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	modules := fs.String("modules", "", "comma-separated modules to enable (overrides MINIBASE_MODULES)")
	if err := fs.Parse(args); err != nil {
		return kernel.Config{}, err
	}

	env := getenv
	if *modules != "" {
		env = func(k string) string {
			if k == kernel.EnvPrefix+"MODULES" {
				return *modules
			}
			return getenv(k)
		}
	}
	return kernel.LoadConfig(env)
}

func serve(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	cfg, err := loadConfig("serve", args, getenv, stderr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := kernel.NewLogger(cfg.Log, stderr)
	return kernel.New(cfg, registry(), log, version, getenv).Run(ctx)
}

func migrateCmd(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	cfg, err := loadConfig("migrate", args, getenv, stderr)
	if err != nil {
		return err
	}
	log := kernel.NewLogger(cfg.Log, stderr)
	return kernel.New(cfg, registry(), log, version, getenv).Migrate(ctx)
}

// healthcheck lets distroless images (no curl) use a Docker HEALTHCHECK.
func healthcheck(ctx context.Context, getenv func(string) string) error {
	addr := getenv(kernel.EnvPrefix + "HTTP_ADDR")
	if addr == "" {
		addr = ":8090"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse %sHTTP_ADDR: %w", kernel.EnvPrefix, err)
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/health/live", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
