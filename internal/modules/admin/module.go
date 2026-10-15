// Package admin serves the web dashboard at /_/: overview, table editor and
// designer with RLS policy templates, CSV import/export, SQL editor with
// saved snippets, app users, API docs and keys, the audit log and admins.
//
// Dashboard operators are separate from application users. Create the first
// one with `minibase admin create --email you@example.com`, or set
// MINIBASE_ADMIN_EMAIL and MINIBASE_ADMIN_PASSWORD to create it on first start.
//
// Settings (environment):
//
//	MINIBASE_ADMIN_EMAIL / _PASSWORD  bootstrap the first admin if none exists
//	MINIBASE_ADMIN_SESSION_TTL        default 12h
//	MINIBASE_ADMIN_SQL_TIMEOUT        default 30s (per SQL editor script)
//	MINIBASE_ADMIN_SQL_MAX_ROWS       default 500 (rows shown per result)
//	MINIBASE_ADMIN_SECURE_COOKIE      default false; set true behind an HTTPS proxy
//	MINIBASE_ADMIN_LOGIN_RATE_LIMIT   default 10 attempts per minute per IP
//	MINIBASE_ADMIN_AUDIT_RETENTION    default 2160h (90 days); 0 keeps everything
package admin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/mini-base/mini-base/internal/kernel"
	adminhttp "github.com/mini-base/mini-base/internal/modules/admin/adapter/http"
	adminpg "github.com/mini-base/mini-base/internal/modules/admin/adapter/postgres"
	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/service"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/internal/platform/password"
	"github.com/mini-base/mini-base/internal/platform/ratelimit"
	"github.com/mini-base/mini-base/pkg/ports"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed web
var web embed.FS

// Module wires the dashboard into the kernel.
type Module struct {
	kernel.BaseModule
	accounts *service.Accounts
	audit    *service.Audit
	retain   time.Duration
	handler  *adminhttp.Handler
	log      *slog.Logger
	stop     chan struct{}
	wg       sync.WaitGroup
}

// New returns an uninitialised module for registration.
func New() *Module { return &Module{} }

// Name implements kernel.Module.
func (*Module) Name() string { return "admin" }

// OptionalDependsOn implements kernel.OptionalDependent: with auth enabled,
// the Users page appears.
func (*Module) OptionalDependsOn() []string { return []string{"auth", "database"} }

// Migrations implements kernel.Module.
func (*Module) Migrations() fs.FS { return sub(migrations, "migrations") }

type config struct {
	bootstrapEmail, bootstrapPassword string
	sessionTTL, sqlTimeout            time.Duration
	auditRetention                    time.Duration
	sqlMaxRows, loginRate             int
	secureCookie                      bool
}

func loadConfig(c *kernel.ModuleConfig) (config, error) {
	cfg := config{
		bootstrapEmail:    c.String("EMAIL", ""),
		bootstrapPassword: c.String("PASSWORD", ""),
		sessionTTL:        c.Duration("SESSION_TTL", 12*time.Hour),
		sqlTimeout:        c.Duration("SQL_TIMEOUT", 30*time.Second),
		sqlMaxRows:        c.Int("SQL_MAX_ROWS", 500),
		loginRate:         c.Int("LOGIN_RATE_LIMIT", 10),
		secureCookie:      c.Bool("SECURE_COOKIE", false),
		auditRetention:    c.Duration("AUDIT_RETENTION", 90*24*time.Hour),
	}
	problems := []error{c.Err()}
	if cfg.sessionTTL < 5*time.Minute || cfg.sessionTTL > 30*24*time.Hour {
		problems = append(problems, errors.New("MINIBASE_ADMIN_SESSION_TTL must be between 5m and 720h"))
	}
	if cfg.sqlMaxRows < 1 || cfg.sqlMaxRows > 10_000 {
		problems = append(problems, errors.New("MINIBASE_ADMIN_SQL_MAX_ROWS must be between 1 and 10000"))
	}
	if cfg.auditRetention < 0 || (cfg.auditRetention > 0 && cfg.auditRetention < time.Hour) {
		problems = append(problems, errors.New("MINIBASE_ADMIN_AUDIT_RETENTION must be 0 (keep everything) or at least 1h"))
	}
	if (cfg.bootstrapEmail == "") != (cfg.bootstrapPassword == "") {
		problems = append(problems, errors.New("set both MINIBASE_ADMIN_EMAIL and MINIBASE_ADMIN_PASSWORD, or neither"))
	}
	return cfg, errors.Join(problems...)
}

