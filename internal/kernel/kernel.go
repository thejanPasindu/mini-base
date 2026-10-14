package kernel

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/platform/authn"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/internal/platform/migrate"
	"github.com/mini-base/mini-base/internal/platform/postgres"
	"github.com/mini-base/mini-base/pkg/ports"
)

// coreMigrations are the kernel's own migrations (shared database roles).
// They run before any module's, recorded under the name "kernel".
//
//go:embed migrations/*.sql
var coreMigrations embed.FS

// Kernel wires configuration, modules, database and HTTP server together
// and owns the process lifecycle.
type Kernel struct {
	cfg      Config
	registry *Registry
	log      *slog.Logger
	version  string
	getenv   func(string) string
	listener net.Listener
	ready    chan<- net.Addr
}

// Option customises a Kernel.
type Option func(*Kernel)

// WithListener serves on an existing listener instead of cfg.HTTP.Addr
// (used by tests to bind an ephemeral port).
func WithListener(l net.Listener) Option { return func(k *Kernel) { k.listener = l } }

// WithReadyNotify receives the bound address once the server accepts requests.
func WithReadyNotify(ch chan<- net.Addr) Option { return func(k *Kernel) { k.ready = ch } }

// New returns a Kernel. getenv supplies module configuration (os.Getenv in
// production).
func New(cfg Config, reg *Registry, log *slog.Logger, version string, getenv func(string) string, opts ...Option) *Kernel {
	k := &Kernel{cfg: cfg, registry: reg, log: log, version: version, getenv: getenv}
	for _, opt := range opts {
		opt(k)
	}
	return k
}

// NewLogger builds the process logger from LogConfig.
func NewLogger(cfg LogConfig, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// Migrate connects to the database and applies migrations for the enabled
// modules, then returns. Used by `minibase migrate`.
func (k *Kernel) Migrate(ctx context.Context) error {
	mods, err := k.registry.Resolve(k.cfg.Modules)
	if err != nil {
		return err
	}
	pool, err := k.connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	return k.migrate(ctx, pool, mods)
}

// WithDB connects, applies migrations for the enabled modules and calls fn
// with the pool. It is for CLI commands such as `minibase admin create`.
func (k *Kernel) WithDB(ctx context.Context, fn func(ctx context.Context, db *pgxpool.Pool) error) error {
	mods, err := k.registry.Resolve(k.cfg.Modules)
	if err != nil {
		return err
	}
	pool, err := k.connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := k.migrate(ctx, pool, mods); err != nil {
		return err
	}
	return fn(ctx, pool)
}

func moduleNames(mods []Module) []string {
	names := make([]string, len(mods))
	for i, m := range mods {
		names[i] = m.Name()
	}
	return names
}

// Run starts everything and blocks until ctx is cancelled (SIGTERM) or the
// server fails, then shuts down gracefully.
func (k *Kernel) Run(ctx context.Context) error {
	mods, err := k.registry.Resolve(k.cfg.Modules)
	if err != nil {
		return err
	}
	k.warnUnsafeSettings()

	pool, err := k.connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := k.migrate(ctx, pool, mods); err != nil {
		return err
	}

	bus := NewInProcBus(k.log.With(slog.String("component", "eventbus")), 0)
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), k.cfg.HTTP.ShutdownTimeout)
		defer cancel()
		if err := bus.Close(sctx); err != nil {
			k.log.Warn("event bus shutdown", slog.Any("error", err))
		}
	}()

	if err := k.initModules(ctx, mods, pool, bus); err != nil {
		return err
	}
	handler := k.buildHandler(mods, pool)

	// Background work gets its own context: it must outlive request
	// handling during drain and is cancelled only when shutdown begins.
	runCtx, stopBackground := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBackground()
	started, err := k.startModules(runCtx, mods)
	defer k.stopModules(started)
	if err != nil {
		return err
	}

	return k.serve(ctx, handler, stopBackground)
}

func (k *Kernel) connect(ctx context.Context) (*pgxpool.Pool, error) {
	return postgres.Connect(ctx, postgres.Options{
		URL:            k.cfg.Database.URL,
		MaxConns:       k.cfg.Database.MaxConns,
		ConnectTimeout: k.cfg.Database.ConnectTimeout,
		AppName:        "minibase",
	}, k.log)
}

