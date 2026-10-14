package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/port"
	"github.com/mini-base/mini-base/internal/modules/admin/service"
	"github.com/mini-base/mini-base/pkg/ports"
)

// ---- account fakes ----

type memAccounts struct {
	mu     sync.Mutex
	byID   map[string]domain.Account
	hashes map[string]string
}

func newMemAccounts() *memAccounts {
	return &memAccounts{byID: map[string]domain.Account{}, hashes: map[string]string{}}
}

func (m *memAccounts) Create(_ context.Context, email, hash string) (domain.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.byID {
		if a.Email == email {
			return domain.Account{}, domain.ErrAccountExists
		}
	}
	a := domain.Account{ID: "id-" + email, Email: email}
	m.byID[a.ID], m.hashes[a.ID] = a, hash
	return a, nil
}

func (m *memAccounts) FindByEmail(_ context.Context, email string) (domain.Account, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.byID {
		if a.Email == email {
			return a, m.hashes[id], nil
		}
	}
	return domain.Account{}, "", domain.ErrAccountNotFound
}

func (m *memAccounts) FindByID(_ context.Context, id string) (domain.Account, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.byID[id]
	if !ok {
		return domain.Account{}, "", domain.ErrAccountNotFound
	}
	return a, m.hashes[id], nil
}

func (m *memAccounts) List(context.Context) ([]domain.Account, error) { return nil, nil }
func (m *memAccounts) Count(context.Context) (int, error)             { return len(m.byID), nil }
func (m *memAccounts) Delete(context.Context, string) error           { return nil }
func (m *memAccounts) SetPassword(_ context.Context, id, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hashes[id] = hash
	return nil
}
func (m *memAccounts) RecordSignIn(context.Context, string, time.Time) error { return nil }

type memSession struct {
	account string
	exp     time.Time
}

type memSessions struct {
	mu       sync.Mutex
	sessions map[string]memSession
	accounts *memAccounts
}

func (m *memSessions) Create(_ context.Context, h []byte, acct string, exp time.Time, _ domain.ClientInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(h)] = memSession{acct, exp}
	return nil
}

func (m *memSessions) Lookup(ctx context.Context, h []byte, now time.Time) (domain.Account, error) {
	m.mu.Lock()
	s, ok := m.sessions[string(h)]
	m.mu.Unlock()
	if !ok || !now.Before(s.exp) {
		return domain.Account{}, domain.ErrNotSignedIn
	}
	a, _, err := m.accounts.FindByID(ctx, s.account)
	return a, err
}

func (m *memSessions) Delete(_ context.Context, h []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, string(h))
	return nil
}

func (m *memSessions) DeleteForAccount(_ context.Context, acct string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		if s.account == acct {
			delete(m.sessions, k)
		}
	}
	return nil
}

func (m *memSessions) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, nil }

type plainHasher struct{}

func (plainHasher) Hash(_ context.Context, pw string) (string, error) { return "h:" + pw, nil }
func (plainHasher) Verify(_ context.Context, pw, enc string) (bool, error) {
	return enc == "h:"+pw, nil
}

var ctx = context.Background()

