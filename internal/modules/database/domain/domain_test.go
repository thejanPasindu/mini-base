package domain_test

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/mini-base/mini-base/internal/modules/database/domain"
)

func todos() *domain.Table {
	return domain.NewTable("public", "todos", domain.KindTable, true, false, []domain.Column{
		{Name: "id", Type: "bigint"},
		{Name: "user_id", Type: "uuid"},
		{Name: "title", Type: "text"},
		{Name: "done", Type: "boolean"},
		{Name: "due", Type: "timestamp with time zone"},
	})
}

func mustValues(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseRead(t *testing.T) {
	t.Parallel()

	q, err := domain.ParseRead(mustValues(t,
		"select=id,title&done=eq.false&title=not.ilike.*milk*&id=in.(1,2,3)&due=is.null&order=due.desc.nullslast,id&limit=10&offset=5"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.Columns, []string{"id", "title"}) || q.Limit != 10 || q.Offset != 5 {
		t.Fatalf("query = %+v", q)
	}
	wantFilters := []domain.Filter{
		{Column: "done", Op: domain.OpEq, Value: "false"},
		{Column: "due", Op: domain.OpIs, Value: "null"},
		{Column: "id", Op: domain.OpIn, Values: []string{"1", "2", "3"}},
		{Column: "title", Op: domain.OpILike, Negate: true, Value: "*milk*"},
	}
	if !reflect.DeepEqual(q.Filters, wantFilters) {
		t.Fatalf("filters = %+v", q.Filters)
	}
	wantOrder := []domain.OrderTerm{{Column: "due", Desc: true, Nulls: "last"}, {Column: "id"}}
	if !reflect.DeepEqual(q.Order, wantOrder) {
		t.Fatalf("order = %+v", q.Order)
	}
}

func TestParseInListQuoting(t *testing.T) {
	t.Parallel()

	q, err := domain.ParseRead(url.Values{"title": {`in.(a,"b,c","d\"e",)`}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b,c", `d"e`, ""}; !reflect.DeepEqual(q.Filters[0].Values, want) {
		t.Fatalf("values = %q", q.Filters[0].Values)
	}
}

func TestParseReadRejects(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"done=false",        // no operator
		"done=regex.x",      // unknown operator
		"due=is.maybe",      // bad is value
		"id=in.1,2",         // missing parens
		`id=in.("a)`,        // unterminated quote
		"limit=-1",          // negative
		"order=id.sideways", // bad direction
		"select=id,,title",  // empty column
	} {
		if _, err := domain.ParseRead(mustValues(t, raw)); err == nil {
			t.Errorf("%s: expected error", raw)
		}
	}
}

func TestParseWrite(t *testing.T) {
	t.Parallel()

	if _, err := domain.ParseWrite(mustValues(t, "select=id&done=eq.true"), false); err == nil {
		t.Fatal("POST must reject filters")
	}
	q, err := domain.ParseWrite(mustValues(t, "select=id&done=eq.true"), true)
	if err != nil || len(q.Filters) != 1 || q.Columns[0] != "id" {
		t.Fatalf("q = %+v, %v", q, err)
	}
}

func TestBuildSelect(t *testing.T) {
	t.Parallel()

	q, _ := domain.ParseRead(mustValues(t, "select=id,title&done=eq.false&id=in.(1,2)&order=id.desc&limit=5000&offset=10"))
	st, err := domain.BuildSelect(todos(), q, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT coalesce(json_agg(_r), '[]'::json)::text FROM (` +
		`SELECT "id", "title" FROM "public"."todos" ` +
		`WHERE "done" = ($1::text)::boolean AND "id" = ANY(($2::text[])::bigint[]) ` +
		`ORDER BY "id" DESC LIMIT $3 OFFSET $4) _r`
	if st.SQL != want {
		t.Fatalf("sql:\n got %s\nwant %s", st.SQL, want)
	}
	if !reflect.DeepEqual(st.Args, []any{"false", []string{"1", "2"}, 1000, 10}) {
		t.Fatalf("args = %#v (limit must be capped at max rows)", st.Args)
	}
	if !st.ReadOnly || !st.Returns {
		t.Fatal("select must be read-only and return rows")
	}
}

func TestBuildSelectAllColumnsAndLike(t *testing.T) {
	t.Parallel()

	q, _ := domain.ParseRead(url.Values{"title": {"like.*milk*"}})
	st, err := domain.BuildSelect(todos(), q, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, `SELECT "id", "user_id", "title", "done", "due" FROM`) ||
		!strings.Contains(st.SQL, `"title"::text LIKE $1::text`) || st.Args[0] != "%milk%" {
		t.Fatalf("sql = %s args = %v", st.SQL, st.Args)
	}
}

func TestUnknownIdentifiersAreRejectedNotQuoted(t *testing.T) {
	t.Parallel()

	inject := `title"; DROP TABLE todos; --`
	for name, q := range map[string]domain.ReadQuery{
		"select": {Columns: []string{inject}},
		"filter": {Filters: []domain.Filter{{Column: inject, Op: domain.OpEq, Value: "x"}}},
		"order":  {Order: []domain.OrderTerm{{Column: inject}}},
	} {
		if _, err := domain.BuildSelect(todos(), q, 10); !errors.Is(err, domain.ErrUnknownColumn) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestValuesAreAlwaysBound(t *testing.T) {
	t.Parallel()

	evil := `x'); DROP TABLE todos; --`
	q, _ := domain.ParseRead(url.Values{"title": {"eq." + evil}})
	st, _ := domain.BuildSelect(todos(), q, 10)
	if strings.Contains(st.SQL, "DROP") || st.Args[0] != evil {
		t.Fatalf("value leaked into SQL: %s", st.SQL)
	}
}

func TestBuildInsert(t *testing.T) {
	t.Parallel()

	cols, rows, err := domain.ParseRows([]byte(`{"title":"milk","done":false}`))
	if err != nil {
		t.Fatal(err)
	}
	st, err := domain.BuildInsert(todos(), cols, rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "public"."todos" ("done", "title") SELECT "done", "title" ` +
		`FROM json_populate_recordset(NULL::"public"."todos", ($1::text)::json)`
	if st.SQL != want || st.Args[0] != `[{"title":"milk","done":false}]` || st.Returns {
		t.Fatalf("sql = %s args = %v", st.SQL, st.Args)
	}

	ret, _ := domain.BuildInsert(todos(), cols, rows, &domain.WriteQuery{Columns: []string{"id"}})
	if !strings.HasPrefix(ret.SQL, "WITH _r AS (INSERT") || !strings.Contains(ret.SQL, `RETURNING "id")`) || !ret.Returns {
		t.Fatalf("returning sql = %s", ret.SQL)
	}

	if _, err := domain.BuildInsert(todos(), []string{"nope"}, `[{"nope":1}]`, nil); !errors.Is(err, domain.ErrUnknownColumn) {
		t.Fatalf("unknown column: %v", err)
	}
}

func TestBuildUpdateAndDeleteRequireFilters(t *testing.T) {
	t.Parallel()

	if _, err := domain.BuildUpdate(todos(), []string{"done"}, `{"done":true}`, domain.WriteQuery{}, false); !errors.Is(err, domain.ErrFilterRequired) {
		t.Fatalf("update: %v", err)
	}
	if _, err := domain.BuildDelete(todos(), domain.WriteQuery{}, false); !errors.Is(err, domain.ErrFilterRequired) {
		t.Fatalf("delete: %v", err)
	}

	q := domain.WriteQuery{Filters: []domain.Filter{{Column: "id", Op: domain.OpEq, Value: "7"}}}
	st, err := domain.BuildUpdate(todos(), []string{"done"}, `{"done":true}`, q, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `UPDATE "public"."todos" SET "done" = (SELECT "done" FROM json_populate_record(NULL::"public"."todos", ($1::text)::json)) ` +
		`WHERE "id" = ($2::text)::bigint`
	if st.SQL != want {
		t.Fatalf("update sql:\n got %s\nwant %s", st.SQL, want)
	}

	del, _ := domain.BuildDelete(todos(), q, true)
	if del.SQL != `WITH _r AS (DELETE FROM "public"."todos" WHERE "id" = ($1::text)::bigint RETURNING "id", "user_id", "title", "done", "due") SELECT coalesce(json_agg(_r), '[]'::json)::text FROM _r` {
		t.Fatalf("delete sql = %s", del.SQL)
	}
}

func TestParseRows(t *testing.T) {
	t.Parallel()

	cols, rows, err := domain.ParseRows([]byte(`[{"b":1,"a":0},{"a":2,"b":3}]`))
	if err != nil || !reflect.DeepEqual(cols, []string{"a", "b"}) || rows != `[{"b":1,"a":0},{"a":2,"b":3}]` {
		t.Fatalf("cols = %v rows = %s err = %v", cols, rows, err)
	}
	if _, _, err := domain.ParseRows([]byte(`[{"a":1},{"a":2,"b":3}]`)); !errors.Is(err, domain.ErrMixedColumns) {
		t.Fatalf("mixed keys: %v", err)
	}
	for _, bad := range []string{``, `[]`, `"x"`, `[1]`, `[null]`, `{"a":`} {
		if _, _, err := domain.ParseRows([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, _, err := domain.ParseObject([]byte(`[{"a":1}]`)); err == nil {
		t.Error("PATCH must take an object")
	}
}

func TestSchemaLookupOrder(t *testing.T) {
	t.Parallel()

	a := domain.NewTable("api", "items", domain.KindTable, true, false, nil)
	p := domain.NewTable("public", "items", domain.KindTable, true, false, nil)
	s := domain.NewSchema([]string{"api", "public"}, []*domain.Table{p, a})
	if got, _ := s.Table("items"); got.Schema != "api" {
		t.Fatalf("got %s, want first exposed schema", got)
	}
	if _, ok := s.Table("missing"); ok {
		t.Fatal("missing table found")
	}
}
