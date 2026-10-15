package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/port"
	"github.com/mini-base/mini-base/internal/modules/admin/service"
	"github.com/mini-base/mini-base/internal/platform/errs"
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
func (m *memAccounts) DeleteByID(_ context.Context, id string) (domain.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.byID[id]
	switch {
	case !ok:
		return domain.Account{}, domain.ErrAccountNotFound
	case len(m.byID) == 1:
		return domain.Account{}, domain.ErrLastAccount
	}
	delete(m.byID, id)
	return a, nil
}
func (m *memAccounts) Count(context.Context) (int, error)   { return len(m.byID), nil }
func (m *memAccounts) Delete(context.Context, string) error { return nil }
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
	ddl    [][]string
}

func (f *fakeCatalog) Schemas(context.Context, []string) ([]string, error) {
	return []string{"public"}, nil
}

func (f *fakeCatalog) ExecDDL(_ context.Context, stmts []string) error {
	f.ddl = append(f.ddl, stmts)
	return nil
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

type fakeData struct {
	last, count  domain.Statement
	importSQL    string
	batches      []string
	importFailAt int
}

func (f *fakeData) QueryText(_ context.Context, st domain.Statement) (string, bool, error) {
	f.last = st
	if strings.HasPrefix(st.SQL, "WITH _r AS (DELETE") {
		return "", false, nil // nothing matched
	}
	return `[{"id":1}]`, true, nil
}

func (f *fakeData) Count(_ context.Context, st domain.Statement) (int64, error) {
	f.count = st
	return 1, nil
}

func (f *fakeData) Export(_ context.Context, st domain.Statement, emit func([]*string) error) (int64, error) {
	f.last = st
	one, text := "1", `say "hi", ok`
	for _, row := range [][]*string{{&one, &text}, {&one, nil}} {
		if err := emit(row); err != nil {
			return 0, err
		}
	}
	return 2, nil
}

func (f *fakeData) Import(_ context.Context, sql string, batches []string) (int64, int, error) {
	f.importSQL, f.batches = sql, batches
	if f.importFailAt > 0 && len(batches) > f.importFailAt {
		return 0, f.importFailAt, errs.Invalid("admin.database_error", `invalid input syntax for type bigint: "x"`)
	}
	return int64(len(batches)), 0, nil
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

type memAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (m *memAudit) Append(_ context.Context, e domain.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

func (m *memAudit) List(context.Context, domain.AuditQuery) ([]domain.AuditEntry, error) {
	return m.entries, nil
}

func (m *memAudit) SQLHistory(context.Context, string, int) ([]domain.HistoryEntry, error) {
	return nil, nil
}

func (m *memAudit) DeleteBefore(context.Context, time.Time) (int64, error) { return 0, nil }

func (m *memAudit) actions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.entries))
	for i, e := range m.entries {
		out[i] = e.Actor + " " + e.Action + " " + e.Target
	}
	return out
}

type memSnippets struct{ owner map[string]string }

func (m *memSnippets) List(context.Context, string) ([]domain.Snippet, error) { return nil, nil }
func (m *memSnippets) Create(_ context.Context, acct, name, q string) (domain.Snippet, error) {
	m.owner[name] = acct
	return domain.Snippet{ID: "s1", Name: name, Query: q}, nil
}
func (m *memSnippets) Update(_ context.Context, acct, id, name, q string) (domain.Snippet, error) {
	return domain.Snippet{ID: id, Name: name, Query: q}, nil
}
func (m *memSnippets) Delete(context.Context, string, string) error { return nil }

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func newConsole(cat *fakeCatalog, data *fakeData, sql fakeSQL, ev *events, withUsers bool) *service.Console {
	c, _ := newConsoleAudited(cat, data, sql, ev, withUsers)
	return c
}

func newConsoleAudited(cat *fakeCatalog, data *fakeData, sql fakeSQL, ev *events, withUsers bool) (*service.Console, *memAudit) {
	users := func() ports.UserDirectory { return nil }
	if withUsers {
		users = func() ports.UserDirectory { return fakeDir{} }
	}
	audit := &memAudit{}
	return service.NewConsole(service.ConsoleDeps{
		Catalog: cat, Data: data, SQL: sql, Events: ev, Snippets: &memSnippets{owner: map[string]string{}},
		Audit: service.NewAudit(audit, discard), Users: users,
		Settings: func() []service.Setting {
			return []service.Setting{
				{Name: "MINIBASE_ANON_KEY", Value: "anon-key"},
				{Name: "MINIBASE_SERVICE_KEY", Value: "svc-…wxyz", Secret: true},
				{Name: "MINIBASE_DATABASE_SCHEMAS", Value: "public, api"},
			}
		},
		Info: service.Info{Version: "test"}, MaxSQLRows: 100,
	}), audit
}