func newAccounts(t *testing.T, now *time.Time) *service.Accounts {
	t.Helper()
	accts := newMemAccounts()
	a, err := service.NewAccounts(accts, &memSessions{sessions: map[string]memSession{}, accounts: accts}, plainHasher{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return a.WithClock(func() time.Time { return *now })
}

// ---- account tests ----

func TestSignInSessionLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	a := newAccounts(t, &now)

	if _, err := a.Create(ctx, "ops@example.com", "short"); !errors.Is(err, domain.ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}
	if _, err := a.Create(ctx, "Ops@Example.com", "a strong admin password"); err != nil {
		t.Fatal(err)
	}

	if _, err := a.SignIn(ctx, "ops@example.com", "wrong password!!", domain.ClientInfo{}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := a.SignIn(ctx, "nobody@example.com", "a strong admin password", domain.ClientInfo{}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("unknown admin: %v", err)
	}

	s, err := a.SignIn(ctx, "OPS@example.com", "a strong admin password", domain.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if acct, err := a.Authenticate(ctx, s.Token); err != nil || acct.Email != "ops@example.com" {
		t.Fatalf("authenticate: %v %v", acct, err)
	}

	now = now.Add(2 * time.Hour) // past the 1h TTL
	if _, err := a.Authenticate(ctx, s.Token); !errors.Is(err, domain.ErrNotSignedIn) {
		t.Fatalf("expired session: %v", err)
	}
}

func TestChangePasswordEndsSessions(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	a := newAccounts(t, &now)
	acct, _ := a.Create(ctx, "ops@example.com", "a strong admin password")
	s, _ := a.SignIn(ctx, "ops@example.com", "a strong admin password", domain.ClientInfo{})

	if err := a.ChangePassword(ctx, acct.ID, "not the password", "another strong password"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("wrong current: %v", err)
	}
	if err := a.ChangePassword(ctx, acct.ID, "a strong admin password", "another strong password"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, s.Token); !errors.Is(err, domain.ErrNotSignedIn) {
		t.Fatal("old session must end after a password change")
	}
	if _, err := a.SignIn(ctx, "ops@example.com", "another strong password", domain.ClientInfo{}); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedPasswordIsStrong(t *testing.T) {
	t.Parallel()
	pw, err := service.GeneratePassword()
	if err != nil || domain.ValidatePassword(pw) != nil || len(pw) < 20 {
		t.Fatalf("pw = %q err = %v", pw, err)
	}
}

// ---- console fakes ----

type fakeCatalog struct {
	table  *domain.Table
	rlsSet *bool
}

func (f *fakeCatalog) ListTables(context.Context, []string) ([]domain.TableSummary, error) {
	return []domain.TableSummary{f.table.TableSummary}, nil
}

func (f *fakeCatalog) Table(_ context.Context, schema, name string, _ []string) (*domain.Table, error) {
	if schema != f.table.Schema || name != f.table.Name {
		return nil, domain.ErrTableNotFound
	}
	c := *f.table
	return &c, nil
}

func (f *fakeCatalog) SetRLS(_ context.Context, _ *domain.Table, on bool) error {
	f.rlsSet = &on
	f.table.RLSEnabled = on
	return nil
}

func (f *fakeCatalog) Stats(context.Context) (port.DatabaseStats, error) {
	return port.DatabaseStats{Name: "minibase", Version: "16.4"}, nil
}

type fakeData struct{ last domain.Statement }

func (f *fakeData) QueryText(_ context.Context, st domain.Statement) (string, bool, error) {
	f.last = st
	if strings.HasPrefix(st.SQL, "WITH _r AS (DELETE") {
		return "", false, nil // nothing matched
	}
	return `[{"id":1}]`, true, nil
}

func (f *fakeData) CountRows(context.Context, *domain.Table) (int64, bool, error) {
	return 1, true, nil
}

type fakeSQL struct{ run domain.SQLRun }

func (f fakeSQL) Run(context.Context, string, int) (domain.SQLRun, error) { return f.run, nil }

type events struct{ names []string }

func (e *events) Publish(_ context.Context, name string, _ any) { e.names = append(e.names, name) }

type fakeDir struct{ ports.UserDirectory }

func (fakeDir) ListUsers(context.Context, ports.UserQuery) ([]ports.UserSummary, int, error) {
	return nil, 42, nil
}

func todos(rls bool) *domain.Table {
	return &domain.Table{
		TableSummary: domain.TableSummary{Schema: "public", Name: "todos", Kind: "table", RLSEnabled: rls},
		Columns:      []domain.Column{{Name: "id", Type: "bigint", PrimaryKey: true, Generated: true}, {Name: "title", Type: "text"}},
	}
}

func newConsole(cat *fakeCatalog, data *fakeData, sql fakeSQL, ev *events, withUsers bool) *service.Console {
	users := func() ports.UserDirectory { return nil }
	if withUsers {
		users = func() ports.UserDirectory { return fakeDir{} }
	}
	return service.NewConsole(cat, data, sql, ev, users, service.Info{Version: "test"}, 100)
}

// ---- console tests ----

func TestOverviewFlagsTablesWithoutRLS(t *testing.T) {
	t.Parallel()
	c := newConsole(&fakeCatalog{table: todos(false)}, &fakeData{}, fakeSQL{}, &events{}, true)
	ov, err := c.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.NoRLS) != 1 || ov.Users == nil || *ov.Users != 42 || !ov.Features["users"] {
		t.Fatalf("overview = %+v", ov)
	}

	c = newConsole(&fakeCatalog{table: todos(true)}, &fakeData{}, fakeSQL{}, &events{}, false)
	ov, _ = c.Overview(ctx)
	if len(ov.NoRLS) != 0 || ov.Users != nil || ov.Features["users"] {
		t.Fatalf("overview without auth = %+v", ov)
	}
	if _, err := c.Users(); !errors.Is(err, service.ErrUsersUnavailable) {
		t.Fatalf("users without auth: %v", err)
	}
}

func TestSetRLSAnnouncesSchemaChange(t *testing.T) {
	t.Parallel()
	cat, ev := &fakeCatalog{table: todos(false)}, &events{}
	tb, err := newConsole(cat, &fakeData{}, fakeSQL{}, ev, false).SetRLS(ctx, "public", "todos", true)
	if err != nil || !tb.RLSEnabled || cat.rlsSet == nil || !*cat.rlsSet {
		t.Fatalf("table = %+v err = %v", tb, err)
	}
	if len(ev.names) != 1 || ev.names[0] != ports.EventSchemaChanged {
		t.Fatalf("events = %v", ev.names)
	}
}

func TestRowEditing(t *testing.T) {
	t.Parallel()
	data := &fakeData{}
	c := newConsole(&fakeCatalog{table: todos(true)}, data, fakeSQL{}, &events{}, false)

	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	if _, err := c.InsertRow(ctx, "public", "todos", map[string]json.RawMessage{"title": raw(`"milk"`)}); err != nil {
		t.Fatal(err)
	}
	if data.last.Args[0] != `{"title":"milk"}` {
		t.Fatalf("insert args = %v", data.last.Args)
	}

	_, err := c.UpdateRow(ctx, "public", "todos", map[string]json.RawMessage{"id": raw("1")}, map[string]json.RawMessage{"title": raw(`"eggs"`)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(data.last.SQL), []byte(`WHERE ("id") = `)) {
		t.Fatalf("update sql = %s", data.last.SQL)
	}

	if _, err := c.DeleteRow(ctx, "public", "todos", map[string]json.RawMessage{"id": raw("999")}); !errors.Is(err, domain.ErrRowNotFound) {
		t.Fatalf("delete missing row: %v", err)
	}
	if _, err := c.Browse(ctx, "public", "missing", nil, 10, 0); !errors.Is(err, domain.ErrTableNotFound) {
		t.Fatalf("missing table: %v", err)
	}
}

func TestRunSQLAnnouncesDDLOnly(t *testing.T) {
	t.Parallel()
	ev := &events{}
	ddl := fakeSQL{run: domain.SQLRun{Results: []domain.SQLResult{{Command: "CREATE TABLE"}}}}
	if _, err := newConsole(&fakeCatalog{table: todos(true)}, &fakeData{}, ddl, ev, false).RunSQL(ctx, "create table x()"); err != nil {
		t.Fatal(err)
	}
	sel := fakeSQL{run: domain.SQLRun{Results: []domain.SQLResult{{Command: "SELECT 1"}}}}
	if _, err := newConsole(&fakeCatalog{table: todos(true)}, &fakeData{}, sel, ev, false).RunSQL(ctx, "select 1"); err != nil {
		t.Fatal(err)
	}
	if len(ev.names) != 1 {
		t.Fatalf("events = %v (only DDL should announce)", ev.names)
	}
	if _, err := newConsole(&fakeCatalog{table: todos(true)}, &fakeData{}, sel, ev, false).RunSQL(ctx, "   "); err == nil {
		t.Fatal("empty query accepted")
	}
}
