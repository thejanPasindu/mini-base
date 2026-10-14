package service_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/mini-base/mini-base/internal/modules/database/domain"
	"github.com/mini-base/mini-base/internal/modules/database/service"
	"github.com/mini-base/mini-base/pkg/ports"
)

type fakeLoader struct {
	tables []*domain.Table
	loads  int
}

func (f *fakeLoader) Load(_ context.Context, schemas []string) (*domain.Schema, error) {
	f.loads++
	return domain.NewSchema(schemas, f.tables), nil
}

type fakeExec struct {
	mu   sync.Mutex
	last domain.Statement
}

func (f *fakeExec) Query(_ context.Context, _ ports.Principal, st domain.Statement) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = st
	return []byte(`[]`), nil
}

func (f *fakeExec) Exec(_ context.Context, _ ports.Principal, st domain.Statement) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = st
	return 1, nil
}

var (
	cols    = []domain.Column{{Name: "id", Type: "bigint"}, {Name: "title", Type: "text"}}
	todos   = domain.NewTable("public", "todos", domain.KindTable, true, false, cols)
	open    = domain.NewTable("public", "open", domain.KindTable, false, false, cols)
	view    = domain.NewTable("public", "v_owner", domain.KindView, false, false, cols)
	safe    = domain.NewTable("public", "v_safe", domain.KindView, false, true, cols)
	matview = domain.NewTable("public", "stats", domain.KindMaterializedView, false, false, cols)

	anon    = ports.Principal{Role: ports.RoleAnon}
	user    = ports.Principal{Role: ports.RoleAuthenticated, UserID: "u"}
	svcRole = ports.Principal{Role: ports.RoleService}
)

func newSvc(t *testing.T, opts service.Options, tables ...*domain.Table) (*service.Service, *fakeLoader, *fakeExec) {
	t.Helper()
	l, e := &fakeLoader{tables: tables}, &fakeExec{}
	if opts.Schemas == nil {
		opts.Schemas = []string{"public"}
	}
	if opts.MaxRows == 0 {
		opts.MaxRows = 100
	}
	s := service.New(l, e, opts)
	if err := s.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, l, e
}

func read(s *service.Service, p ports.Principal, table string) error {
	_, err := s.Read(context.Background(), service.Request{Principal: p, Table: table, Query: url.Values{}})
	return err
}

func TestSecureByDefaultGuard(t *testing.T) {
	t.Parallel()
	s, _, _ := newSvc(t, service.Options{}, todos, open, view, safe, matview)

	tests := []struct {
		name  string
		p     ports.Principal
		table string
		want  error
	}{
		{"rls table, user", user, "todos", nil},
		{"rls table, anon", anon, "todos", nil},
		{"no rls, user", user, "open", service.ErrRLSDisabled},
		{"no rls, service key", svcRole, "open", nil},
		{"owner view, anon", anon, "v_owner", service.ErrUnsafeView},
		{"security_invoker view, anon", anon, "v_safe", nil},
		{"materialized view, user", user, "stats", service.ErrNotExposable},
		{"materialized view, service", svcRole, "stats", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := read(s, tt.p, tt.table)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestGuardMessageExplainsTheFix(t *testing.T) {
	t.Parallel()
	s, _, _ := newSvc(t, service.Options{}, open)
	if err := read(s, user, "open"); err == nil || !strings.Contains(err.Error(), "ENABLE ROW LEVEL SECURITY") {
		t.Fatalf("err = %v", err)
	}
}

func TestAllowNoRLSOptOut(t *testing.T) {
	t.Parallel()
	s, _, _ := newSvc(t, service.Options{AllowNoRLS: true}, open)
	if err := read(s, anon, "open"); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownTableTriggersOneThrottledReload(t *testing.T) {
	t.Parallel()
	s, l, _ := newSvc(t, service.Options{}, todos)

	if err := read(s, user, "created_later"); !errors.Is(err, service.ErrTableNotFound) {
		t.Fatalf("err = %v", err)
	}
	if l.loads != 2 {
		t.Fatalf("loads = %d, want initial + 1 reload", l.loads)
	}
	if err := read(s, user, "created_later"); !errors.Is(err, service.ErrTableNotFound) {
		t.Fatal(err)
	}
	if l.loads != 2 {
		t.Fatalf("reload not throttled: loads = %d", l.loads)
	}
}

func TestNewTableFoundAfterReload(t *testing.T) {
	t.Parallel()
	s, l, _ := newSvc(t, service.Options{})
	l.tables = []*domain.Table{todos} // table created after startup
	if err := read(s, user, "todos"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateMinimalVsRepresentation(t *testing.T) {
	t.Parallel()
	s, _, e := newSvc(t, service.Options{}, todos)
	ctx := context.Background()

	res, err := s.Create(ctx, service.Request{Principal: user, Table: "todos", Query: url.Values{}, Body: []byte(`{"title":"x"}`)})
	if err != nil || res.Body != nil || res.Affected != 1 || e.last.Returns {
		t.Fatalf("minimal: res=%+v err=%v", res, err)
	}

	res, err = s.Create(ctx, service.Request{Principal: user, Table: "todos", Query: url.Values{"select": {"id"}}, Body: []byte(`{"title":"x"}`), Return: true})
	if err != nil || string(res.Body) != "[]" || !strings.Contains(e.last.SQL, `RETURNING "id"`) {
		t.Fatalf("representation: res=%+v err=%v sql=%s", res, err, e.last.SQL)
	}
}

func TestDeleteWithoutFilterRefused(t *testing.T) {
	t.Parallel()
	s, _, _ := newSvc(t, service.Options{}, todos)
	_, err := s.Delete(context.Background(), service.Request{Principal: svcRole, Table: "todos", Query: url.Values{}})
	if !errors.Is(err, domain.ErrFilterRequired) {
		t.Fatalf("err = %v", err)
	}
}
