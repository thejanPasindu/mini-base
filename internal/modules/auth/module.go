// Package auth provides user accounts, sessions and access tokens.
//
// Settings (environment):
//
//	MINIBASE_AUTH_JWT_SECRET            required, ≥ 32 chars (openssl rand -hex 32)
//	MINIBASE_AUTH_ACCESS_TOKEN_TTL      default 15m
//	MINIBASE_AUTH_SESSION_TTL           default 720h (30 days)
//	MINIBASE_AUTH_PASSWORD_MIN_LENGTH   default 8
//	MINIBASE_AUTH_ALLOW_SIGNUP          default true
//	MINIBASE_AUTH_RATE_LIMIT_PER_MINUTE default 30 (per IP, sign-up and token endpoints; 0 = off)
//	MINIBASE_AUTH_MAX_CONCURRENT_HASHES default 4  (each Argon2id hash uses ~19 MiB)
package auth

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
	authhttp "github.com/mini-base/mini-base/internal/modules/auth/adapter/http"
	authpg "github.com/mini-base/mini-base/internal/modules/auth/adapter/postgres"
	"github.com/mini-base/mini-base/internal/modules/auth/adapter/token"
	"github.com/mini-base/mini-base/internal/modules/auth/service"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/internal/platform/jwt"
	"github.com/mini-base/mini-base/internal/platform/password"
	"github.com/mini-base/mini-base/internal/platform/ratelimit"
	"github.com/mini-base/mini-base/pkg/ports"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Housekeeping: sessions expired or revoked for longer than this are deleted.
const (
	purgeInterval  = time.Hour
	purgeRetention = 7 * 24 * time.Hour
)

// Module wires the auth feature into the kernel.
type Module struct {
	kernel.BaseModule
	svc     *service.Service
	handler *authhttp.Handler
	log     *slog.Logger

	stop chan struct{}
	wg   sync.WaitGroup
}

// New returns an uninitialised module for registration.
func New() *Module { return &Module{} }

// Name implements kernel.Module.
func (*Module) Name() string { return "auth" }

// Migrations implements kernel.Module.
func (*Module) Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

type config struct {
	jwtSecret         string
	accessTTL         time.Duration
	sessionTTL        time.Duration
	passwordMinLength int
	allowSignup       bool
	ratePerMinute     int
	maxHashes         int
}

func loadConfig(c *kernel.ModuleConfig) (config, error) {
	cfg := config{
		jwtSecret:         c.String("JWT_SECRET", ""),
		accessTTL:         c.Duration("ACCESS_TOKEN_TTL", 15*time.Minute),
		sessionTTL:        c.Duration("SESSION_TTL", 30*24*time.Hour),
		passwordMinLength: c.Int("PASSWORD_MIN_LENGTH", 8),
		allowSignup:       c.Bool("ALLOW_SIGNUP", true),
		ratePerMinute:     c.Int("RATE_LIMIT_PER_MINUTE", 30),
		maxHashes:         c.Int("MAX_CONCURRENT_HASHES", 4),
	}
	problems := []error{c.Err()}
	if len(cfg.jwtSecret) < jwt.MinSecretLength {
		problems = append(problems, fmt.Errorf("MINIBASE_AUTH_JWT_SECRET must be at least %d characters (generate with: openssl rand -hex 32)", jwt.MinSecretLength))
	}
	if cfg.accessTTL < time.Minute || cfg.accessTTL > 24*time.Hour {
		problems = append(problems, errors.New("MINIBASE_AUTH_ACCESS_TOKEN_TTL must be between 1m and 24h"))
	}
	if cfg.sessionTTL < cfg.accessTTL {
		problems = append(problems, errors.New("MINIBASE_AUTH_SESSION_TTL must be longer than the access token TTL"))
	}
	if cfg.passwordMinLength < 8 {
		problems = append(problems, errors.New("MINIBASE_AUTH_PASSWORD_MIN_LENGTH must be at least 8"))
	}
	return cfg, errors.Join(problems...)
}

// Init implements kernel.Module.
func (m *Module) Init(_ context.Context, deps kernel.Deps) error {
	if err := deps.RequireKeys(); err != nil {
		return err
	}
	cfg, err := loadConfig(deps.Config)
	if err != nil {
		return err
	}

	tokens, err := token.New([]byte(cfg.jwtSecret), cfg.accessTTL)
	if err != nil {
		return err
	}
	svc, err := service.New(
		authpg.NewUsers(deps.DB),
		authpg.NewSessions(deps.DB),
		password.NewArgon2id(password.DefaultParams, cfg.maxHashes),
		tokens,
		deps.Bus,
		service.Options{SessionTTL: cfg.sessionTTL, PasswordMinLength: cfg.passwordMinLength, AllowSignup: cfg.allowSignup},
	)
	if err != nil {
		return err
	}

	// Other modules (database, storage, realtime) verify tokens through this port.
	if err := deps.Services.Provide(ports.TokenVerifierService, ports.TokenVerifier(tokens)); err != nil {
		return err
	}
	// Operator tools (admin dashboard) manage users through this port.
	if err := deps.Services.Provide(ports.UserDirectoryService, ports.UserDirectory(directory{svc: svc})); err != nil {
		return err
	}

	m.svc = svc
	m.log = deps.Logger
	m.handler = authhttp.NewHandler(svc, deps.Auth, ratelimit.New(cfg.ratePerMinute, 10), deps.Logger)
	return nil
}

// Routes implements kernel.Module.
func (m *Module) Routes(r *httpx.Router) { m.handler.Routes(r) }

// Start implements kernel.Module: periodically purges old sessions.
func (m *Module) Start(ctx context.Context) error {
	m.stop = make(chan struct{})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(purgeInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-t.C:
				if n, err := m.svc.PurgeStaleSessions(ctx, purgeRetention); err != nil {
					m.log.WarnContext(ctx, "purge sessions", slog.Any("error", err))
				} else if n > 0 {
					m.log.InfoContext(ctx, "purged stale sessions", slog.Int64("count", n))
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
		return fmt.Errorf("auth: stop: %w", ctx.Err())
	}
}
