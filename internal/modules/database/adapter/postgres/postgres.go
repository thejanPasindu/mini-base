// Package postgres implements schema loading and role-scoped execution.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/modules/database/domain"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/pkg/ports"
)

// Postgres roles for request principals. Must match the kernel's roles migration.
const (
	roleAnon          = "minibase_anon"
	roleAuthenticated = "minibase_authenticated"
)

// SchemaLoader implements port.SchemaLoader.
type SchemaLoader struct{ db *pgxpool.Pool }

// NewSchemaLoader returns a SchemaLoader.
func NewSchemaLoader(db *pgxpool.Pool) *SchemaLoader { return &SchemaLoader{db: db} }

const schemaQuery = `
SELECT n.nspname, c.relname, c.relkind::text, c.relrowsecurity,
       coalesce(c.reloptions, '{}'::text[]),
       a.attname, format_type(a.atttypid, a.atttypmod)
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE n.nspname = ANY($1::text[]) AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
ORDER BY n.nspname, c.relname, a.attnum`

// Load implements port.SchemaLoader.
func (l *SchemaLoader) Load(ctx context.Context, schemas []string) (*domain.Schema, error) {
	rows, err := l.db.Query(ctx, schemaQuery, schemas)
	if err != nil {
		return nil, fmt.Errorf("query catalog: %w", err)
	}
	defer rows.Close()

	type key struct{ schema, name string }
	type rel struct {
		kind      domain.Kind
		rls, sinv bool
		cols      []domain.Column
	}
	rels := make(map[key]*rel)
	var order []key

	for rows.Next() {
		var (
			schema, name, kind, col, typ string
			rls                          bool
			options                      []string
		)
		if err := rows.Scan(&schema, &name, &kind, &rls, &options, &col, &typ); err != nil {
			return nil, fmt.Errorf("scan catalog: %w", err)
		}
		k := key{schema, name}
		r, ok := rels[k]
		if !ok {
			r = &rel{kind: relKind(kind), rls: rls, sinv: securityInvoker(options)}
			rels[k] = r
			order = append(order, k)
		}
		r.cols = append(r.cols, domain.Column{Name: col, Type: typ})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}

	tables := make([]*domain.Table, 0, len(order))
	for _, k := range order {
		r := rels[k]
		tables = append(tables, domain.NewTable(k.schema, k.name, r.kind, r.rls, r.sinv, r.cols))
	}
	return domain.NewSchema(schemas, tables), nil
}

func relKind(k string) domain.Kind {
	switch k {
	case "r", "p":
		return domain.KindTable
	case "v":
		return domain.KindView
	case "m":
		return domain.KindMaterializedView
	default:
		return domain.KindForeignTable
	}
}

func securityInvoker(options []string) bool {
	for _, o := range options {
		k, v, _ := strings.Cut(o, "=")
		if k == "security_invoker" {
			switch strings.ToLower(v) {
			case "true", "on", "yes", "1":
				return true
			}
		}
	}
	return false
}

// Executor implements port.Executor.
type Executor struct {
	db      *pgxpool.Pool
	timeout time.Duration
}

// NewExecutor returns an Executor. timeout bounds each statement.
func NewExecutor(db *pgxpool.Pool, timeout time.Duration) *Executor {
	return &Executor{db: db, timeout: timeout}
}

// Query implements port.Executor.
func (e *Executor) Query(ctx context.Context, p ports.Principal, st domain.Statement) ([]byte, error) {
	var out string
	err := e.inTx(ctx, p, st.ReadOnly, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, st.SQL, st.Args...).Scan(&out)
	})
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// Exec implements port.Executor.
func (e *Executor) Exec(ctx context.Context, p ports.Principal, st domain.Statement) (int64, error) {
	var n int64
	err := e.inTx(ctx, p, st.ReadOnly, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, st.SQL, st.Args...)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// inTx runs fn in a transaction that impersonates the principal:
//
//	SET LOCAL ROLE minibase_authenticated   -- anon/authenticated only
//	set_config('request.jwt.claims', ...)   -- read by auth.uid()
//	set_config('statement_timeout', ...)
//
// LOCAL settings end with the transaction, so a pooled connection never
// leaks one request's identity into the next.
func (e *Executor) inTx(ctx context.Context, p ports.Principal, readOnly bool, fn func(pgx.Tx) error) (err error) {
	mode := pgx.ReadWrite
	if readOnly {
		mode = pgx.ReadOnly
	}
	tx, err := e.db.BeginTx(ctx, pgx.TxOptions{AccessMode: mode})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	switch p.Role {
	case ports.RoleAnon:
		_, err = tx.Exec(ctx, "SET LOCAL ROLE "+roleAnon)
	case ports.RoleAuthenticated:
		_, err = tx.Exec(ctx, "SET LOCAL ROLE "+roleAuthenticated)
	case ports.RoleService:
		// runs as the connection user, which owns the tables and bypasses RLS
	default:
		return fmt.Errorf("unknown role %q", p.Role)
	}
	if err != nil {
		return fmt.Errorf("set role: %w", err)
	}

	claims, err := json.Marshal(p.Claims)
	if err != nil {
		return fmt.Errorf("encode claims: %w", err)
	}
	if _, err = tx.Exec(ctx,
		"SELECT set_config('request.jwt.claims', $1, true), set_config('statement_timeout', $2, true)",
		string(claims), strconv.FormatInt(e.timeout.Milliseconds(), 10)); err != nil {
		return fmt.Errorf("set request context: %w", err)
	}

	if err = fn(tx); err != nil {
		return mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return mapError(err)
	}
	return nil
}

// mapError turns Postgres errors caused by the request into client errors.
// Their messages describe the caller's own input or a policy decision, so
// they are returned; anything else stays an opaque 500.
func mapError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	msg := pgErr.Message
	switch pgErr.Code {
	case "42501": // insufficient_privilege, incl. RLS WITH CHECK failures
		return errs.Forbidden("database.permission_denied", msg).Wrap(err)
	case "23505":
		return errs.Conflict("database.unique_violation", msg).Wrap(err)
	case "23503":
		return errs.Conflict("database.foreign_key_violation", msg).Wrap(err)
	case "23502":
		return errs.Invalid("database.not_null_violation", msg).Wrap(err)
	case "23514", "23P01":
		return errs.Invalid("database.check_violation", msg).Wrap(err)
	case "57014": // query_canceled (statement_timeout)
		return errs.Unavailable("database.timeout", "The query took too long and was cancelled").Wrap(err)
	case "25006": // read_only_sql_transaction (e.g. a volatile function in a GET)
		return errs.Invalid("database.read_only", msg).Wrap(err)
	case "P0001": // RAISE EXCEPTION in a trigger or function
		return errs.Invalid("database.raised", msg).Wrap(err)
	}
	switch {
	case strings.HasPrefix(pgErr.Code, "22"), // data exceptions: bad input syntax, out of range…
		pgErr.Code == "42883", // undefined_function: e.g. no = operator for json
		pgErr.Code == "42804": // datatype_mismatch
		return errs.Invalid("database.invalid_input", msg).Wrap(err)
	}
	return err
}
