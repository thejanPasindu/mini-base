package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/port"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/pkg/ports"
)

// Console implements the dashboard's data features.
type Console struct {
	catalog port.Catalog
	data    port.DataStore
	sql     port.SQLRunner
	events  port.EventPublisher
	users   func() ports.UserDirectory // nil result when the auth module is off
	info    Info
	maxRows int
}

// Info describes the running server for the overview page.
type Info struct {
	Version string   `json:"version"`
	Env     string   `json:"env"`
	Modules []string `json:"modules"`
}

// NewConsole returns a Console. users is looked up per call because the auth
// module may provide the directory after this module initialises.
func NewConsole(catalog port.Catalog, data port.DataStore, sql port.SQLRunner, events port.EventPublisher,
	users func() ports.UserDirectory, info Info, maxSQLRows int,
) *Console {
	return &Console{catalog: catalog, data: data, sql: sql, events: events, users: users, info: info, maxRows: maxSQLRows}
}

// ErrUsersUnavailable is returned by user features when auth is disabled.
var ErrUsersUnavailable = errs.NotFound("admin.users_unavailable", "User management needs the auth module (add it to MINIBASE_MODULES)")

// Overview is the dashboard home page.
type Overview struct {
	Server   Info                  `json:"server"`
	Database port.DatabaseStats    `json:"database"`
	Tables   int                   `json:"tables"`
	NoRLS    []domain.TableSummary `json:"tables_without_rls"`
	Users    *int                  `json:"users"` // nil when the auth module is off
	Features map[string]bool       `json:"features"`
}

// Overview gathers server, database and security status.
func (c *Console) Overview(ctx context.Context) (Overview, error) {
	stats, err := c.catalog.Stats(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("database stats: %w", err)
	}
	tables, err := c.catalog.ListTables(ctx, domain.InternalSchemas)
	if err != nil {
		return Overview{}, fmt.Errorf("list tables: %w", err)
	}
	ov := Overview{
		Server:   c.info,
		Database: stats,
		Tables:   len(tables),
		NoRLS:    []domain.TableSummary{},
		Features: map[string]bool{"users": c.users() != nil},
	}
	for _, t := range tables {
		if t.Kind == "table" && !t.RLSEnabled {
			ov.NoRLS = append(ov.NoRLS, t)
		}
	}
	if dir := c.users(); dir != nil {
		_, total, err := dir.ListUsers(ctx, ports.UserQuery{Limit: 1})
		if err != nil {
			return Overview{}, fmt.Errorf("count users: %w", err)
		}
		ov.Users = &total
	}
	return ov, nil
}

// Tables lists user tables (internal schemas hidden).
func (c *Console) Tables(ctx context.Context) ([]domain.TableSummary, error) {
	return c.catalog.ListTables(ctx, domain.InternalSchemas)
}

// Table describes one table, including policies.
func (c *Console) Table(ctx context.Context, schema, name string) (*domain.Table, error) {
	return c.catalog.Table(ctx, schema, name, domain.InternalSchemas)
}

// SetRLS turns row level security on or off for a table.
func (c *Console) SetRLS(ctx context.Context, schema, name string, enabled bool) (*domain.Table, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return nil, err
	}
	if t.Kind != "table" {
		return nil, errs.Invalid("admin.rls_not_supported", "Row level security applies to tables only")
	}
	if err := c.catalog.SetRLS(ctx, t, enabled); err != nil {
		return nil, fmt.Errorf("set rls: %w", err)
	}
	c.events.Publish(ctx, ports.EventSchemaChanged, map[string]string{"table": schema + "." + name})
	return c.Table(ctx, schema, name)
}

// Page is a page of rows.
type Page struct {
	Rows       json.RawMessage `json:"rows"`
	Total      int64           `json:"total"`
	TotalExact bool            `json:"total_exact"`
	Editable   bool            `json:"editable"`
}

// Browse returns rows of a table.
func (c *Console) Browse(ctx context.Context, schema, name string, sort *domain.SortTerm, limit, offset int) (Page, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return Page{}, err
	}
	if limit < 1 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	st, err := domain.BuildBrowse(t, sort, limit, offset)
	if err != nil {
		return Page{}, err
	}
	rows, _, err := c.data.QueryText(ctx, st)
	if err != nil {
		return Page{}, err
	}
	total, exact, err := c.data.CountRows(ctx, t)
	if err != nil {
		return Page{}, err
	}
	return Page{Rows: json.RawMessage(rows), Total: total, TotalExact: exact, Editable: t.Editable()}, nil
}

// InsertRow inserts a row from a JSON object of column values.
func (c *Console) InsertRow(ctx context.Context, schema, name string, values map[string]json.RawMessage) (json.RawMessage, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return nil, err
	}
	cols, js, err := encode(values)
	if err != nil {
		return nil, err
	}
	st, err := domain.BuildInsert(t, cols, js)
	if err != nil {
		return nil, err
	}
	return c.one(ctx, st)
}

// UpdateRow changes columns of the row identified by key.
func (c *Console) UpdateRow(ctx context.Context, schema, name string, key, values map[string]json.RawMessage) (json.RawMessage, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return nil, err
	}
	keyCols, keyJS, err := encode(key)
	if err != nil {
		return nil, err
	}
	cols, js, err := encode(values)
	if err != nil {
		return nil, err
	}
	st, err := domain.BuildUpdate(t, keyCols, keyJS, cols, js)
	if err != nil {
		return nil, err
	}
	return c.one(ctx, st)
}

// DeleteRow deletes the row identified by key.
func (c *Console) DeleteRow(ctx context.Context, schema, name string, key map[string]json.RawMessage) (json.RawMessage, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return nil, err
	}
	keyCols, keyJS, err := encode(key)
	if err != nil {
		return nil, err
	}
	st, err := domain.BuildDelete(t, keyCols, keyJS)
	if err != nil {
		return nil, err
	}
	return c.one(ctx, st)
}

func (c *Console) one(ctx context.Context, st domain.Statement) (json.RawMessage, error) {
	v, ok, err := c.data.QueryText(ctx, st)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrRowNotFound
	}
	return json.RawMessage(v), nil
}

// RunSQL executes a script in the SQL editor.
func (c *Console) RunSQL(ctx context.Context, query string) (domain.SQLRun, error) {
	if strings.TrimSpace(query) == "" {
		return domain.SQLRun{}, errs.Invalid("admin.empty_query", "Enter some SQL to run")
	}
	run, err := c.sql.Run(ctx, query, c.maxRows)
	if err != nil {
		return domain.SQLRun{}, err
	}
	if run.ChangesSchema() {
		c.events.Publish(ctx, ports.EventSchemaChanged, map[string]string{"source": "sql_editor"})
	}
	return run, nil
}

// Users returns the user directory or ErrUsersUnavailable.
func (c *Console) Users() (ports.UserDirectory, error) {
	if dir := c.users(); dir != nil {
		return dir, nil
	}
	return nil, ErrUsersUnavailable
}

// encode returns the keys (sorted) and the JSON object for values.
func encode(values map[string]json.RawMessage) ([]string, string, error) {
	cols := make([]string, 0, len(values))
	for k := range values {
		cols = append(cols, k)
	}
	slices.Sort(cols)
	b, err := json.Marshal(values)
	if err != nil {
		return nil, "", errs.Invalid("admin.invalid_values", "Values must be JSON")
	}
	return cols, string(b), nil
}
