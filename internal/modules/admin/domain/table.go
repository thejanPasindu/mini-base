package domain

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// InternalSchemas belong to mini-base or Postgres and are hidden from the
// table editor. Their data is managed through dedicated pages (e.g. Users).
var InternalSchemas = []string{"auth", "admin", "minibase", "storage", "realtime", "hooks", "example", "information_schema"}

// Errors for the table editor.
var (
	ErrTableNotFound  = errs.NotFound("admin.table_not_found", "Table not found")
	ErrNotEditable    = errs.Invalid("admin.not_editable", "Rows can only be edited in tables that have a primary key")
	ErrUnknownColumn  = errs.Invalid("admin.unknown_column", "Unknown column")
	ErrMissingKey     = errs.Invalid("admin.missing_key", "The row key must include every primary key column")
	ErrRowNotFound    = errs.NotFound("admin.row_not_found", "Row not found; it may have been changed or deleted")
	ErrNothingToWrite = errs.Invalid("admin.nothing_to_write", "No columns to update")
)

// TableSummary is one entry in the table list.
type TableSummary struct {
	Schema        string `json:"schema"`
	Name          string `json:"name"`
	Kind          string `json:"kind"` // table, view, materialized_view, foreign_table
	RLSEnabled    bool   `json:"rls_enabled"`
	Policies      int    `json:"policies"`
	EstimatedRows int64  `json:"estimated_rows"`
}

// Column describes one column for the editor.
type Column struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Nullable   bool    `json:"nullable"`
	Default    *string `json:"default"`
	Generated  bool    `json:"generated"` // identity ALWAYS or generated column: not writable
	PrimaryKey bool    `json:"primary_key"`
}

// Policy is a row-level security policy.
type Policy struct {
	Name       string   `json:"name"`
	Command    string   `json:"command"` // ALL, SELECT, INSERT, UPDATE, DELETE
	Roles      []string `json:"roles"`
	Permissive bool     `json:"permissive"`
	Using      *string  `json:"using"`
	WithCheck  *string  `json:"with_check"`
}

// Table is the full description of one relation.
type Table struct {
	TableSummary
	Columns  []Column `json:"columns"`
	Policies []Policy `json:"policy_list"`
}

// Editable reports whether rows can be changed: a table with a primary key.
func (t *Table) Editable() bool {
	return t.Kind == "table" && len(t.PrimaryKey()) > 0
}

// PrimaryKey returns the primary key column names in table order.
func (t *Table) PrimaryKey() []string {
	var pk []string
	for _, c := range t.Columns {
		if c.PrimaryKey {
			pk = append(pk, c.Name)
		}
	}
	return pk
}

