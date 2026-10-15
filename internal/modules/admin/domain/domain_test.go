package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
)

func todos() *domain.Table {
	return &domain.Table{
		TableSummary: domain.TableSummary{Schema: "public", Name: "todos", Kind: "table"},
		Columns: []domain.Column{
			{Name: "id", Type: "bigint", Generated: true, PrimaryKey: true},
			{Name: "title", Type: "text"},
			{Name: "done", Type: "boolean"},
		},
	}
}

func TestPasswordPolicy(t *testing.T) {
	t.Parallel()
	if err := domain.ValidatePassword("short-pass"); !errors.Is(err, domain.ErrWeakPassword) {
		t.Fatalf("got %v", err)
	}
	if err := domain.ValidatePassword("a-long-enough-pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := domain.NormalizeEmail("Ops <ops@x.com>"); !errors.Is(err, domain.ErrInvalidEmail) {
		t.Fatalf("got %v", err)
	}
}

func TestBrowseOrdersByPrimaryKey(t *testing.T) {
	t.Parallel()
	st, err := domain.BuildBrowse(todos(), nil, nil, 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT coalesce(json_agg(_r), '[]'::json)::text FROM (SELECT * FROM "public"."todos" ORDER BY "id" LIMIT $1 OFFSET $2) _r`
	if st.SQL != want || st.Args[0] != 50 || st.Args[1] != 100 {
		t.Fatalf("sql = %s args = %v", st.SQL, st.Args)
	}
	if _, err := domain.BuildBrowse(todos(), nil, &domain.SortTerm{Column: `x"; drop`}, 1, 0); err == nil {
		t.Fatal("unknown sort column accepted")
	}
}

func TestUpdateUsesKeyAndRejectsGenerated(t *testing.T) {
	t.Parallel()
	st, err := domain.BuildUpdate(todos(), []string{"id"}, `{"id":7}`, []string{"done"}, `{"done":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, `WHERE ("id") = (SELECT "id" FROM json_populate_record(NULL::"public"."todos", ($2::text)::json))`) {
		t.Fatalf("sql = %s", st.SQL)
	}
	if _, err := domain.BuildUpdate(todos(), []string{"id"}, `{"id":7}`, []string{"id"}, `{"id":8}`); err == nil {
		t.Fatal("writing a generated column must be refused")
	}
	if _, err := domain.BuildUpdate(todos(), nil, `{}`, []string{"done"}, `{"done":true}`); !errors.Is(err, domain.ErrMissingKey) {
		t.Fatalf("missing key: %v", err)
	}
}

func TestNoPrimaryKeyMeansReadOnly(t *testing.T) {
	t.Parallel()
	tb := todos()
	for i := range tb.Columns {
		tb.Columns[i].PrimaryKey = false
	}
	if _, err := domain.BuildDelete(tb, nil, `{}`); !errors.Is(err, domain.ErrNotEditable) {
		t.Fatalf("got %v", err)
	}
}

func TestInsertDefaultValues(t *testing.T) {
	t.Parallel()
	st, err := domain.BuildInsert(todos(), nil, `{}`)
	if err != nil || !strings.Contains(st.SQL, "DEFAULT VALUES") {
		t.Fatalf("sql = %s err = %v", st.SQL, err)
	}
}

func TestChangesSchema(t *testing.T) {
	t.Parallel()
	run := domain.SQLRun{Results: []domain.SQLResult{{Command: "SELECT 1"}, {Command: "ALTER TABLE"}}}
	if !run.ChangesSchema() {
		t.Fatal("ALTER should count as a schema change")
	}
	if (domain.SQLRun{Results: []domain.SQLResult{{Command: "INSERT 0 1"}}}).ChangesSchema() {
		t.Fatal("INSERT is not a schema change")
	}
}
