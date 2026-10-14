package domain

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// Op is a filter operator.
type Op string

// Supported operators (PostgREST-compatible names).
const (
	OpEq    Op = "eq"
	OpNeq   Op = "neq"
	OpGt    Op = "gt"
	OpGte   Op = "gte"
	OpLt    Op = "lt"
	OpLte   Op = "lte"
	OpLike  Op = "like"
	OpILike Op = "ilike"
	OpIn    Op = "in"
	OpIs    Op = "is"
)

var comparison = map[Op]string{OpEq: "=", OpNeq: "<>", OpGt: ">", OpGte: ">=", OpLt: "<", OpLte: "<="}

// Filter is one condition: column [not.]op.value. Filters are ANDed.
type Filter struct {
	Column string
	Op     Op
	Negate bool
	Value  string   // all ops except in
	Values []string // in
}

// OrderTerm is one ORDER BY element.
type OrderTerm struct {
	Column string
	Desc   bool
	Nulls  string // "", "first" or "last"
}

// ReadQuery is a parsed GET request.
type ReadQuery struct {
	Columns []string // nil = all columns
	Filters []Filter
	Order   []OrderTerm
	Limit   int // 0 = server default
	Offset  int
}

// WriteQuery is the URL part of POST/PATCH/DELETE.
type WriteQuery struct {
	Columns []string // returned columns; nil = all
	Filters []Filter // PATCH/DELETE only
}

// Parameter names that are not column filters.
const (
	paramSelect = "select"
	paramOrder  = "order"
	paramLimit  = "limit"
	paramOffset = "offset"
)

// Errors.
var (
	ErrInvalidSelect = errs.Invalid("database.invalid_select", "select must be * or a comma-separated list of columns")
	ErrInvalidOrder  = errs.Invalid("database.invalid_order", "order must look like col, col.desc or col.asc.nullslast")
	ErrInvalidRange  = errs.Invalid("database.invalid_range", "limit and offset must be non-negative integers")
	ErrInvalidFilter = errs.Invalid("database.invalid_filter", "Filters must look like column=op.value, e.g. done=eq.false")
	ErrUnexpectedArg = errs.Invalid("database.unexpected_parameter", "This request does not accept that query parameter")
)

// ParseRead parses the query string of a GET request.
func ParseRead(v url.Values) (ReadQuery, error) {
	var q ReadQuery
	var err error
	if q.Columns, err = parseSelect(v.Get(paramSelect)); err != nil {
		return ReadQuery{}, err
	}
	if q.Order, err = parseOrder(v.Get(paramOrder)); err != nil {
		return ReadQuery{}, err
	}
	if q.Limit, err = parseNonNegative(v.Get(paramLimit)); err != nil {
		return ReadQuery{}, err
	}
	if q.Offset, err = parseNonNegative(v.Get(paramOffset)); err != nil {
		return ReadQuery{}, err
	}
	if q.Filters, err = parseFilters(v, paramSelect, paramOrder, paramLimit, paramOffset); err != nil {
		return ReadQuery{}, err
	}
	return q, nil
}

// ParseWrite parses the query string of a mutation. withFilters is false for
// POST, where filters make no sense and are rejected.
func ParseWrite(v url.Values, withFilters bool) (WriteQuery, error) {
	cols, err := parseSelect(v.Get(paramSelect))
	if err != nil {
		return WriteQuery{}, err
	}
	q := WriteQuery{Columns: cols}
	if !withFilters {
		for k := range v {
			if k != paramSelect {
				return WriteQuery{}, errs.Invalid(ErrUnexpectedArg.Code, fmt.Sprintf("Unexpected query parameter %q", k))
			}
		}
		return q, nil
	}
	if q.Filters, err = parseFilters(v, paramSelect); err != nil {
		return WriteQuery{}, err
	}
	return q, nil
}

func parseSelect(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	cols := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "*" {
			return nil, ErrInvalidSelect
		}
		cols = append(cols, p)
	}
	return cols, nil
}

func parseOrder(raw string) ([]OrderTerm, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []OrderTerm
	for _, term := range strings.Split(raw, ",") {
		parts := strings.Split(strings.TrimSpace(term), ".")
		t := OrderTerm{Column: parts[0]}
		if t.Column == "" || len(parts) > 3 {
			return nil, ErrInvalidOrder
		}
		for _, mod := range parts[1:] {
			switch mod {
			case "asc":
			case "desc":
				t.Desc = true
			case "nullsfirst":
				t.Nulls = "first"
			case "nullslast":
				t.Nulls = "last"
			default:
				return nil, ErrInvalidOrder
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func parseNonNegative(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, ErrInvalidRange
	}
	return n, nil
}

func parseFilters(v url.Values, reserved ...string) ([]Filter, error) {
	var out []Filter
	for col, values := range v {
		if isReserved(col, reserved) {
			continue
		}
		for _, raw := range values {
			f, err := parseFilter(col, raw)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		}
	}
	// Map iteration is random; sort for stable SQL (and plan caching).
	sortFilters(out)
	return out, nil
}

func isReserved(k string, reserved []string) bool {
	for _, r := range reserved {
		if k == r {
			return true
		}
	}
	return false
}

func parseFilter(col, raw string) (Filter, error) {
	f := Filter{Column: col}
	if rest, ok := strings.CutPrefix(raw, "not."); ok {
		f.Negate, raw = true, rest
	}
	op, value, ok := strings.Cut(raw, ".")
	if !ok || col == "" {
		return Filter{}, ErrInvalidFilter
	}
	f.Op = Op(op)

	switch {
	case comparison[f.Op] != "", f.Op == OpLike, f.Op == OpILike:
		f.Value = value
	case f.Op == OpIs:
		v := strings.ToLower(value)
		if v != "null" && v != "true" && v != "false" && v != "unknown" {
			return Filter{}, ErrInvalidFilter
		}
		f.Value = v
	case f.Op == OpIn:
		values, err := parseList(value)
		if err != nil {
			return Filter{}, err
		}
		f.Values = values
	default:
		return Filter{}, ErrInvalidFilter
	}
	return f, nil
}

// parseList parses (a,b,"c,d") into its elements. Double quotes protect
// commas; \" and \\ escape inside quotes.
func parseList(raw string) ([]string, error) {
	if len(raw) < 2 || raw[0] != '(' || raw[len(raw)-1] != ')' {
		return nil, ErrInvalidFilter
	}
	body := raw[1 : len(raw)-1]
	if body == "" {
		return []string{}, nil
	}

	var (
		out     []string
		cur     strings.Builder
		quoted  bool
		escaped bool
	)
	for _, r := range body {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quoted && r == '\\':
			escaped = true
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if quoted || escaped {
		return nil, ErrInvalidFilter
	}
	return append(out, cur.String()), nil
}

func sortFilters(fs []Filter) {
	// insertion sort: filter lists are tiny
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && less(fs[j], fs[j-1]); j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
}

func less(a, b Filter) bool {
	if a.Column != b.Column {
		return a.Column < b.Column
	}
	if a.Op != b.Op {
		return a.Op < b.Op
	}
	return a.Value < b.Value
}
