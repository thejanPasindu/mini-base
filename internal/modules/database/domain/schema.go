// Package domain contains the database module's core: the schema model, the
// URL query language and the SQL builder. Everything here is pure; nothing
// talks to Postgres.
package domain

import (
	"strings"
)

// Kind is the relation kind.
type Kind uint8

// Relation kinds that can be exposed.
const (
	KindTable Kind = iota + 1
	KindView
	KindMaterializedView
	KindForeignTable
)

// Column is one column with its SQL type as rendered by format_type().
type Column struct {
	Name string
	Type string
}

// Table is an exposed relation.
type Table struct {
	Schema          string
	Name            string
	Kind            Kind
	RLSEnabled      bool // tables: row level security is enabled
	SecurityInvoker bool // views: run with the caller's privileges (PG15+)
	Columns         []Column

	index map[string]int
}

// NewTable builds a Table and indexes its columns.
func NewTable(schema, name string, kind Kind, rls, securityInvoker bool, cols []Column) *Table {
	t := &Table{
		Schema: schema, Name: name, Kind: kind, RLSEnabled: rls, SecurityInvoker: securityInvoker,
		Columns: cols, index: make(map[string]int, len(cols)),
	}
	for i, c := range cols {
		t.index[c.Name] = i
	}
	return t
}

// Column returns the named column.
func (t *Table) Column(name string) (Column, bool) {
	i, ok := t.index[name]
	if !ok {
		return Column{}, false
	}
	return t.Columns[i], true
}

// QualifiedName returns the quoted "schema"."name".
func (t *Table) QualifiedName() string {
	return QuoteIdent(t.Schema) + "." + QuoteIdent(t.Name)
}

// String returns schema.name for messages.
func (t *Table) String() string { return t.Schema + "." + t.Name }

// Schema is a snapshot of every exposed relation.
type Schema struct {
	search []string                     // exposed schemas, in lookup order
	tables map[string]map[string]*Table // schema -> name -> table
}

// NewSchema builds a Schema. search lists exposed schemas in lookup order.
func NewSchema(search []string, tables []*Table) *Schema {
	s := &Schema{search: search, tables: make(map[string]map[string]*Table)}
	for _, t := range tables {
		if s.tables[t.Schema] == nil {
			s.tables[t.Schema] = make(map[string]*Table)
		}
		s.tables[t.Schema][t.Name] = t
	}
	return s
}

// Table finds name in the first exposed schema that has it.
func (s *Schema) Table(name string) (*Table, bool) {
	for _, schema := range s.search {
		if t, ok := s.tables[schema][name]; ok {
			return t, true
		}
	}
	return nil, false
}

// QuoteIdent quotes an SQL identifier. Identifiers are also validated against
// the schema before use; quoting is defence in depth.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
