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

type fakeAccounts struct{ signedOut string }

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

type fakeConsole struct{ sqlRuns int }

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
func (f *fakeConsole) Browse(context.Context, string, string, *domain.SortTerm, int, int) (service.Page, error) {
	return service.Page{Rows: json.RawMessage(`[]`)}, nil
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
func (f *fakeConsole) Users() (ports.UserDirectory, error) { return nil, service.ErrUsersUnavailable }

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
