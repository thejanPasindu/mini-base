package service

import (
	"context"
	"fmt"
	"slices"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/pkg/ports"
)

// DDLResult is the outcome of a table designer change. With Preview, only
// SQL is set and nothing ran.
type DDLResult struct {
	SQL     []string      `json:"sql"`
	Preview bool          `json:"preview"`
	Table   *domain.Table `json:"table,omitempty"`
}

// Schemas lists the schemas tables can be created in.
func (c *Console) Schemas(ctx context.Context) ([]string, error) {
	return c.catalog.Schemas(ctx, domain.InternalSchemas)
}

// CreateTable creates a table from the designer form.
func (c *Console) CreateTable(ctx context.Context, spec domain.TableSpec, preview bool) (DDLResult, error) {
	schemas, err := c.Schemas(ctx)
	if err != nil {
		return DDLResult{}, err
	}
	if !slices.Contains(schemas, spec.Schema) {
		return DDLResult{}, errs.NotFound(domain.ErrSchemaNotFound.Code, fmt.Sprintf("Schema %q not found", spec.Schema))
	}
	for _, col := range spec.Columns {
		if err := c.checkReference(ctx, col); err != nil {
			return DDLResult{}, err
		}
	}
	stmts, err := domain.BuildCreateTable(spec)
	if err != nil {
		return DDLResult{}, err
	}
	return c.applyDDL(ctx, stmts, preview, domain.ActionTableCreate, spec.Schema, spec.Name, true)
}

// DropTable drops a table (or view); confirm must repeat its name.
func (c *Console) DropTable(ctx context.Context, schema, name, confirm string, preview bool) (DDLResult, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return DDLResult{}, err
	}
	stmts, err := domain.BuildDropTable(t, confirm)
	if err != nil {
		return DDLResult{}, err
	}
	return c.applyDDL(ctx, stmts, preview, domain.ActionTableDrop, schema, name, false)
}

// AddColumn adds a column.
func (c *Console) AddColumn(ctx context.Context, schema, name string, col domain.ColumnSpec, preview bool) (DDLResult, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return DDLResult{}, err
	}
	if err := c.checkReference(ctx, col); err != nil {
		return DDLResult{}, err
	}
	stmts, err := domain.BuildAddColumn(t, col)
	if err != nil {
		return DDLResult{}, err
	}
	return c.applyDDL(ctx, stmts, preview, domain.ActionColumnAdd, schema, name, true)
}

// DropColumn drops a column.
func (c *Console) DropColumn(ctx context.Context, schema, name, column string, preview bool) (DDLResult, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return DDLResult{}, err
	}
	stmts, err := domain.BuildDropColumn(t, column)
	if err != nil {
		return DDLResult{}, err
	}
	return c.applyDDL(ctx, stmts, preview, domain.ActionColumnDrop, schema, name, true)
}

// AddPolicy adds a row level security policy from a template.
func (c *Console) AddPolicy(ctx context.Context, schema, name string, spec domain.PolicySpec, preview bool) (DDLResult, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return DDLResult{}, err
	}
	stmt, err := domain.BuildPolicy(t, spec)
	if err != nil {
		return DDLResult{}, err
	}
	return c.applyDDL(ctx, []string{stmt}, preview, domain.ActionPolicyCreate, schema, name, true)
}

// DropPolicy removes a policy.
func (c *Console) DropPolicy(ctx context.Context, schema, name, policy string, preview bool) (DDLResult, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return DDLResult{}, err
	}
	stmt, err := domain.BuildDropPolicy(t, policy)
	if err != nil {
		return DDLResult{}, err
	}
	return c.applyDDL(ctx, []string{stmt}, preview, domain.ActionPolicyDrop, schema, name, true)
}

// applyDDL runs (or previews) statements, then announces the schema change,
// records it and returns the table as it is now.
func (c *Console) applyDDL(ctx context.Context, stmts []string, preview bool, action, schema, name string, reload bool) (DDLResult, error) {
	res := DDLResult{SQL: stmts, Preview: preview}
	if preview {
		return res, nil
	}
	if err := c.catalog.ExecDDL(ctx, stmts); err != nil {
		return DDLResult{}, err
	}
	c.events.Publish(ctx, ports.EventSchemaChanged, map[string]string{"table": schema + "." + name})
	c.audit.Record(ctx, action, schema+"."+name, map[string]any{"sql": stmts})
	if reload {
		t, err := c.Table(ctx, schema, name)
		if err != nil {
			return DDLResult{}, err
		}
		res.Table = t
	}
	return res, nil
}

// checkReference verifies that a foreign key target exists. auth.users is
// allowed when the auth module is on; other targets must be visible tables.
func (c *Console) checkReference(ctx context.Context, col domain.ColumnSpec) error {
	r := col.References
	if r == nil {
		return nil
	}
	if r.IsAuthUsers() {
		if c.users() == nil {
			return errs.Invalid("admin.invalid_reference", "auth.users exists only when the auth module is enabled")
		}
		if col.Type != "uuid" {
			return errs.Invalid("admin.invalid_reference", fmt.Sprintf("Column %q must be uuid to reference auth.users", col.Name))
		}
		return nil
	}
	t, err := c.Table(ctx, r.Schema, r.Table)
	if err != nil {
		return errs.Invalid("admin.invalid_reference", fmt.Sprintf("Column %q references %s.%s, which was not found", col.Name, r.Schema, r.Table))
	}
	if !slices.ContainsFunc(t.Columns, func(tc domain.Column) bool { return tc.Name == r.Column }) {
		return errs.Invalid("admin.invalid_reference", fmt.Sprintf("Column %q references %s.%s(%s), which does not exist", col.Name, r.Schema, r.Table, r.Column))
	}
	return nil
}
