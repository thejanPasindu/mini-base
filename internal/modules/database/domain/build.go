package domain

import (
	"fmt"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// Statement is SQL ready to execute. Every value supplied by the client is a
// bind parameter; identifiers and types come only from the schema snapshot.
type Statement struct {
	SQL      string
	Args     []any
	ReadOnly bool
	// Returns is true when SQL yields one text value: a JSON array of rows.
	// Otherwise it is executed for its affected-row count.
	Returns bool
}

// Builder errors.
var (
	ErrUnknownColumn  = errs.Invalid("database.unknown_column", "Unknown column")
	ErrFilterRequired = errs.Invalid("database.filter_required",
		"PATCH and DELETE require at least one filter, e.g. ?id=eq.1, so a whole table is never changed by accident")
	ErrEmptyBody = errs.Invalid("database.empty_body", "Request body must set at least one column")
)

func unknownColumn(t *Table, col string) error {
	return errs.Invalid(ErrUnknownColumn.Code, fmt.Sprintf("Column %q does not exist on %s", col, t))
}

// args collects bind parameters and hands out their placeholders.
type args struct{ list []any }

func (a *args) add(v any) string {
	a.list = append(a.list, v)
	return fmt.Sprintf("$%d", len(a.list))
}

// BuildSelect builds:
//
//	SELECT coalesce(json_agg(_r), '[]')::text FROM (SELECT ... LIMIT n) _r
//
// Postgres serialises the rows to JSON, so no type mapping happens in Go.
func BuildSelect(t *Table, q ReadQuery, maxRows int) (Statement, error) {
	var a args
	cols, err := columnList(t, q.Columns)
	if err != nil {
		return Statement{}, err
	}
	where, err := whereClause(t, q.Filters, &a)
	if err != nil {
		return Statement{}, err
	}
	order, err := orderClause(t, q.Order)
	if err != nil {
		return Statement{}, err
	}

	limit := maxRows
	if q.Limit > 0 && q.Limit < maxRows {
		limit = q.Limit
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SELECT %s FROM %s", cols, t.QualifiedName())
	b.WriteString(where)
	b.WriteString(order)
	fmt.Fprintf(&b, " LIMIT %s", a.add(limit))
	if q.Offset > 0 {
		fmt.Fprintf(&b, " OFFSET %s", a.add(q.Offset))
	}

	return Statement{
		SQL:      "SELECT coalesce(json_agg(_r), '[]'::json)::text FROM (" + b.String() + ") _r",
		Args:     a.list,
		ReadOnly: true,
		Returns:  true,
	}, nil
}

// BuildInsert inserts rowsJSON (a JSON array of objects) using
// json_populate_recordset, which converts each value with the column's type.
// Keys absent from a row become NULL.
func BuildInsert(t *Table, columns []string, rowsJSON string, ret *WriteQuery) (Statement, error) {
	if len(columns) == 0 {
		return Statement{}, ErrEmptyBody
	}
	var a args
	cols, err := namedColumns(t, columns)
	if err != nil {
		return Statement{}, err
	}
	src := a.add(rowsJSON)
	stmt := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM json_populate_recordset(NULL::%s, (%s::text)::json)",
		t.QualifiedName(), cols, cols, t.QualifiedName(), src)
	return withReturning(t, stmt, a, ret)
}

// BuildUpdate sets the columns present in objJSON (a JSON object) on every
// row matching the filters, which are mandatory.
func BuildUpdate(t *Table, columns []string, objJSON string, q WriteQuery, ret bool) (Statement, error) {
	if len(columns) == 0 {
		return Statement{}, ErrEmptyBody
	}
	if len(q.Filters) == 0 {
		return Statement{}, ErrFilterRequired
	}
	var a args
	src := a.add(objJSON)
	sets := make([]string, 0, len(columns))
	for _, name := range columns {
		if _, ok := t.Column(name); !ok {
			return Statement{}, unknownColumn(t, name)
		}
		col := QuoteIdent(name)
		sets = append(sets, fmt.Sprintf("%s = (SELECT %s FROM json_populate_record(NULL::%s, (%s::text)::json))",
			col, col, t.QualifiedName(), src))
	}
	where, err := whereClause(t, q.Filters, &a)
	if err != nil {
		return Statement{}, err
	}
	stmt := fmt.Sprintf("UPDATE %s SET %s%s", t.QualifiedName(), strings.Join(sets, ", "), where)
	return withReturning(t, stmt, a, returning(q, ret))
}

// BuildDelete deletes every row matching the filters, which are mandatory.
func BuildDelete(t *Table, q WriteQuery, ret bool) (Statement, error) {
	if len(q.Filters) == 0 {
		return Statement{}, ErrFilterRequired
	}
	var a args
	where, err := whereClause(t, q.Filters, &a)
	if err != nil {
		return Statement{}, err
	}
	return withReturning(t, "DELETE FROM "+t.QualifiedName()+where, a, returning(q, ret))
}

func returning(q WriteQuery, ret bool) *WriteQuery {
	if !ret {
		return nil
	}
	return &q
}

// withReturning wraps a mutation so it yields the affected rows as JSON when
// ret is non-nil. Without RETURNING the statement does not need SELECT
// permission on the rows, which matches "write-only" RLS policies.
func withReturning(t *Table, stmt string, a args, ret *WriteQuery) (Statement, error) {
	if ret == nil {
		return Statement{SQL: stmt, Args: a.list}, nil
	}
	cols, err := columnList(t, ret.Columns)
	if err != nil {
		return Statement{}, err
	}
	return Statement{
		SQL:     "WITH _r AS (" + stmt + " RETURNING " + cols + ") SELECT coalesce(json_agg(_r), '[]'::json)::text FROM _r",
		Args:    a.list,
		Returns: true,
	}, nil
}

// columnList renders requested columns, or all columns when none requested.
func columnList(t *Table, requested []string) (string, error) {
	if len(requested) == 0 {
		names := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			names[i] = c.Name
		}
		return namedColumns(t, names)
	}
	return namedColumns(t, requested)
}

