package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
)

func ptr(s string) *string { return &s }

func notes() *domain.Table {
	t := todos()
	t.Columns = append(t.Columns,
		domain.Column{Name: "owner", Type: "uuid"},
		domain.Column{Name: "meta", Type: "jsonb", Nullable: true},
	)
	return t
}

func TestCreateTableSQL(t *testing.T) {
	t.Parallel()
	stmts, err := domain.BuildCreateTable(domain.TableSpec{
		Schema: "public", Name: "todos", PrimaryKey: "bigint", Timestamps: true, EnableRLS: true,
		Columns: []domain.ColumnSpec{
			{Name: "title", Type: "text", Default: &domain.ColumnDefault{Value: ptr("it's new")}},
			{Name: "user_id", Type: "uuid", Default: &domain.ColumnDefault{Expression: "auth.uid()"},
				References: &domain.Reference{Schema: "auth", Table: "users", Column: "id", OnDelete: "cascade"}},
			{Name: "tags", Type: "text[]", Nullable: true, Unique: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE TABLE "public"."todos" (
  "id" bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  "title" text NOT NULL DEFAULT 'it''s new'::text,
  "user_id" uuid NOT NULL DEFAULT auth.uid() REFERENCES "auth"."users" ("id") ON DELETE CASCADE,
  "tags" text[] UNIQUE,
  "created_at" timestamptz NOT NULL DEFAULT now()
)`
	if len(stmts) != 2 || stmts[0] != want || stmts[1] != `ALTER TABLE "public"."todos" ENABLE ROW LEVEL SECURITY` {
		t.Fatalf("got:\n%s", strings.Join(stmts, ";\n"))
	}
}

func TestCreateTableRejectsUnsafeInput(t *testing.T) {
	t.Parallel()
	base := func() domain.TableSpec {
		return domain.TableSpec{Schema: "public", Name: "t", Columns: []domain.ColumnSpec{{Name: "a", Type: "text"}}}
	}
	cases := map[string]func(*domain.TableSpec){
		"quote in name":     func(s *domain.TableSpec) { s.Name = `t"; drop table x; --` },
		"uppercase name":    func(s *domain.TableSpec) { s.Name = "Todos" },
		"type injection":    func(s *domain.TableSpec) { s.Columns[0].Type = "text; drop table x" },
		"default injection": func(s *domain.TableSpec) { s.Columns[0].Default = &domain.ColumnDefault{Expression: "pg_sleep(10)"} },
		"both defaults": func(s *domain.TableSpec) {
			s.Columns[0].Default = &domain.ColumnDefault{Expression: "now()", Value: ptr("x")}
		},
		"duplicate id": func(s *domain.TableSpec) { s.Columns[0].Name = "id" },
		"bad on delete": func(s *domain.TableSpec) {
			s.Columns[0].References = &domain.Reference{Schema: "a", Table: "b", Column: "c", OnDelete: "explode"}
		},
		"set null not null": func(s *domain.TableSpec) {
			s.Columns[0].References = &domain.Reference{Schema: "a", Table: "b", Column: "c", OnDelete: "set null"}
		},
		"no columns": func(s *domain.TableSpec) { s.PrimaryKey = "none"; s.Columns = nil },
	}
	for name, mutate := range cases {
		s := base()
		mutate(&s)
		if _, err := domain.BuildCreateTable(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestColumnAndDropStatements(t *testing.T) {
	t.Parallel()
	add, err := domain.BuildAddColumn(todos(), domain.ColumnSpec{Name: "due", Type: "date", Nullable: true})
	if err != nil || add[0] != `ALTER TABLE "public"."todos" ADD COLUMN "due" date` {
		t.Fatalf("add = %v %v", add, err)
	}
	if _, err := domain.BuildAddColumn(todos(), domain.ColumnSpec{Name: "title", Type: "text"}); !errors.Is(err, domain.ErrDuplicateColumn) {
		t.Fatalf("duplicate: %v", err)
	}
	drop, err := domain.BuildDropColumn(todos(), "done")
	if err != nil || drop[0] != `ALTER TABLE "public"."todos" DROP COLUMN "done"` {
		t.Fatalf("drop = %v %v", drop, err)
	}
	if _, err := domain.BuildDropTable(todos(), "todo"); !errors.Is(err, domain.ErrConfirmName) {
		t.Fatalf("confirm: %v", err)
	}
	if st, _ := domain.BuildDropTable(todos(), "todos"); st[0] != `DROP TABLE "public"."todos"` {
		t.Fatalf("drop table = %v", st)
	}
}

func TestPolicyTemplates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		spec domain.PolicySpec
		want string
	}{
		{domain.PolicySpec{Template: "public_read"},
			`CREATE POLICY "Anyone can read" ON "public"."todos" AS PERMISSIVE FOR SELECT TO minibase_anon, minibase_authenticated USING (true)`},
		{domain.PolicySpec{Template: "signed_in_insert", Name: "inserts"},
			`CREATE POLICY "inserts" ON "public"."todos" AS PERMISSIVE FOR INSERT TO minibase_authenticated WITH CHECK (true)`},
		{domain.PolicySpec{Template: "owner_all", Column: "owner"},
			`CREATE POLICY "Owners manage their own rows" ON "public"."todos" AS PERMISSIVE FOR ALL TO minibase_authenticated USING ("owner" = auth.uid()) WITH CHECK ("owner" = auth.uid())`},
		{domain.PolicySpec{Template: "custom", Name: `say "hi"`, Command: "update", Roles: []string{"authenticated"}, Using: "done = false"},
			`CREATE POLICY "say ""hi""" ON "public"."todos" AS PERMISSIVE FOR UPDATE TO minibase_authenticated USING (done = false)`},
	}
	for _, c := range cases {
		got, err := domain.BuildPolicy(notes(), c.spec)
		if err != nil || got != c.want {
			t.Errorf("%s:\n got %s (%v)\nwant %s", c.spec.Template, got, err, c.want)
		}
	}

	bad := []domain.PolicySpec{
		{Template: "nope"},
		{Template: "owner_all", Column: "title"}, // not uuid
		{Template: "owner_read", Column: "missing"},
		{Template: "custom", Command: "SELECT", Roles: []string{"anon"}}, // no USING
		{Template: "custom", Command: "INSERT", Roles: []string{"anon"}, Using: "true", WithCheck: "true"}, // USING on insert
		{Template: "custom", Command: "SELECT", Roles: []string{"postgres"}, Using: "true"},                // unknown role
		{Template: "custom", Command: "TRUNCATE", Roles: []string{"anon"}, Using: "true"},
	}
	for _, s := range bad {
		if _, err := domain.BuildPolicy(notes(), s); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}

	tb := notes()
	tb.Policies = []domain.Policy{{Name: "Anyone can read"}}
	if st, err := domain.BuildDropPolicy(tb, "Anyone can read"); err != nil || st != `DROP POLICY "Anyone can read" ON "public"."todos"` {
		t.Fatalf("drop = %s %v", st, err)
	}
	if _, err := domain.BuildDropPolicy(tb, "other"); !errors.Is(err, domain.ErrPolicyNotFound) {
		t.Fatalf("drop missing: %v", err)
	}
}

func TestFiltersBindValuesAndCastByColumnType(t *testing.T) {
	t.Parallel()
	f, err := domain.ParseFilter("title:contains:a:b")
	if err != nil || f.Value != "a:b" {
		t.Fatalf("parse = %+v %v", f, err)
	}
	st, err := domain.BuildBrowse(todos(), []domain.Filter{
		{Column: "done", Op: "eq", Value: "true"},
		f,
		{Column: "id", Op: "not_null"},
	}, &domain.SortTerm{Column: "title", Desc: true}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT coalesce(json_agg(_r), '[]'::json)::text FROM (SELECT * FROM "public"."todos" WHERE "done" = ($3::text)::boolean AND strpos(lower("title"::text), lower($4::text)) > 0 AND "id" IS NOT NULL ORDER BY "title" DESC NULLS LAST LIMIT $1 OFFSET $2) _r`
	if st.SQL != want || len(st.Args) != 4 || st.Args[2] != "true" || st.Args[3] != "a:b" {
		t.Fatalf("sql = %s\nargs = %v", st.SQL, st.Args)
	}
	count, _ := domain.BuildCount(todos(), []domain.Filter{{Column: "done", Op: "neq", Value: "true"}})
	if count.SQL != `SELECT count(*) FROM "public"."todos" WHERE "done" IS DISTINCT FROM ($1::text)::boolean` {
		t.Fatalf("count = %s", count.SQL)
	}
	for _, bad := range []domain.Filter{{Column: "nope", Op: "eq"}, {Column: "title", Op: "regex"}} {
		if _, err := domain.BuildCount(todos(), []domain.Filter{bad}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if _, err := domain.ParseFilter("justacolumn"); err == nil {
		t.Error("filter without op accepted")
	}
}

func TestExportSelectsTextInTableOrder(t *testing.T) {
	t.Parallel()
	st, header, err := domain.BuildExport(todos(), nil, nil)
	if err != nil || strings.Join(header, ",") != "id,title,done" ||
		st.SQL != `SELECT "id"::text, "title"::text, "done"::text FROM "public"."todos" ORDER BY "id"` {
		t.Fatalf("sql = %s header = %v err = %v", st.SQL, header, err)
	}
}

func TestImportPlan(t *testing.T) {
	t.Parallel()
	p, err := domain.PlanImport(notes(), []string{"\ufeffid", " title ", "meta", "done"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Columns, ",") != "title,meta,done" || strings.Join(p.Ignored, ",") != "id" {
		t.Fatalf("plan = %+v", p)
	}
	batch, err := p.Encode([][]string{{"1", "milk", `{"a":1}`, ""}, {"2", "", "not json", "t"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"done":null,"meta":{"a":1},"title":"milk"},{"done":"t","meta":"not json","title":null}]`; batch != want {
		t.Fatalf("batch = %s\nwant  %s", batch, want)
	}
	asStrings, _ := p.Encode([][]string{{"1", "", "", ""}}, false)
	if asStrings != `[{"done":"","meta":"","title":""}]` {
		t.Fatalf("empty as string = %s", asStrings)
	}
	if !strings.Contains(p.Statement(), `json_populate_recordset(NULL::"public"."todos", ($1::text)::json)`) {
		t.Fatalf("stmt = %s", p.Statement())
	}
	for _, header := range [][]string{{}, {"title", "title"}, {"nope"}, {"id"}} {
		if _, err := domain.PlanImport(notes(), header); err == nil {
			t.Errorf("header %q accepted", header)
		}
	}
}

func TestAuditQueryDefaults(t *testing.T) {
	t.Parallel()
	q := domain.AuditQuery{Limit: 5000, Actor: " Alice@Example.com "}.Normalize()
	if q.Limit != 50 || q.Actor != "alice@example.com" {
		t.Fatalf("q = %+v", q)
	}
	long := strings.Repeat("é", domain.MaxAuditedSQL) // 2 bytes each
	if got := domain.TruncateSQL(long); len(got) > domain.MaxAuditedSQL+20 || !strings.HasSuffix(got, "(truncated)") {
		t.Fatalf("truncated to %d bytes", len(got))
	}
}
