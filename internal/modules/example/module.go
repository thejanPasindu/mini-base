// Package example is a minimal, complete module that doubles as the template
// for new modules. Copy its layout:
//
//	module.go          wiring only — implements kernel.Module
//	domain/            entities and rules, no I/O
//	port/              interfaces the service depends on
//	service/           use cases
//	adapter/postgres/  repository implementation
//	adapter/http/      handlers and DTOs
//	migrations/        NNNN_description.sql, embedded
//
// Enable it with MINIBASE_MODULES=example.
package example

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/mini-base/mini-base/internal/kernel"
	examplehttp "github.com/mini-base/mini-base/internal/modules/example/adapter/http"
	examplepg "github.com/mini-base/mini-base/internal/modules/example/adapter/postgres"
	"github.com/mini-base/mini-base/internal/modules/example/service"
	"github.com/mini-base/mini-base/internal/platform/httpx"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Module wires the example feature into the kernel.
type Module struct {
	kernel.BaseModule
	handler *examplehttp.Handler
}

// New returns an uninitialised module for registration.
func New() *Module { return &Module{} }

// Name implements kernel.Module.
func (*Module) Name() string { return "example" }

// Migrations implements kernel.Module.
func (*Module) Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the embed pattern above guarantees the directory exists
	}
	return sub
}

// Init implements kernel.Module.
func (m *Module) Init(_ context.Context, deps kernel.Deps) error {
	if err := deps.Config.Err(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	svc := service.New(examplepg.NewRepository(deps.DB), deps.Bus)
	m.handler = examplehttp.NewHandler(svc, deps.Logger)

	// Show cross-module events working: log every greeting created.
	deps.Bus.Subscribe(service.EventGreetingCreated, func(ctx context.Context, e kernel.Event) {
		deps.Logger.DebugContext(ctx, "event received", slog.String("event", e.Name))
	})
	return nil
}

// Routes implements kernel.Module.
func (m *Module) Routes(r *httpx.Router) { m.handler.Routes(r) }
