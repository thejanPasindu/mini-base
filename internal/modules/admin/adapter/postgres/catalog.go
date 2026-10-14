package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/port"
	"github.com/mini-base/mini-base/internal/platform/errs"
)

// Catalog implements port.Catalog and port.DataStore.
type Catalog struct{ db *pgxpool.Pool }

// NewCatalog returns a Catalog.
func NewCatalog(db *pgxpool.Pool) *Catalog { return &Catalog{db: db} }

const listTablesSQL = `
SELECT n.nspname, c.relname, c.relkind::text, c.relrowsecurity,
       (SELECT count(*) FROM pg_catalog.pg_policy p WHERE p.polrelid = c.oid)::int,
       greatest(c.reltuples, 0)::bigint
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
  AND n.nspname <> ALL($1::text[])
  AND n.nspname NOT LIKE 'pg\_%'
ORDER BY n.nspname, c.relname`

// ListTables implements port.Catalog.
func (c *Catalog) ListTables(ctx context.Context, hidden []string) ([]domain.TableSummary, error) {
	rows, err := c.db.Query(ctx, listTablesSQL, hidden)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	out := []domain.TableSummary{}
	for rows.Next() {
		var (
			t    domain.TableSummary
			kind string
		)
		if err := rows.Scan(&t.Schema, &t.Name, &kind, &t.RLSEnabled, &t.Policies, &t.EstimatedRows); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		t.Kind = kindName(kind)
		out = append(out, t)
	}
	return out, rows.Err()
}

const columnsSQL = `
SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
       pg_get_expr(d.adbin, d.adrelid),
       a.attidentity = 'a' OR a.attgenerated <> '',
       coalesce(a.attnum = ANY(i.indkey::int2[]), false)
FROM pg_catalog.pg_attribute a
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
LEFT JOIN pg_catalog.pg_index i ON i.indrelid = a.attrelid AND i.indisprimary
WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`

const policiesSQL = `
SELECT policyname, cmd, permissive = 'PERMISSIVE', roles::text[], qual, with_check
FROM pg_catalog.pg_policies
WHERE schemaname = $1 AND tablename = $2
ORDER BY policyname`

// Table implements port.Catalog.
func (c *Catalog) Table(ctx context.Context, schema, name string, hidden []string) (*domain.Table, error) {
	if slices.Contains(hidden, schema) || strings.HasPrefix(schema, "pg_") {
		return nil, domain.ErrTableNotFound
	}
	all, err := c.ListTables(ctx, hidden)
	if err != nil {
		return nil, err
	}
	idx := slices.IndexFunc(all, func(t domain.TableSummary) bool { return t.Schema == schema && t.Name == name })
	if idx < 0 {
		return nil, domain.ErrTableNotFound
	}
	t := &domain.Table{TableSummary: all[idx], Columns: []domain.Column{}, Policies: []domain.Policy{}}

	rows, err := c.db.Query(ctx, columnsSQL, t.QualifiedName())
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}
	for rows.Next() {
		var col domain.Column
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &col.Default, &col.Generated, &col.PrimaryKey); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan column: %w", err)
		}
		t.Columns = append(t.Columns, col)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	prows, err := c.db.Query(ctx, policiesSQL, schema, name)
	if err != nil {
		return nil, fmt.Errorf("policies: %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var p domain.Policy
		if err := prows.Scan(&p.Name, &p.Command, &p.Permissive, &p.Roles, &p.Using, &p.WithCheck); err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		t.Policies = append(t.Policies, p)
	}
	return t, prows.Err()
}

// SetRLS implements port.Catalog. The table was resolved from the catalog,
// so its quoted name is safe to splice into DDL.
func (c *Catalog) SetRLS(ctx context.Context, t *domain.Table, enabled bool) error {
	action := "DISABLE"
	if enabled {
		action = "ENABLE"
	}
	_, err := c.db.Exec(ctx, "ALTER TABLE "+t.QualifiedName()+" "+action+" ROW LEVEL SECURITY")
	return mapError(err)
}

// Stats implements port.Catalog.
func (c *Catalog) Stats(ctx context.Context) (port.DatabaseStats, error) {
	var s port.DatabaseStats
	err := c.db.QueryRow(ctx,
		`SELECT current_database(), current_setting('server_version'), pg_database_size(current_database())`,
	).Scan(&s.Name, &s.Version, &s.SizeBytes)
	return s, err
}

// QueryText implements port.DataStore.
func (c *Catalog) QueryText(ctx context.Context, st domain.Statement) (string, bool, error) {
	var v *string
	err := c.db.QueryRow(ctx, st.SQL, st.Args...).Scan(&v)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, mapError(err)
	case v == nil:
		return "", false, nil
	}
	return *v, true, nil
}

// exactCountLimit: tables estimated above this get the planner's estimate
// instead of a full count(*) scan.
const exactCountLimit = 100_000

// CountRows implements port.DataStore.
func (c *Catalog) CountRows(ctx context.Context, t *domain.Table) (int64, bool, error) {
	if t.EstimatedRows > exactCountLimit {
		return t.EstimatedRows, false, nil
	}
	var n int64
	if err := c.db.QueryRow(ctx, "SELECT count(*) FROM "+t.QualifiedName()).Scan(&n); err != nil {
		return 0, false, mapError(err)
	}
	return n, true, nil
}

func kindName(k string) string {
	switch k {
	case "r", "p":
		return "table"
	case "v":
		return "view"
	case "m":
		return "materialized_view"
	default:
		return "foreign_table"
	}
}

// mapError turns Postgres errors caused by editor input into client errors
// carrying Postgres's message, which an operator needs to fix the input.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	msg := pgErr.Message
	if pgErr.Detail != "" {
		msg += " — " + pgErr.Detail
	}
	switch {
	case pgErr.Code == "23505" || pgErr.Code == "23503":
		return errs.Conflict("admin.constraint_violation", msg).Wrap(err)
	case strings.HasPrefix(pgErr.Code, "22"), strings.HasPrefix(pgErr.Code, "23"),
		strings.HasPrefix(pgErr.Code, "42"), pgErr.Code == "P0001":
		return errs.Invalid("admin.database_error", msg).Wrap(err)
	}
	return err
}
