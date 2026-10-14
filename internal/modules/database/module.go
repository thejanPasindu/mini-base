// Package database exposes Postgres tables as a REST API secured by
// row-level security.
//
// Settings (environment):
//
//	MINIBASE_DATABASE_SCHEMAS                   default public (comma-separated, lookup order)
//	MINIBASE_DATABASE_MAX_ROWS                  default 1000 (cap on rows per read)
//	MINIBASE_DATABASE_STATEMENT_TIMEOUT         default 8s
//	MINIBASE_DATABASE_ALLOW_TABLES_WITHOUT_RLS  default false (unsafe when true)
package database

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"time"

	"github.com/mini-base/mini-base/internal/kernel"
	dbhttp "github.com/mini-base/mini-base/internal/modules/database/adapter/http"
	dbpg "github.com/mini-base/mini-base/internal/modules/database/adapter/postgres"
	"github.com/mini-base/mini-base/internal/modules/database/service"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/pkg/ports"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Schemas owned by mini-base or Postgres that must never be exposed.
var reservedSchemas = []string{"auth", "admin", "minibase", "storage", "realtime", "hooks", "example", "pg_catalog", "information_schema", "pg_toast"}

// Module wires the database feature into the kernel.
type Module struct {
	kernel.BaseModule
	svc     *service.Service
	handler *dbhttp.Handler
}

// New returns an uninitialised module for registration.
func New() *Module { return &Module{} }

// Name implements kernel.Module.
func (*Module) Name() string { return "database" }

// OptionalDependsOn implements kernel.OptionalDependent: with auth enabled,
// signed-in users get per-user RLS; without it only API keys work.
func (*Module) OptionalDependsOn() []string { return []string{"auth"} }

// Migrations implements kernel.Module.
func (*Module) Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

func loadOptions(c *kernel.ModuleConfig) (service.Options, time.Duration, error) {
	opts := service.Options{
		Schemas:    c.List("SCHEMAS", "public"),
		MaxRows:    c.Int("MAX_ROWS", 1000),
		AllowNoRLS: c.Bool("ALLOW_TABLES_WITHOUT_RLS", false),
	}
	timeout := c.Duration("STATEMENT_TIMEOUT", 8*time.Second)

	problems := []error{c.Err()}
	if len(opts.Schemas) == 0 {
		problems = append(problems, errors.New("MINIBASE_DATABASE_SCHEMAS must list at least one schema"))
	}
	for _, s := range opts.Schemas {
		if slices.Contains(reservedSchemas, s) {
			problems = append(problems, fmt.Errorf("MINIBASE_DATABASE_SCHEMAS must not include the internal schema %q", s))
		}
	}
	if opts.MaxRows < 1 || opts.MaxRows > 100_000 {
		problems = append(problems, errors.New("MINIBASE_DATABASE_MAX_ROWS must be between 1 and 100000"))
	}
	if timeout < 100*time.Millisecond {
		problems = append(problems, errors.New("MINIBASE_DATABASE_STATEMENT_TIMEOUT must be at least 100ms"))
	}
	return opts, timeout, errors.Join(problems...)
}

// Init implements kernel.Module.
func (m *Module) Init(ctx context.Context, deps kernel.Deps) error {
	if err := deps.RequireKeys(); err != nil {
		return err
	}
	opts, timeout, err := loadOptions(deps.Config)
	if err != nil {
		return err
	}
	if opts.AllowNoRLS {
		deps.Logger.Warn("MINIBASE_DATABASE_ALLOW_TABLES_WITHOUT_RLS=true: tables without row level security are readable and writable with the anon key")
	}

	m.svc = service.New(dbpg.NewSchemaLoader(deps.DB), dbpg.NewExecutor(deps.DB, timeout), opts)
	if err := m.svc.Reload(ctx); err != nil {
		return err
	}
	m.handler = dbhttp.NewHandler(m.svc, deps.Auth, deps.Logger)

	// The admin dashboard announces schema edits; refresh the cache at once.
	deps.Bus.Subscribe(ports.EventSchemaChanged, func(ctx context.Context, _ kernel.Event) {
		if err := m.svc.Invalidate(ctx); err != nil {
			deps.Logger.WarnContext(ctx, "reload schema after change", slog.Any("error", err))
		}
	})
	return nil
}

// Routes implements kernel.Module.
func (m *Module) Routes(r *httpx.Router) { m.handler.Routes(r) }

// Ready implements kernel.ReadinessChecker.
func (m *Module) Ready(ctx context.Context) error { return m.svc.Ready(ctx) }
