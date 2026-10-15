package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/port"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/pkg/ports"
)

// Console implements the dashboard's data features.
type Console struct {
	catalog  port.Catalog
	data     port.DataStore
	sql      port.SQLRunner
	events   port.EventPublisher
	snippets port.SnippetStore
	audit    *Audit
	users    func() ports.UserDirectory // nil result when the auth module is off
	settings func() []Setting
	info     Info
	maxRows  int
}

// ConsoleDeps are the Console's collaborators. Users and Settings are
// called per request because other modules finish initialising later.
type ConsoleDeps struct {
	Catalog    port.Catalog
	Data       port.DataStore
	SQL        port.SQLRunner
	Events     port.EventPublisher
	Snippets   port.SnippetStore
	Audit      *Audit                     // nil: nothing is recorded
	Users      func() ports.UserDirectory // nil or nil result: auth module off
	Settings   func() []Setting           // nil: no settings shown
	Info       Info
	MaxSQLRows int
}

// Info describes the running server for the overview page.
type Info struct {
	Version string   `json:"version"`
	Env     string   `json:"env"`
	Modules []string `json:"modules"`
}

// NewConsole returns a Console.
func NewConsole(d ConsoleDeps) *Console {
	c := &Console{
		catalog: d.Catalog, data: d.Data, sql: d.SQL, events: d.Events, snippets: d.Snippets, audit: d.Audit,
		users: d.Users, settings: d.Settings, info: d.Info, maxRows: d.MaxSQLRows,
	}
	if c.users == nil {
		c.users = func() ports.UserDirectory { return nil }
	}
	if c.settings == nil {
		c.settings = func() []Setting { return nil }
	}
	return c
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
	action := domain.ActionRLSDisable
	if enabled {
		action = domain.ActionRLSEnable
	}
	c.audit.Record(ctx, action, schema+"."+name, nil)
	return c.Table(ctx, schema, name)
}

// Page is a page of rows.
type Page struct {
	Rows       json.RawMessage `json:"rows"`
	Total      int64           `json:"total"` // -1 when counting took too long
	TotalExact bool            `json:"total_exact"`
	Editable   bool            `json:"editable"`
}

// RowQuery selects rows in the browser.
type RowQuery struct {
	Filters []domain.Filter
	Sort    *domain.SortTerm
	Limit   int
	Offset  int
}

// exactCountLimit: tables estimated above this are not fully counted
// unless filtered, and filtered counts give up after countTimeout.
const (
	exactCountLimit = 100_000
	countTimeout    = 3 * time.Second
)

// Browse returns rows of a table.
func (c *Console) Browse(ctx context.Context, schema, name string, q RowQuery) (Page, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return Page{}, err
	}
	if q.Limit < 1 || q.Limit > 500 {
		q.Limit = 50
	}
	q.Offset = max(q.Offset, 0)
	st, err := domain.BuildBrowse(t, q.Filters, q.Sort, q.Limit, q.Offset)
	if err != nil {
		return Page{}, err
	}
	rows, _, err := c.data.QueryText(ctx, st)
	if err != nil {
		return Page{}, err
	}
	page := Page{Rows: json.RawMessage(rows), Editable: t.Editable()}
	page.Total, page.TotalExact, err = c.count(ctx, t, q.Filters)
	return page, err
}

func (c *Console) count(ctx context.Context, t *domain.Table, filters []domain.Filter) (int64, bool, error) {
	if t.EstimatedRows <= exactCountLimit {
		st, err := domain.BuildCount(t, filters)
		if err != nil {
			return 0, false, err
		}
		n, err := c.data.Count(ctx, st)
		return n, err == nil, err
	}
	if len(filters) == 0 {
		return t.EstimatedRows, false, nil
	}
	st, err := domain.BuildCount(t, filters)
	if err != nil {
		return 0, false, err
	}
	cctx, cancel := context.WithTimeout(ctx, countTimeout)
	defer cancel()
	n, err := c.data.Count(cctx, st)
	if err != nil {
		if ctx.Err() == nil && cctx.Err() != nil {
			return -1, false, nil // too slow to count; the page itself is fine
		}
		return 0, false, err
	}
	return n, true, nil
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
	row, err := c.one(ctx, st)
	if err == nil {
		c.audit.Record(ctx, domain.ActionRowInsert, schema+"."+name, map[string]any{"columns": cols})
	}
	return row, err
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
	row, err := c.one(ctx, st)
	if err == nil {
		c.audit.Record(ctx, domain.ActionRowUpdate, schema+"."+name, map[string]any{"key": key, "columns": cols})
	}
	return row, err
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
	row, err := c.one(ctx, st)
	if err == nil {
		c.audit.Record(ctx, domain.ActionRowDelete, schema+"."+name, map[string]any{"key": key})
	}
	return row, err
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
	detail := map[string]any{"query": domain.TruncateSQL(query), "ok": err == nil && run.Error == nil, "duration_ms": run.DurationMS}
	switch {
	case err != nil:
		detail["error"] = err.Error()
	case run.Error != nil:
		detail["error"] = run.Error.Message
	}
	c.audit.Record(ctx, domain.ActionSQLRun, "", detail)
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

// ListUsers pages through app users.
func (c *Console) ListUsers(ctx context.Context, q ports.UserQuery) ([]ports.UserSummary, int, error) {
	dir, err := c.Users()
	if err != nil {
		return nil, 0, err
	}
	users, total, err := dir.ListUsers(ctx, q)
	if users == nil {
		users = []ports.UserSummary{}
	}
	return users, total, err
}

// CreateUser adds an app user.
func (c *Console) CreateUser(ctx context.Context, email, password string) (ports.UserSummary, error) {
	dir, err := c.Users()
	if err != nil {
		return ports.UserSummary{}, err
	}
	u, err := dir.CreateUser(ctx, email, password)
	if err == nil {
		c.audit.Record(ctx, domain.ActionUserCreate, u.Email, map[string]any{"user_id": u.ID})
	}
	return u, err
}

// DeleteUser deletes an app user and everything that cascades from it.
func (c *Console) DeleteUser(ctx context.Context, id string) error {
	dir, err := c.Users()
	if err != nil {
		return err
	}
	if err := dir.DeleteUser(ctx, id); err != nil {
		return err
	}
	c.audit.Record(ctx, domain.ActionUserDelete, id, map[string]any{"user_id": id})
	return nil
}

// SignOutUser revokes an app user's sessions.
func (c *Console) SignOutUser(ctx context.Context, id string) (int, error) {
	dir, err := c.Users()
	if err != nil {
		return 0, err
	}
	n, err := dir.RevokeSessions(ctx, id)
	if err == nil {
		c.audit.Record(ctx, domain.ActionUserSignOut, id, map[string]any{"user_id": id, "sessions": n})
	}
	return n, err
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
