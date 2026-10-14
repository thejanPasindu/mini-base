package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/platform/migrate"
)

// advisoryLockID serialises migrations across concurrently starting instances.
const advisoryLockID int64 = 0x6d696e6962617365 // "minibase"

const bootstrapSQL = `
CREATE SCHEMA IF NOT EXISTS minibase;
CREATE TABLE IF NOT EXISTS minibase.schema_migrations (
    module     text        NOT NULL,
    version    bigint      NOT NULL,
    name       text        NOT NULL,
    checksum   text        NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (module, version)
);`

// Migrator applies module migrations and records them in
// minibase.schema_migrations. Each migration runs in its own transaction.
type Migrator struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewMigrator returns a Migrator using pool.
func NewMigrator(pool *pgxpool.Pool, log *slog.Logger) *Migrator {
	return &Migrator{pool: pool, log: log}
}

// Up applies every migration in migs not yet recorded for module and returns
// how many were applied. It fails if an applied migration's SQL has changed:
// released migrations are immutable — add a new one instead.
func (m *Migrator) Up(ctx context.Context, module string, migs []migrate.Migration) (int, error) {
	if len(migs) == 0 {
		return 0, nil
	}

	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return 0, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Unlock even if ctx was cancelled; the session lock would otherwise
		// live as long as the pooled connection.
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockID); err != nil {
			m.log.ErrorContext(ctx, "release migration lock", slog.Any("error", err))
		}
	}()

	if _, err := conn.Exec(ctx, bootstrapSQL); err != nil {
		return 0, fmt.Errorf("bootstrap migrations table: %w", err)
	}

	applied, err := m.appliedChecksums(ctx, conn, module)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, mig := range migs {
		if sum, done := applied[mig.Version]; done {
			if sum != mig.Checksum {
				return count, fmt.Errorf("module %s: migration %04d_%s was modified after being applied", module, mig.Version, mig.Name)
			}
			continue
		}
		if err := m.apply(ctx, conn, module, mig); err != nil {
			return count, err
		}
		count++
		m.log.InfoContext(ctx, "migration applied",
			slog.String("module", module), slog.Int64("version", mig.Version), slog.String("name", mig.Name))
	}
	return count, nil
}

func (m *Migrator) appliedChecksums(ctx context.Context, conn *pgxpool.Conn, module string) (map[int64]string, error) {
	rows, err := conn.Query(ctx,
		"SELECT version, checksum FROM minibase.schema_migrations WHERE module = $1", module)
	if err != nil {
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]string)
	for rows.Next() {
		var (
			version int64
			sum     string
		)
		if err := rows.Scan(&version, &sum); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	return applied, nil
}

func (m *Migrator) apply(ctx context.Context, conn *pgxpool.Conn, module string, mig migrate.Migration) (err error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	// Exec without arguments uses the simple protocol, so a file may contain
	// several statements.
	if _, err = tx.Exec(ctx, mig.SQL); err != nil {
		return fmt.Errorf("module %s: migration %04d_%s: %w", module, mig.Version, mig.Name, err)
	}
	if _, err = tx.Exec(ctx,
		"INSERT INTO minibase.schema_migrations (module, version, name, checksum) VALUES ($1, $2, $3, $4)",
		module, mig.Version, mig.Name, mig.Checksum); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
