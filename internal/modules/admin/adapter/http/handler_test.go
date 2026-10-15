package http_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	adminhttp "github.com/mini-base/mini-base/internal/modules/admin/adapter/http"
	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/service"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/internal/platform/ratelimit"
	"github.com/mini-base/mini-base/pkg/ports"
)

const goodToken = "session-token"

// The fakes embed the interfaces so tests only implement what they use;
// calling anything else panics.
type fakeAccounts struct {
	adminhttp.Accounts
	signedOut string
	deletedBy domain.Actor
}

func (f *fakeAccounts) DeleteAccount(ctx context.Context, id string) error {
	f.deletedBy, _ = service.ActorFrom(ctx)
	if id == "a1" {
		return domain.ErrCannotDeleteSelf
	}
	return nil
}

func (f *fakeAccounts) SignIn(_ context.Context, email, pw string, _ domain.ClientInfo) (service.Session, error) {
	if pw != "correct password" {
		return service.Session{}, domain.ErrInvalidCredentials
	}
	return service.Session{Token: goodToken, ExpiresAt: time.Now().Add(time.Hour), Account: domain.Account{Email: email}}, nil
}

func (f *fakeAccounts) Authenticate(_ context.Context, token string) (domain.Account, error) {
	if token != goodToken {
		return domain.Account{}, domain.ErrNotSignedIn
	}
	return domain.Account{ID: "a1", Email: "ops@example.com"}, nil
}

func (f *fakeAccounts) SignOut(_ context.Context, token string) error {
	f.signedOut = token
	return nil
}

func (f *fakeAccounts) ChangePassword(context.Context, string, string, string) error { return nil }

type fakeConsole struct {
	adminhttp.Console
	sqlRuns     int
	query       service.RowQuery
	preview     bool
	emptyAsNull bool
	imported    string
}