// Init implements kernel.Module.
func (m *Module) Init(ctx context.Context, deps kernel.Deps) error {
	cfg, err := loadConfig(deps.Config)
	if err != nil {
		return err
	}
	m.log = deps.Logger
	m.retain = cfg.auditRetention
	m.audit = service.NewAudit(adminpg.NewAuditLog(deps.DB), deps.Logger)

	accounts, err := newAccounts(deps, cfg.sessionTTL)
	if err != nil {
		return err
	}
	m.accounts = accounts.WithAudit(m.audit)
	if err := m.bootstrap(service.WithActor(ctx, domain.Actor{Email: "bootstrap"}), cfg); err != nil {
		return err
	}

	catalog := adminpg.NewCatalog(deps.DB)
	users := func() ports.UserDirectory {
		d, ok, _ := kernel.Lookup[ports.UserDirectory](deps.Services, ports.UserDirectoryService)
		if !ok {
			return nil
		}
		return d
	}
	console := service.NewConsole(service.ConsoleDeps{
		Catalog:    catalog,
		Data:       catalog,
		SQL:        adminpg.NewSQLRunner(deps.DB, cfg.sqlTimeout),
		Events:     deps.Bus,
		Snippets:   adminpg.NewSnippets(deps.DB),
		Audit:      m.audit,
		Users:      users,
		Settings:   func() []service.Setting { return settings(deps.Settings) },
		Info:       service.Info{Version: deps.Info.Version, Env: string(deps.Info.Env), Modules: deps.Info.Modules},
		MaxSQLRows: cfg.sqlMaxRows,
	})

	m.handler = adminhttp.NewHandler(accounts, console, sub(web, "web"), ratelimit.New(cfg.loginRate, 5), deps.Logger,
		adminhttp.Options{SecureCookie: cfg.secureCookie})
	return nil
}

// settings converts the kernel's record of configuration for the console.
func settings(s *kernel.Settings) []service.Setting {
	list := s.List()
	out := make([]service.Setting, len(list))
	for i, st := range list {
		out[i] = service.Setting(st)
	}
	return out
}

func newAccounts(deps kernel.Deps, ttl time.Duration) (*service.Accounts, error) {
	return service.NewAccounts(adminpg.NewAccounts(deps.DB), adminpg.NewSessions(deps.DB),
		password.NewArgon2id(password.DefaultParams, 2), ttl)
}

// bootstrap creates the first admin from the environment, or explains how
// to create one.
func (m *Module) bootstrap(ctx context.Context, cfg config) error {
	n, err := m.accounts.Count(ctx)
	if err != nil {
		return err
	}
	switch {
	case n > 0:
		return nil
	case cfg.bootstrapEmail != "":
		if _, err := m.accounts.Create(ctx, cfg.bootstrapEmail, cfg.bootstrapPassword); err != nil {
			return fmt.Errorf("create admin from MINIBASE_ADMIN_EMAIL: %w", err)
		}
		m.log.InfoContext(ctx, "created first admin account; you can remove MINIBASE_ADMIN_PASSWORD from the environment now",
			slog.String("email", cfg.bootstrapEmail))
	default:
		m.log.WarnContext(ctx, "no admin accounts yet; create one with: minibase admin create --email you@example.com "+
			"(in Docker: docker compose exec minibase /minibase admin create --email you@example.com)")
	}
	return nil
}

// Routes implements kernel.Module.
func (m *Module) Routes(r *httpx.Router) { m.handler.Routes(r) }

// Start implements kernel.Module: hourly cleanup of expired sessions and
// old audit entries.
func (m *Module) Start(ctx context.Context) error {
	m.stop = make(chan struct{})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-t.C:
				if _, err := m.accounts.PurgeExpired(ctx); err != nil {
					m.log.WarnContext(ctx, "purge admin sessions", slog.Any("error", err))
				}
				if _, err := m.audit.Purge(ctx, m.retain); err != nil {
					m.log.WarnContext(ctx, "purge audit log", slog.Any("error", err))
				}
			}
		}
	}()
	return nil
}

// Stop implements kernel.Module.
func (m *Module) Stop(ctx context.Context) error {
	if m.stop == nil {
		return nil
	}
	close(m.stop)
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("admin: stop: %w", ctx.Err())
	}
}

func sub(f embed.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err) // the embed patterns above guarantee the directories exist
	}
	return s
}
