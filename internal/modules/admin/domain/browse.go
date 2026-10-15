package domain

import (
	"fmt"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// Filter narrows the row browser and CSV export.
type Filter struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

// FilterOps are the supported operators. Values are compared after casting
// to the column's type, except "contains", which matches the text form
// case-insensitively.
var FilterOps = []string{"eq", "neq", "lt", "lte", "gt", "gte", "contains", "is_null", "not_null"}

var comparisons = map[string]string{"eq": "=", "neq": "IS DISTINCT FROM", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}

// ParseFilter parses "column:op:value" (the value may contain colons).
func ParseFilter(raw string) (Filter, error) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 2 {
		return Filter{}, errs.Invalid("admin.invalid_filter", fmt.Sprintf("Filter %q must look like column:op:value", raw))
	}
	f := Filter{Column: parts[0], Op: parts[1]}
	if len(parts) == 3 {
		f.Value = parts[2]
	}
	return f, nil
}

// where renders the filters as a WHERE clause whose bind parameters start
// at $first, and returns the clause and the values to bind.
func where(t *Table, filters []Filter, first int) (string, []any, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}
	conds := make([]string, 0, len(filters))
	var args []any
	param := func(v string) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", first+len(args)-1)
	}
	for _, f := range filters {
		col, ok := t.column(f.Column)
		if !ok {
			return "", nil, unknownColumn(f.Column)
		}
		q := QuoteIdent(col.Name)
		switch op, cmp := f.Op, comparisons[f.Op]; {
		case cmp != "":
			// col.Type comes from the catalog (format_type), not from the client.
			conds = append(conds, fmt.Sprintf("%s %s (%s::text)::%s", q, cmp, param(f.Value), col.Type))
		case op == "contains":
			conds = append(conds, fmt.Sprintf("strpos(lower(%s::text), lower(%s::text)) > 0", q, param(f.Value)))
		case op == "is_null":
			conds = append(conds, q+" IS NULL")
		case op == "not_null":
			conds = append(conds, q+" IS NOT NULL")
		default:
			return "", nil, errs.Invalid("admin.invalid_filter", fmt.Sprintf("Unknown filter operator %q", f.Op))
		}
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

func orderBy(t *Table, sort *SortTerm) (string, error) {
	switch {
	case sort != nil:
		if _, ok := t.column(sort.Column); !ok {
			return "", unknownColumn(sort.Column)
		}
		dir := "ASC"
		if sort.Desc {
			dir = "DESC"
		}
		return " ORDER BY " + QuoteIdent(sort.Column) + " " + dir + " NULLS LAST", nil
	case len(t.PrimaryKey()) > 0:
		return " ORDER BY " + quoteAll(t.PrimaryKey()), nil
	}
	return "", nil
}

// BuildBrowse selects a page of rows as a JSON array. Without an explicit
// sort, rows are ordered by primary key so paging is stable.
func BuildBrowse(t *Table, filters []Filter, sort *SortTerm, limit, offset int) (Statement, error) {
	order, err := orderBy(t, sort)
	if err != nil {
		return Statement{}, err
	}
	cond, args, err := where(t, filters, 3)
	if err != nil {
		return Statement{}, err
	}
	return Statement{
		SQL: "SELECT coalesce(json_agg(_r), '[]'::json)::text FROM (SELECT * FROM " + t.QualifiedName() +
			cond + order + " LIMIT $1 OFFSET $2) _r",
		Args: append([]any{limit, offset}, args...),
	}, nil
}

// BuildCount counts the rows matching filters.
func BuildCount(t *Table, filters []Filter) (Statement, error) {
	cond, args, err := where(t, filters, 1)
	if err != nil {
		return Statement{}, err
	}
	return Statement{SQL: "SELECT count(*) FROM " + t.QualifiedName() + cond, Args: args}, nil
}

// BuildExport selects every column as text, in table order, for CSV export.
// NULL comes back as a nil value.
func BuildExport(t *Table, filters []Filter, sort *SortTerm) (Statement, []string, error) {
	order, err := orderBy(t, sort)
	if err != nil {
		return Statement{}, nil, err
	}
	cond, args, err := where(t, filters, 1)
	if err != nil {
		return Statement{}, nil, err
	}
	names := make([]string, len(t.Columns))
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
		cols[i] = QuoteIdent(c.Name) + "::text"
	}
	return Statement{
		SQL:  "SELECT " + strings.Join(cols, ", ") + " FROM " + t.QualifiedName() + cond + order,
		Args: args,
	}, names, nil
}
