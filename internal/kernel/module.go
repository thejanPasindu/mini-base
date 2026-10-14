// Package kernel is the always-on core of mini-base: configuration, the module
// registry and lifecycle, the event bus, the HTTP gateway and health checks.
//
// Features live in modules (internal/modules/*). The kernel never imports a
// module; modules are registered by the composition root (cmd/minibase).
package kernel

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/platform/authn"
	"github.com/mini-base/mini-base/internal/platform/httpx"
)

// Module is the contract every feature module implements.
//
// Lifecycle, in dependency order: Migrations → Init → Routes → Start.
// Shutdown calls Stop in reverse order.
type Module interface {
	// Name is the unique lowercase identifier used in MINIBASE_MODULES.
	Name() string

	// DependsOn lists modules that must be enabled and initialised first.
	// A missing dependency is a startup error.
	DependsOn() []string

	// Init wires the module. It must not start background work.
	Init(ctx context.Context, deps Deps) error

	// Routes registers the module's HTTP handlers, normally on a group such
	// as r.Group("/auth/v1").
	Routes(r *httpx.Router)

	// Migrations returns the module's SQL migrations (files named
	// NNNN_description.sql at the root of the FS), or nil.
	Migrations() fs.FS

	// Start launches background work and must return quickly. ctx is
	// cancelled when shutdown begins.
	Start(ctx context.Context) error

	// Stop releases resources and must finish before ctx's deadline.
	Stop(ctx context.Context) error
}

// OptionalDependent is implemented by modules that integrate with other
// modules when present (e.g. database uses auth for per-user RLS) but work
// without them. Enabled optional dependencies are initialised first.
type OptionalDependent interface {
	OptionalDependsOn() []string
}

// ReadinessChecker is implemented by modules that contribute to /health/ready.
type ReadinessChecker interface {
	Ready(ctx context.Context) error
}

// Deps is everything the kernel provides to a module. Modules must not reach
// for globals or for other modules' packages; they get what they need here.
type Deps struct {
	Config   *ModuleConfig // this module's MINIBASE_<NAME>_* settings
	DB       *pgxpool.Pool // shared connection pool
	Bus      EventBus      // cross-module notifications
	Logger   *slog.Logger  // pre-tagged with module=<name>
	Services *Services     // ports published by other modules
	Env      Environment   // development or production

	// Auth authenticates API requests (API key + optional access token).
	// Wrap API routes with Auth.Middleware(); call RequireKeys in Init.
	Auth *authn.Authenticator

	// Info describes the running server (for status pages).
	Info RuntimeInfo
}

// RuntimeInfo describes the running server.
type RuntimeInfo struct {
	Version string
	Env     Environment
	Modules []string // enabled modules, in start order
}

// RequireKeys returns an error explaining how to configure API keys when
// they are missing. Modules that expose an API call it from Init.
func (d Deps) RequireKeys() error {
	if d.Auth == nil || !d.Auth.Configured() {
		return errors.New("MINIBASE_ANON_KEY and MINIBASE_SERVICE_KEY must be set; generate each with: openssl rand -hex 32")
	}
	return nil
}

// BaseModule provides no-op defaults so modules only implement what they use.
// Embed it and implement Name and Init.
type BaseModule struct{}

// DependsOn returns no dependencies.
func (BaseModule) DependsOn() []string { return nil }

// Routes registers nothing.
func (BaseModule) Routes(*httpx.Router) {}

// Migrations returns nil (no schema).
func (BaseModule) Migrations() fs.FS { return nil }

// Start does nothing.
func (BaseModule) Start(context.Context) error { return nil }

// Stop does nothing.
func (BaseModule) Stop(context.Context) error { return nil }