func (f *fakeConsole) Overview(context.Context) (service.Overview, error) {
	return service.Overview{Server: service.Info{Version: "test"}}, nil
}
func (f *fakeConsole) Tables(context.Context) ([]domain.TableSummary, error) { return nil, nil }
func (f *fakeConsole) Table(context.Context, string, string) (*domain.Table, error) {
	return nil, domain.ErrTableNotFound
}
func (f *fakeConsole) SetRLS(context.Context, string, string, bool) (*domain.Table, error) {
	return nil, domain.ErrTableNotFound
}
func (f *fakeConsole) Browse(_ context.Context, _, _ string, q service.RowQuery) (service.Page, error) {
	f.query = q
	return service.Page{Rows: json.RawMessage(`[]`)}, nil
}
func (f *fakeConsole) Export(_ context.Context, _, name string, q service.RowQuery, open func(string) io.Writer) (int64, error) {
	if name != "todos" {
		return 0, domain.ErrTableNotFound
	}
	f.query = q
	_, _ = io.WriteString(open(name+".csv"), "id,title\n1,milk\n")
	return 1, nil
}
func (f *fakeConsole) Import(_ context.Context, _, _ string, r io.Reader, emptyAsNull bool) (service.ImportResult, error) {
	b, err := io.ReadAll(r)
	f.imported, f.emptyAsNull = string(b), emptyAsNull
	return service.ImportResult{Rows: 1}, err
}
func (f *fakeConsole) DropTable(_ context.Context, _, _, confirm string, preview bool) (service.DDLResult, error) {
	f.preview = preview
	return service.DDLResult{SQL: []string{"DROP TABLE x"}, Preview: preview}, nil
}
func (f *fakeConsole) ListUsers(context.Context, ports.UserQuery) ([]ports.UserSummary, int, error) {
	return nil, 0, service.ErrUsersUnavailable
}
func (f *fakeConsole) InsertRow(context.Context, string, string, map[string]json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (f *fakeConsole) UpdateRow(context.Context, string, string, map[string]json.RawMessage, map[string]json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (f *fakeConsole) DeleteRow(context.Context, string, string, map[string]json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (f *fakeConsole) RunSQL(context.Context, string) (domain.SQLRun, error) {
	f.sqlRuns++
	return domain.SQLRun{}, nil
}

func newServer(t *testing.T) (http.Handler, *fakeAccounts, *fakeConsole) {
	t.Helper()
	static := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>mini-base</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	a, c := &fakeAccounts{}, &fakeConsole{}
	r := httpx.NewRouter()
	adminhttp.NewHandler(a, c, static, ratelimit.New(0, 1), slog.New(slog.NewTextHandler(io.Discard, nil)), adminhttp.Options{}).Routes(r)
	return r, a, c
}

type req struct {
	method, path, body string
	headers            map[string]string
	cookie             bool
}

func do(h http.Handler, r req) *httptest.ResponseRecorder {
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	hr.Host = "admin.example.com"
	if r.body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	if r.cookie {
		hr.AddCookie(&http.Cookie{Name: adminhttp.CookieName, Value: goodToken})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, hr)
	return rec
}

var csrf = map[string]string{adminhttp.CSRFHeader: "1"}

func TestIndexHasStrictSecurityHeaders(t *testing.T) {
	t.Parallel()
	h, _, _ := newServer(t)
	rec := do(h, req{method: "GET", path: "/_/"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "mini-base") {
		t.Fatalf("index = %d %s", rec.Code, rec.Body)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("csp = %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing X-Frame-Options")
	}
	if rec := do(h, req{method: "GET", path: "/_/assets/app.js"}); rec.Code != 200 || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset = %d %q", rec.Code, rec.Body)
	}
	if rec := do(h, req{method: "GET", path: "/_"}); rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/_ redirect = %d", rec.Code)
	}
}

func TestLoginSetsHardenedCookie(t *testing.T) {
	t.Parallel()
	h, _, _ := newServer(t)
	rec := do(h, req{method: "POST", path: "/_/api/login", body: `{"email":"ops@example.com","password":"correct password"}`, headers: csrf})
	if rec.Code != 200 {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	c := rec.Result().Cookies()[0]
	if c.Name != adminhttp.CookieName || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/_/" {
		t.Fatalf("cookie = %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("API responses must not be cached")
	}

	bad := do(h, req{method: "POST", path: "/_/api/login", body: `{"email":"ops@example.com","password":"nope"}`, headers: csrf})
	if bad.Code != 401 || len(bad.Result().Cookies()) != 0 {
		t.Fatalf("bad login = %d", bad.Code)
	}
}

func TestCSRFProtection(t *testing.T) {
	t.Parallel()
	h, _, c := newServer(t)
	body := `{"query":"drop table x"}`

	tests := map[string]map[string]string{
		"missing header":   {},
		"cross-site fetch": {adminhttp.CSRFHeader: "1", "Sec-Fetch-Site": "cross-site"},
		"foreign origin":   {adminhttp.CSRFHeader: "1", "Origin": "https://evil.example.net"},
		"malformed origin": {adminhttp.CSRFHeader: "1", "Origin": "://"},
	}
	for name, headers := range tests {
		rec := do(h, req{method: "POST", path: "/_/api/sql", body: body, headers: headers, cookie: true})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "admin.csrf") {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
	}
	if c.sqlRuns != 0 {
		t.Fatal("a forged request reached the SQL editor")
	}

	ok := do(h, req{method: "POST", path: "/_/api/sql", body: body, cookie: true,
		headers: map[string]string{adminhttp.CSRFHeader: "1", "Origin": "https://admin.example.com"}})
	if ok.Code != 200 || c.sqlRuns != 1 {
		t.Fatalf("same-origin request = %d", ok.Code)
	}
}

func TestAPIRequiresSession(t *testing.T) {
	t.Parallel()
	h, a, _ := newServer(t)

	if rec := do(h, req{method: "GET", path: "/_/api/overview"}); rec.Code != 401 {
		t.Fatalf("no cookie = %d", rec.Code)
	}
	if rec := do(h, req{method: "GET", path: "/_/api/me", cookie: true}); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ops@example.com") {
		t.Fatalf("me = %d %s", rec.Code, rec.Body)
	}
	rec := do(h, req{method: "POST", path: "/_/api/logout", cookie: true, headers: csrf})
	if rec.Code != 204 || a.signedOut != goodToken || rec.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout = %d, signed out %q", rec.Code, a.signedOut)
	}
}

func TestUsersPageWithoutAuthModule(t *testing.T) {
	t.Parallel()
	h, _, _ := newServer(t)
	rec := do(h, req{method: "GET", path: "/_/api/users", cookie: true})
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "admin.users_unavailable") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestBrowseParsesFilters(t *testing.T) {
	t.Parallel()
	h, _, c := newServer(t)
	rec := do(h, req{method: "GET", path: "/_/api/tables/public/todos/rows?filter=title:contains:a:b&filter=done:eq:true&sort=title&dir=desc&limit=5", cookie: true})
	if rec.Code != 200 || len(c.query.Filters) != 2 || c.query.Filters[0].Value != "a:b" || !c.query.Sort.Desc || c.query.Limit != 5 {
		t.Fatalf("got %d %+v", rec.Code, c.query)
	}
	if rec := do(h, req{method: "GET", path: "/_/api/tables/public/todos/rows?filter=nocolon", cookie: true}); rec.Code != 400 {
		t.Fatalf("bad filter = %d", rec.Code)
	}
}

func TestExportDownloadsCSV(t *testing.T) {
	t.Parallel()
	h, _, _ := newServer(t)
	rec := do(h, req{method: "GET", path: "/_/api/tables/public/todos/export", cookie: true})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename=todos.csv` || rec.Body.String() != "id,title\n1,milk\n" {
		t.Fatalf("got %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	// Errors found before streaming are normal JSON errors.
	rec = do(h, req{method: "GET", path: "/_/api/tables/public/missing/export", cookie: true})
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "admin.table_not_found") {
		t.Fatalf("missing = %d %s", rec.Code, rec.Body)
	}
}

func TestImportReadsRawCSV(t *testing.T) {
	t.Parallel()
	h, _, c := newServer(t)
	rec := do(h, req{method: "POST", path: "/_/api/tables/public/todos/import?empty=string", body: "title\nmilk\n", cookie: true,
		headers: map[string]string{adminhttp.CSRFHeader: "1", "Content-Type": "text/csv"}})
	if rec.Code != 200 || c.imported != "title\nmilk\n" || c.emptyAsNull {
		t.Fatalf("got %d %s imported %q", rec.Code, rec.Body, c.imported)
	}
	if rec := do(h, req{method: "POST", path: "/_/api/tables/public/todos/import", body: "title\n", cookie: true}); rec.Code != 403 {
		t.Fatalf("import without CSRF header = %d", rec.Code)
	}
}

func TestDesignerPreviewFlag(t *testing.T) {
	t.Parallel()
	h, _, c := newServer(t)
	rec := do(h, req{method: "DELETE", path: "/_/api/tables/public/todos?preview=1", body: `{"confirm":"todos"}`, cookie: true, headers: csrf})
	if rec.Code != 200 || !c.preview || !strings.Contains(rec.Body.String(), `"preview":true`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestActionsCarryTheSignedInAdmin(t *testing.T) {
	t.Parallel()
	h, a, _ := newServer(t)
	rec := do(h, req{method: "DELETE", path: "/_/api/admins/a2", cookie: true, headers: csrf})
	if rec.Code != 204 || a.deletedBy.Email != "ops@example.com" || a.deletedBy.AccountID != "a1" || a.deletedBy.IP == "" {
		t.Fatalf("got %d actor %+v", rec.Code, a.deletedBy)
	}
	rec = do(h, req{method: "DELETE", path: "/_/api/admins/a1", cookie: true, headers: csrf})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "admin.cannot_delete_self") {
		t.Fatalf("delete self = %d %s", rec.Code, rec.Body)
	}
}
