// Package postgres owns the PostgreSQL connection pool and the migration
// store. It is the only kernel-level package that talks to pgx directly.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options configures Connect.
type Options struct {
	URL            string
	MaxConns       int32
	ConnectTimeout time.Duration // total time to keep retrying
	AppName        string
}

// Connect opens a pool and waits until Postgres answers, retrying with
// exponential back-off. This lets `docker compose up` start mini-base before
// Postgres has finished booting.
func Connect(ctx context.Context, opts Options, log *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(opts.URL)
	if err != nil {
		// Do not wrap err: it may echo the URL, which contains the password.
		return nil, errors.New("parse database URL: invalid connection string")
	}
	cfg.MaxConns = opts.MaxConns
	if opts.AppName != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = opts.AppName
	}

	ctx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	backoff := 250 * time.Millisecond
	for attempt := 1; ; attempt++ {
		pingErr := pool.Ping(ctx)
		if pingErr == nil {
			log.InfoContext(ctx, "database connected",
				slog.String("host", cfg.ConnConfig.Host), slog.String("database", cfg.ConnConfig.Database))
			return pool, nil
		}
		// Retrying cannot fix wrong credentials or a missing database.
		if hint := permanentFailure(pingErr); hint != "" {
			pool.Close()
			return nil, fmt.Errorf("%s: %w", hint, pingErr)
		}
		log.WarnContext(ctx, "database not ready, retrying",
			slog.Int("attempt", attempt), slog.Duration("backoff", backoff), slog.Any("error", pingErr))

		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("database unreachable after %s: %w", opts.ConnectTimeout, pingErr)
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

// permanentFailure returns a hint for connection errors that retrying will
// not fix, or "" for errors worth retrying (server still starting, network).
func permanentFailure(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	switch pgErr.Code {
	case "28P01", "28000": // invalid_password, invalid_authorization_specification
		return "database rejected the credentials. With the bundled Postgres, the password is fixed " +
			"when its data volume is first created, so changing POSTGRES_PASSWORD later has no effect. " +
			"See docs/getting-started.md, Troubleshooting"
	case "3D000": // invalid_catalog_name
		return "database does not exist; check the database name in MINIBASE_DATABASE_URL"
	}
	return ""
}