func (t *Table) column(name string) (Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

// QualifiedName returns "schema"."name".
func (t *Table) QualifiedName() string { return QuoteIdent(t.Schema) + "." + QuoteIdent(t.Name) }

// QuoteIdent quotes an SQL identifier.
func QuoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// Statement is SQL plus bind arguments; it yields one text value.
type Statement struct {
	SQL  string
	Args []any
}

// SortTerm orders the row browser.
type SortTerm struct {
	Column string
	Desc   bool
}

// BuildBrowse selects a page of rows as a JSON array. Without an explicit
// sort, rows are ordered by primary key so paging is stable.
func BuildBrowse(t *Table, sort *SortTerm, limit, offset int) (Statement, error) {
	order := ""
	switch {
	case sort != nil:
		if _, ok := t.column(sort.Column); !ok {
			return Statement{}, unknownColumn(sort.Column)
		}
		dir := "ASC"
		if sort.Desc {
			dir = "DESC"
		}
		order = " ORDER BY " + QuoteIdent(sort.Column) + " " + dir + " NULLS LAST"
	case len(t.PrimaryKey()) > 0:
		order = " ORDER BY " + quoteAll(t.PrimaryKey())
	}
	return Statement{
		SQL: "SELECT coalesce(json_agg(_r), '[]'::json)::text FROM (SELECT * FROM " + t.QualifiedName() +
			order + " LIMIT $1 OFFSET $2) _r",
		Args: []any{limit, offset},
	}, nil
}

// BuildInsert inserts one row from a JSON object and returns it.
// Columns absent from values get their defaults.
func BuildInsert(t *Table, columns []string, valuesJSON string) (Statement, error) {
	if t.Kind != "table" {
		return Statement{}, ErrNotEditable
	}
	if len(columns) == 0 {
		return Statement{
			SQL: "WITH _r AS (INSERT INTO " + t.QualifiedName() + " DEFAULT VALUES RETURNING *) SELECT row_to_json(_r)::text FROM _r",
		}, nil
	}
	if err := checkWritable(t, columns); err != nil {
		return Statement{}, err
	}
	cols := quoteAll(columns)
	return Statement{
		SQL: fmt.Sprintf("WITH _r AS (INSERT INTO %s (%s) SELECT %s FROM json_populate_record(NULL::%s, ($1::text)::json) RETURNING *) SELECT row_to_json(_r)::text FROM _r",
			t.QualifiedName(), cols, cols, t.QualifiedName()),
		Args: []any{valuesJSON},
	}, nil
}

// BuildUpdate changes the given columns of the row identified by keyJSON
// (all primary key columns) and returns the updated row.
func BuildUpdate(t *Table, keyColumns []string, keyJSON string, columns []string, valuesJSON string) (Statement, error) {
	if !t.Editable() {
		return Statement{}, ErrNotEditable
	}
	if len(columns) == 0 {
		return Statement{}, ErrNothingToWrite
	}
	if err := checkKey(t, keyColumns); err != nil {
		return Statement{}, err
	}
	if err := checkWritable(t, columns); err != nil {
		return Statement{}, err
	}
	sets := make([]string, len(columns))
	for i, c := range columns {
		q := QuoteIdent(c)
		sets[i] = fmt.Sprintf("%s = (SELECT %s FROM json_populate_record(NULL::%s, ($1::text)::json))", q, q, t.QualifiedName())
	}
	return Statement{
		SQL: fmt.Sprintf("WITH _r AS (UPDATE %s SET %s WHERE %s RETURNING *) SELECT row_to_json(_r)::text FROM _r",
			t.QualifiedName(), strings.Join(sets, ", "), keyMatch(t, "$2")),
		Args: []any{valuesJSON, keyJSON},
	}, nil
}

// BuildDelete deletes the row identified by keyJSON and returns it.
func BuildDelete(t *Table, keyColumns []string, keyJSON string) (Statement, error) {
	if !t.Editable() {
		return Statement{}, ErrNotEditable
	}
	if err := checkKey(t, keyColumns); err != nil {
		return Statement{}, err
	}
	return Statement{
		SQL: fmt.Sprintf("WITH _r AS (DELETE FROM %s WHERE %s RETURNING *) SELECT row_to_json(_r)::text FROM _r",
			t.QualifiedName(), keyMatch(t, "$1")),
		Args: []any{keyJSON},
	}, nil
}

// keyMatch renders (pk1, pk2) = (SELECT pk1, pk2 FROM json_populate_record(...)),
// so key values are converted with the columns' own types.
func keyMatch(t *Table, param string) string {
	pk := quoteAll(t.PrimaryKey())
	return fmt.Sprintf("(%s) = (SELECT %s FROM json_populate_record(NULL::%s, (%s::text)::json))", pk, pk, t.QualifiedName(), param)
}

func checkKey(t *Table, keyColumns []string) error {
	pk := t.PrimaryKey()
	if len(keyColumns) != len(pk) {
		return ErrMissingKey
	}
	for _, c := range pk {
		if !slices.Contains(keyColumns, c) {
			return ErrMissingKey
		}
	}
	return nil
}

func checkWritable(t *Table, columns []string) error {
	for _, name := range columns {
		c, ok := t.column(name)
		if !ok {
			return unknownColumn(name)
		}
		if c.Generated {
			return errs.Invalid("admin.generated_column", fmt.Sprintf("Column %q is generated by the database and cannot be written", name))
		}
	}
	return nil
}

func unknownColumn(name string) error {
	return errs.Invalid(ErrUnknownColumn.Code, fmt.Sprintf("Unknown column %q", name))
}

func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}