// alice is the signed-in admin in console tests.
var alice = service.WithActor(context.Background(), domain.Actor{AccountID: "id-alice@example.com", Email: "alice@example.com", IP: "10.0.0.1"})

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
	if _, err := c.Browse(ctx, "public", "missing", service.RowQuery{Limit: 10}); !errors.Is(err, domain.ErrTableNotFound) {
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

// ---- admin management and audit ----

func newAuditedAccounts(t *testing.T) (*service.Accounts, *memAudit) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	audit := &memAudit{}
	return newAccounts(t, &now).WithAudit(service.NewAudit(audit, discard)), audit
}

func TestManagingAdminsFromTheDashboard(t *testing.T) {
	t.Parallel()
	a, audit := newAuditedAccounts(t)
	me, _ := a.Create(ctx, "alice@example.com", "a strong admin password")
	asAlice := service.WithActor(ctx, domain.Actor{AccountID: me.ID, Email: me.Email})

	if err := a.DeleteAccount(asAlice, me.ID); !errors.Is(err, domain.ErrCannotDeleteSelf) {
		t.Fatalf("delete self: %v", err)
	}
	if _, err := a.ResetAccountPassword(asAlice, me.ID, ""); !errors.Is(err, domain.ErrUseChangePassword) {
		t.Fatalf("reset own password: %v", err)
	}

	bob, generated, err := a.CreateAccount(asAlice, "bob@example.com", "")
	if err != nil || domain.ValidatePassword(generated) != nil {
		t.Fatalf("create with generated password: %q %v", generated, err)
	}
	if _, err := a.SignIn(ctx, "bob@example.com", generated, domain.ClientInfo{}); err != nil {
		t.Fatalf("bob signs in with the generated password: %v", err)
	}
	pw, err := a.ResetAccountPassword(asAlice, bob.ID, "")
	if err != nil || pw == "" || pw == generated {
		t.Fatalf("reset: %q %v", pw, err)
	}
	if err := a.DeleteAccount(asAlice, bob.ID); err != nil {
		t.Fatal(err)
	}
	// Alice is now the only admin; even another actor cannot delete her.
	asCLI := service.WithActor(ctx, domain.CLIActor)
	if err := a.DeleteAccount(asCLI, me.ID); !errors.Is(err, domain.ErrLastAccount) {
		t.Fatalf("delete last admin: %v", err)
	}

	got := strings.Join(audit.actions(), "\n")
	for _, want := range []string{
		"unknown admin.create alice@example.com", // created without an actor in ctx
		"alice@example.com admin.create bob@example.com",
		"bob@example.com admin.sign_in ",
		"alice@example.com admin.password_reset bob@example.com",
		"alice@example.com admin.delete bob@example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("audit log lacks %q:\n%s", want, got)
		}
	}
}

func TestFailedSignInsAreAudited(t *testing.T) {
	t.Parallel()
	a, audit := newAuditedAccounts(t)
	_, _ = a.Create(ctx, "alice@example.com", "a strong admin password")
	_, _ = a.SignIn(ctx, " Alice@Example.com ", "wrong password!!", domain.ClientInfo{IP: "203.0.113.9"})
	_, _ = a.SignIn(ctx, "mallory@example.com", "whatever password", domain.ClientInfo{IP: "203.0.113.9"})
	acts := audit.actions()
	if len(acts) != 3 || acts[1] != "alice@example.com admin.sign_in_failed " || acts[2] != "mallory@example.com admin.sign_in_failed " {
		t.Fatalf("audit = %q", acts)
	}
	if audit.entries[1].IP != "203.0.113.9" || audit.entries[1].AccountID != nil {
		t.Fatalf("entry = %+v", audit.entries[1])
	}
}