func namedColumns(t *Table, names []string) (string, error) {
	quoted := make([]string, len(names))
	for i, n := range names {
		if _, ok := t.Column(n); !ok {
			return "", unknownColumn(t, n)
		}
		quoted[i] = QuoteIdent(n)
	}
	return strings.Join(quoted, ", "), nil
}

func whereClause(t *Table, filters []Filter, a *args) (string, error) {
	if len(filters) == 0 {
		return "", nil
	}
	conds := make([]string, 0, len(filters))
	for _, f := range filters {
		c, ok := t.Column(f.Column)
		if !ok {
			return "", unknownColumn(t, f.Column)
		}
		cond, err := condition(c, f, a)
		if err != nil {
			return "", err
		}
		if f.Negate {
			cond = "NOT (" + cond + ")"
		}
		conds = append(conds, cond)
	}
	return " WHERE " + strings.Join(conds, " AND "), nil
}

// condition renders one filter. Values are bound as text and cast to the
// column's type in SQL, so Postgres parses them exactly as it would a literal
// and rejects invalid input with a clear error.
func condition(c Column, f Filter, a *args) (string, error) {
	col := QuoteIdent(c.Name)
	switch f.Op {
	case OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte:
		return fmt.Sprintf("%s %s (%s::text)::%s", col, comparison[f.Op], a.add(f.Value), c.Type), nil
	case OpLike, OpILike:
		// PostgREST convention: * is the wildcard (% is awkward in URLs).
		pattern := strings.ReplaceAll(f.Value, "*", "%")
		return fmt.Sprintf("%s::text %s %s::text", col, strings.ToUpper(string(f.Op)), a.add(pattern)), nil
	case OpIn:
		return fmt.Sprintf("%s = ANY((%s::text[])::%s[])", col, a.add(f.Values), c.Type), nil
	case OpIs:
		return fmt.Sprintf("%s IS %s", col, strings.ToUpper(f.Value)), nil // value is from a fixed set
	default:
		return "", ErrInvalidFilter
	}
}

func orderClause(t *Table, terms []OrderTerm) (string, error) {
	if len(terms) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(terms))
	for _, o := range terms {
		if _, ok := t.Column(o.Column); !ok {
			return "", unknownColumn(t, o.Column)
		}
		p := QuoteIdent(o.Column)
		if o.Desc {
			p += " DESC"
		} else {
			p += " ASC"
		}
		switch o.Nulls {
		case "first":
			p += " NULLS FIRST"
		case "last":
			p += " NULLS LAST"
		}
		parts = append(parts, p)
	}
	return " ORDER BY " + strings.Join(parts, ", "), nil
}