func (k *Kernel) migrate(ctx context.Context, pool *pgxpool.Pool, mods []Module) error {
	migrator := postgres.NewMigrator(pool, k.log)

	core, err := fs.Sub(coreMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("kernel migrations: %w", err)
	}
	coreMigs, err := migrate.Load(core)
	if err != nil {
		return fmt.Errorf("kernel migrations: %w", err)
	}
	if _, err := migrator.Up(ctx, "kernel", coreMigs); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	for _, m := range mods {
		migs, err := migrate.Load(m.Migrations())
		if err != nil {
			return fmt.Errorf("module %s: %w", m.Name(), err)
		}
		if _, err := migrator.Up(ctx, m.Name(), migs); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (k *Kernel) initModules(ctx context.Context, mods []Module, pool *pgxpool.Pool, bus EventBus) error {
	services := NewServices()

	// The token verifier is provided by the auth module during its Init, so
	// it is looked up per request rather than now.
	auth := authn.New(authn.Keys{Anon: k.cfg.Keys.Anon, Service: k.cfg.Keys.Service}, func() ports.TokenVerifier {
		v, ok, _ := Lookup[ports.TokenVerifier](services, ports.TokenVerifierService)
		if !ok {
			return nil
		}
		return v
	})

	info := RuntimeInfo{Version: k.version, Env: k.cfg.Env, Modules: moduleNames(mods)}
	for _, m := range mods {
		deps := Deps{
			Config:   NewModuleConfig(m.Name(), k.getenv),
			DB:       pool,
			Bus:      bus,
			Logger:   k.log.With(slog.String("module", m.Name())),
			Services: services,
			Env:      k.cfg.Env,
			Auth:     auth,
			Info:     info,
		}
		if err := m.Init(ctx, deps); err != nil {
			return fmt.Errorf("init module %s: %w", m.Name(), err)
		}
	}
	return nil
}

func (k *Kernel) buildHandler(mods []Module, pool *pgxpool.Pool) http.Handler {
	names := make([]string, 0, len(mods))
	checks := []Check{{Name: "postgres", Fn: pool.Ping}}
	for _, m := range mods {
		names = append(names, m.Name())
		if rc, ok := m.(ReadinessChecker); ok {
			checks = append(checks, Check{Name: m.Name(), Fn: rc.Ready})
		}
	}

	root := httpx.NewRouter()
	(&healthHandler{
		log:     k.log,
		checks:  checks,
		timeout: 3 * time.Second,
		info:    serverInfo{Name: "mini-base", Version: k.version, Modules: names},
	}).routes(root)
	for _, m := range mods {
		m.Routes(root)
	}

	return httpx.Chain(root,
		httpx.WithRequestID(),
		httpx.WithLogging(k.log),
		httpx.WithRecovery(k.log),
		httpx.WithCORS(httpx.CORSOptions{
			AllowedOrigins: k.cfg.HTTP.CORSOrigins,
			AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Authorization", "Content-Type", "Prefer", "X-Request-Id", "apikey"},
			ExposedHeaders: []string{"X-Request-Id", "Content-Range"},
			MaxAge:         10 * time.Minute,
		}),
		httpx.WithMaxBodySize(k.cfg.HTTP.MaxBodyBytes),
	)
}

func (k *Kernel) startModules(ctx context.Context, mods []Module) ([]Module, error) {
	started := make([]Module, 0, len(mods))
	for _, m := range mods {
		if err := m.Start(ctx); err != nil {
			return started, fmt.Errorf("start module %s: %w", m.Name(), err)
		}
		started = append(started, m)
	}
	return started, nil
}

// stopModules stops modules in reverse start order, sharing one deadline.
func (k *Kernel) stopModules(started []Module) {
	ctx, cancel := context.WithTimeout(context.Background(), k.cfg.HTTP.ShutdownTimeout)
	defer cancel()
	for i := len(started) - 1; i >= 0; i-- {
		m := started[i]
		if err := m.Stop(ctx); err != nil {
			k.log.Error("stop module", slog.String("module", m.Name()), slog.Any("error", err))
		}
	}
}

func (k *Kernel) serve(ctx context.Context, handler http.Handler, stopBackground context.CancelFunc) error {
	ln := k.listener
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", k.cfg.HTTP.Addr); err != nil {
			return fmt.Errorf("listen on %s: %w", k.cfg.HTTP.Addr, err)
		}
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: k.cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       k.cfg.HTTP.ReadTimeout,
		WriteTimeout:      k.cfg.HTTP.WriteTimeout,
		IdleTimeout:       k.cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(k.log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	k.log.Info("mini-base started",
		slog.String("addr", ln.Addr().String()),
		slog.String("version", k.version),
		slog.String("env", string(k.cfg.Env)),
		slog.Any("modules", k.cfg.Modules))
	if k.ready != nil {
		k.ready <- ln.Addr()
	}

	select {
	case err := <-errCh:
		stopBackground()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	k.log.Info("shutting down", slog.Duration("timeout", k.cfg.HTTP.ShutdownTimeout))
	sctx, cancel := context.WithTimeout(context.Background(), k.cfg.HTTP.ShutdownTimeout)
	defer cancel()
	err := srv.Shutdown(sctx) // stop accepting, drain in-flight requests
	stopBackground()
	if err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

func (k *Kernel) warnUnsafeSettings() {
	if k.cfg.Env != EnvProduction {
		return
	}
	for _, o := range k.cfg.HTTP.CORSOrigins {
		if o == "*" {
			k.log.Warn("CORS allows any origin in production; set MINIBASE_CORS_ALLOWED_ORIGINS")
		}
	}
}