func TestConsoleActionsAreAuditedWithTheActor(t *testing.T) {
	t.Parallel()
	ok := fakeSQL{run: domain.SQLRun{Results: []domain.SQLResult{{Command: "SELECT 1"}}, DurationMS: 4}}
	c, audit := newConsoleAudited(&fakeCatalog{table: todos(false)}, &fakeData{}, ok, &events{}, false)

	if _, err := c.RunSQL(alice, "select 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetRLS(alice, "public", "todos", true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InsertRow(alice, "public", "todos", map[string]json.RawMessage{"title": json.RawMessage(`"x"`)}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"alice@example.com sql.run ",
		"alice@example.com schema.rls_enable public.todos",
		"alice@example.com data.row_insert public.todos",
	}
	if got := audit.actions(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("audit = %q", got)
	}
	run := audit.entries[0]
	if run.Detail["query"] != "select 1" || run.Detail["ok"] != true || *run.AccountID != "id-alice@example.com" || run.IP != "10.0.0.1" {
		t.Fatalf("sql entry = %+v", run)
	}
}

// ---- table designer ----

func TestDesignerPreviewRunsNothing(t *testing.T) {
	t.Parallel()
	cat, ev := &fakeCatalog{table: todos(true)}, &events{}
	c, audit := newConsoleAudited(cat, &fakeData{}, fakeSQL{}, ev, true)
	spec := domain.TableSpec{Schema: "public", Name: "notes", PrimaryKey: "uuid", Timestamps: true, EnableRLS: true,
		Columns: []domain.ColumnSpec{{Name: "body", Type: "text"}}}

	res, err := c.CreateTable(alice, spec, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Preview || len(res.SQL) != 2 || len(cat.ddl) != 0 || len(ev.names) != 0 || len(audit.entries) != 0 {
		t.Fatalf("preview = %+v ddl = %v events = %v", res, cat.ddl, ev.names)
	}

	if _, err := c.CreateTable(alice, spec, false); !errors.Is(err, domain.ErrTableNotFound) {
		// The fake catalog only knows "todos", so reloading "notes" fails —
		// but only after the DDL ran, was announced and was audited.
		t.Fatalf("create: %v", err)
	}
	if len(cat.ddl) != 1 || len(ev.names) != 1 || audit.actions()[0] != "alice@example.com schema.table_create public.notes" {
		t.Fatalf("ddl = %v events = %v audit = %v", cat.ddl, ev.names, audit.actions())
	}

	spec.Schema = "nope"
	if _, err := c.CreateTable(alice, spec, true); !errors.Is(err, domain.ErrSchemaNotFound) {
		t.Fatalf("unknown schema: %v", err)
	}
}

func TestDesignerChecksReferences(t *testing.T) {
	t.Parallel()
	toUsers := domain.ColumnSpec{Name: "user_id", Type: "uuid", References: &domain.Reference{Schema: "auth", Table: "users", Column: "id", OnDelete: "cascade"}}

	withoutAuth := newConsole(&fakeCatalog{table: todos(true)}, &fakeData{}, fakeSQL{}, &events{}, false)
	if _, err := withoutAuth.AddColumn(alice, "public", "todos", toUsers, true); err == nil {
		t.Fatal("auth.users accepted without the auth module")
	}
	withAuth := newConsole(&fakeCatalog{table: todos(true)}, &fakeData{}, fakeSQL{}, &events{}, true)
	res, err := withAuth.AddColumn(alice, "public", "todos", toUsers, true)
	if err != nil || !strings.Contains(res.SQL[0], `REFERENCES "auth"."users" ("id") ON DELETE CASCADE`) {
		t.Fatalf("res = %+v err = %v", res, err)
	}

	toTodos := domain.ColumnSpec{Name: "parent", Type: "bigint", Nullable: true, References: &domain.Reference{Schema: "public", Table: "todos", Column: "nope"}}
	if _, err := withAuth.AddColumn(alice, "public", "todos", toTodos, true); err == nil {
		t.Fatal("reference to a missing column accepted")
	}
}

// ---- data tools ----

func TestBrowseCountsWithFilters(t *testing.T) {
	t.Parallel()
	data := &fakeData{}
	c := newConsole(&fakeCatalog{table: todos(true)}, data, fakeSQL{}, &events{}, false)
	page, err := c.Browse(alice, "public", "todos", service.RowQuery{Filters: []domain.Filter{{Column: "title", Op: "contains", Value: "milk"}}})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || !page.TotalExact || len(data.count.Args) != 1 || len(data.last.Args) != 3 {
		t.Fatalf("page = %+v count = %+v browse = %+v", page, data.count, data.last)
	}
}

func TestExportWritesCSV(t *testing.T) {
	t.Parallel()
	c, audit := newConsoleAudited(&fakeCatalog{table: todos(true)}, &fakeData{}, fakeSQL{}, &events{}, false)
	var buf bytes.Buffer
	var name string
	n, err := c.Export(alice, "public", "todos", service.RowQuery{}, func(f string) io.Writer { name = f; return &buf })
	if err != nil || n != 2 || name != "todos.csv" {
		t.Fatalf("n = %d name = %q err = %v", n, name, err)
	}
	if want := "id,title\n1,\"say \"\"hi\"\", ok\"\n1,\n"; buf.String() != want {
		t.Fatalf("csv = %q, want %q", buf.String(), want)
	}
	if audit.actions()[0] != "alice@example.com data.export public.todos" {
		t.Fatalf("audit = %v", audit.actions())
	}

	opened := false
	if _, err := c.Export(alice, "public", "missing", service.RowQuery{}, func(string) io.Writer { opened = true; return &buf }); err == nil || opened {
		t.Fatal("output must not start before the request is validated")
	}
}

func TestImportBatchesAndExplainsFailures(t *testing.T) {
	t.Parallel()
	var csvFile strings.Builder
	csvFile.WriteString("\ufeffid,title\n") // spreadsheet BOM; id is generated and skipped
	for i := range 1200 {
		fmt.Fprintf(&csvFile, "%d,row %d\n", i, i)
	}

	data := &fakeData{}
	c, audit := newConsoleAudited(&fakeCatalog{table: todos(true)}, data, fakeSQL{}, &events{}, false)
	res, err := c.Import(alice, "public", "todos", strings.NewReader(csvFile.String()), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.batches) != 3 || strings.Join(res.Ignored, ",") != "id" || strings.Join(res.Columns, ",") != "title" {
		t.Fatalf("res = %+v batches = %d", res, len(data.batches))
	}
	if !strings.HasPrefix(data.batches[0], `[{"title":"row 0"}`) {
		t.Fatalf("batch = %.60s", data.batches[0])
	}
	if audit.actions()[0] != "alice@example.com data.import public.todos" {
		t.Fatalf("audit = %v", audit.actions())
	}

	data.importFailAt = 1
	_, err = c.Import(alice, "public", "todos", strings.NewReader(csvFile.String()), true)
	if err == nil || !strings.Contains(err.Error(), "between lines 502 and 1001") || !strings.Contains(err.Error(), "Nothing was imported") {
		t.Fatalf("err = %v", err)
	}

	for _, bad := range []string{"", "title,nope\nx,y\n", "title\n", "title\n\"unterminated\n"} {
		if _, err := c.Import(alice, "public", "todos", strings.NewReader(bad), true); err == nil {
			t.Errorf("import %q accepted", bad)
		}
	}
}

func TestSnippetsNeedASignedInAdmin(t *testing.T) {
	t.Parallel()
	c := newConsole(&fakeCatalog{table: todos(true)}, &fakeData{}, fakeSQL{}, &events{}, false)
	if _, err := c.SaveSnippet(ctx, "", "q", "select 1"); !errors.Is(err, domain.ErrNotSignedIn) {
		t.Fatalf("without actor: %v", err)
	}
	sn, err := c.SaveSnippet(alice, "", "  Active users ", "select 1")
	if err != nil || sn.Name != "Active users" {
		t.Fatalf("snippet = %+v err = %v", sn, err)
	}
	if _, err := c.SaveSnippet(alice, "", "x", "   "); err == nil {
		t.Fatal("empty snippet accepted")
	}
}

func TestSettingsViewExposesAnonKeyOnly(t *testing.T) {
	t.Parallel()
	v := newConsole(&fakeCatalog{table: todos(true)}, &fakeData{}, fakeSQL{}, &events{}, false).Settings()
	if v.AnonKey != "anon-key" || v.ServiceKey != "svc-…wxyz" || strings.Join(v.RESTSchemas, ",") != "public,api" {
		t.Fatalf("settings = %+v", v)
	}
}
