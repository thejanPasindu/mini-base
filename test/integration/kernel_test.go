//go:build integration

// Integration tests need a real PostgreSQL. Run with:
//
//	make test-integration
//
// which starts Postgres via Docker and sets MINIBASE_TEST_DATABASE_URL.
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/kernel"
	"github.com/mini-base/mini-base/internal/modules/admin"
	"github.com/mini-base/mini-base/internal/modules/auth"
	"github.com/mini-base/mini-base/internal/modules/database"
	"github.com/mini-base/mini-base/internal/modules/example"
)

const (
	anonKey    = "itest-anon-key-0123456789abcdef0123456789"
	serviceKey = "itest-service-key-0123456789abcdef01234567"
	jwtSecret  = "itest-jwt-secret-0123456789abcdef012345678"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("MINIBASE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MINIBASE_TEST_DATABASE_URL not set")
	}
	return url
}

func registry() *kernel.Registry {
	r := kernel.NewRegistry()
	r.MustRegister(admin.New(), auth.New(), database.New(), example.New())
	return r
}

func envFunc(t *testing.T, modules string) func(string) string {
	t.Helper()
	env := map[string]string{
		"MINIBASE_DATABASE_URL":               testDatabaseURL(t),
		"MINIBASE_MODULES":                    modules,
		"MINIBASE_HTTP_SHUTDOWN_TIMEOUT":      "2s",
		"MINIBASE_ANON_KEY":                   anonKey,
		"MINIBASE_SERVICE_KEY":                serviceKey,
		"MINIBASE_AUTH_JWT_SECRET":            jwtSecret,
		"MINIBASE_AUTH_RATE_LIMIT_PER_MINUTE": "0",
		"MINIBASE_DATABASE_STATEMENT_TIMEOUT": "5s",
		"MINIBASE_ADMIN_EMAIL":                "itest-admin@example.com",
		"MINIBASE_ADMIN_PASSWORD":             "itest admin password",
	}
	return func(k string) string { return env[k] }
}

// startKernel runs a kernel on an ephemeral port and returns its base URL.
// The kernel stops when the test ends.
func startKernel(t *testing.T, modules string) string {
	t.Helper()
	getenv := envFunc(t, modules)
	cfg, err := kernel.LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan net.Addr, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	k := kernel.New(cfg, registry(), log, "test", getenv, kernel.WithListener(ln), kernel.WithReadyNotify(ready))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("kernel shutdown: %v", err)
		}
	})

	select {
	case addr := <-ready:
		return "http://" + addr.String()
	case err := <-done:
		t.Fatalf("kernel exited during startup: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("kernel did not start")
	}
	return ""
}

// ---- tiny HTTP client ----

type client struct {
	t     *testing.T
	base  string
	key   string
	token string
}

func (c client) do(method, path, body string, headers ...string) (int, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if c.key != "" {
		req.Header.Set("apikey", c.key)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

type session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	User         struct {
		ID string `json:"id"`
	} `json:"user"`
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

func expect(t *testing.T, what string, gotStatus int, body []byte, wantStatus int) {
	t.Helper()
	if gotStatus != wantStatus {
		t.Fatalf("%s: status %d, want %d: %s", what, gotStatus, wantStatus, body)
	}
}

// ---- tests ----

func TestKernelServesHealthAndExampleModule(t *testing.T) {
	base := startKernel(t, "example")
	c := client{t: t, base: base}

	st, b := c.do(http.MethodGet, "/health/ready", "")
	expect(t, "ready", st, b, http.StatusOK)

	st, b = c.do(http.MethodPost, "/example/v1/greetings", `{"message":"hello"}`)
	expect(t, "create greeting", st, b, http.StatusCreated)

	st, b = c.do(http.MethodGet, "/example/v1/greetings?limit=1", "")
	expect(t, "list greetings", st, b, http.StatusOK)
	if !bytes.Contains(b, []byte(`"message":"hello"`)) {
		t.Fatalf("list = %s", b)
	}
}

func TestDisabledModuleHasNoRoutes(t *testing.T) {
	c := client{t: t, base: startKernel(t, "")}
	st, b := c.do(http.MethodGet, "/example/v1/ping", "")
	expect(t, "disabled module route", st, b, http.StatusNotFound)
}

func TestMigrationsAreIdempotent(t *testing.T) {
	for range 2 {
		getenv := envFunc(t, "auth,database,example")
		cfg, err := kernel.LoadConfig(getenv)
		if err != nil {
			t.Fatal(err)
		}
		k := kernel.New(cfg, registry(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test", getenv)
		if err := k.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPhase1 is the Phase 1 exit criterion: users sign up, write rows, and
// row-level security keeps each user's rows private.
func TestPhase1(t *testing.T) {
	ctx := context.Background()

	// Run migrations first so auth.uid() exists for the policy below.
	getenv := envFunc(t, "auth,database")
	cfg, _ := kernel.LoadConfig(getenv)
	if err := kernel.New(cfg, registry(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test", getenv).Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS public.itest_todos, public.itest_open;
		CREATE TABLE public.itest_todos (
		    id      bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		    user_id uuid    NOT NULL DEFAULT auth.uid(),
		    title   text    NOT NULL,
		    done    boolean NOT NULL DEFAULT false
		);
		ALTER TABLE public.itest_todos ENABLE ROW LEVEL SECURITY;
		CREATE POLICY own_rows ON public.itest_todos FOR ALL TO minibase_authenticated
		    USING (user_id = auth.uid()) WITH CHECK (user_id = auth.uid());
		CREATE TABLE public.itest_open (id int);`); err != nil {
		t.Fatal(err)
	}

	base := startKernel(t, "auth,database")
	anon := client{t: t, base: base, key: anonKey}
	svc := client{t: t, base: base, key: serviceKey}
	suffix := time.Now().Format("150405.000000")

	signUp := func(name string) (client, session) {
		st, b := anon.do(http.MethodPost, "/auth/v1/signup",
			`{"email":"`+name+suffix+`@example.com","password":"correct horse battery"}`)
		expect(t, "sign up "+name, st, b, http.StatusCreated)
		s := decode[session](t, b)
		return client{t: t, base: base, key: anonKey, token: s.AccessToken}, s
	}
	alice, aliceSession := signUp("alice")
	bob, _ := signUp("bob")

	// Alice creates a todo; user_id defaults to her id via auth.uid().
	st, b := alice.do(http.MethodPost, "/rest/v1/itest_todos", `{"title":"alice todo"}`, "Prefer", "return=representation")
	expect(t, "alice insert", st, b, http.StatusCreated)
	if !bytes.Contains(b, []byte(aliceSession.User.ID)) {
		t.Fatalf("row not owned by alice: %s", b)
	}

	st, b = alice.do(http.MethodGet, "/rest/v1/itest_todos?select=title", "")
	expect(t, "alice read", st, b, http.StatusOK)
	if string(b) != `[{"title":"alice todo"}]` {
		t.Fatalf("alice sees %s", b)
	}

	// Bob sees nothing and cannot change Alice's row.
	st, b = bob.do(http.MethodGet, "/rest/v1/itest_todos", "")
	expect(t, "bob read", st, b, http.StatusOK)
	if string(b) != `[]` {
		t.Fatalf("bob sees alice's rows: %s", b)
	}
	st, b = bob.do(http.MethodPatch, "/rest/v1/itest_todos?title=eq.alice%20todo", `{"done":true}`, "Prefer", "return=representation")
	expect(t, "bob update", st, b, http.StatusOK)
	if string(b) != `[]` {
		t.Fatalf("bob updated alice's row: %s", b)
	}
	st, b = bob.do(http.MethodPost, "/rest/v1/itest_todos", `{"title":"forged","user_id":"`+aliceSession.User.ID+`"}`)
	expect(t, "bob forges ownership", st, b, http.StatusForbidden)

	// Anonymous callers have no policy, so they see nothing.
	st, b = anon.do(http.MethodGet, "/rest/v1/itest_todos", "")
	expect(t, "anon read", st, b, http.StatusOK)
	if string(b) != `[]` {
		t.Fatalf("anon sees %s", b)
	}

	// The service key bypasses RLS.
	st, b = svc.do(http.MethodGet, "/rest/v1/itest_todos?select=title", "")
	expect(t, "service read", st, b, http.StatusOK)
	if !bytes.Contains(b, []byte("alice todo")) {
		t.Fatalf("service sees %s", b)
	}

	// Tables without RLS are refused for users, allowed for the service key.
	st, b = alice.do(http.MethodGet, "/rest/v1/itest_open", "")
	expect(t, "no-RLS table as user", st, b, http.StatusForbidden)
	st, b = svc.do(http.MethodGet, "/rest/v1/itest_open", "")
	expect(t, "no-RLS table as service", st, b, http.StatusOK)

	// Refresh rotates; replaying the old token revokes the session.
	st, b = anon.do(http.MethodPost, "/auth/v1/token?grant_type=refresh_token", `{"refresh_token":"`+aliceSession.RefreshToken+`"}`)
	expect(t, "refresh", st, b, http.StatusOK)
	rotated := decode[session](t, b)
	st, b = anon.do(http.MethodPost, "/auth/v1/token?grant_type=refresh_token", `{"refresh_token":"`+aliceSession.RefreshToken+`"}`)
	expect(t, "refresh replay", st, b, http.StatusUnauthorized)
	if !bytes.Contains(b, []byte("auth.refresh_token_reused")) {
		t.Fatalf("replay body = %s", b)
	}
	st, b = anon.do(http.MethodPost, "/auth/v1/token?grant_type=refresh_token", `{"refresh_token":"`+rotated.RefreshToken+`"}`)
	expect(t, "refresh after revocation", st, b, http.StatusUnauthorized)

	// Password sign-in and the user endpoint.
	st, b = anon.do(http.MethodPost, "/auth/v1/token?grant_type=password",
		`{"email":"bob`+suffix+`@example.com","password":"correct horse battery"}`)
	expect(t, "password sign-in", st, b, http.StatusOK)
	st, b = bob.do(http.MethodGet, "/auth/v1/user", "")
	expect(t, "get user", st, b, http.StatusOK)

	// Logout ends the session.
	st, b = bob.do(http.MethodPost, "/auth/v1/logout", "")
	expect(t, "logout", st, b, http.StatusNoContent)
}

// TestAdminDashboard drives the dashboard API: bootstrap admin, sign-in,
// SQL editor, RLS toggle, row editing and user management.
func TestAdminDashboard(t *testing.T) {
	base := startKernel(t, "admin,auth,database")
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}

	call := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, base+"/_/api"+path, strings.NewReader(body))
		req.Header.Set("X-Minibase-Admin", "1")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	st, b := call(http.MethodGet, "/overview", "")
	expect(t, "overview before sign-in", st, b, http.StatusUnauthorized)

	st, b = call(http.MethodPost, "/login", `{"email":"itest-admin@example.com","password":"itest admin password"}`)
	expect(t, "admin sign-in (bootstrapped from env)", st, b, http.StatusOK)

	st, b = call(http.MethodPost, "/sql", `{"query":"DROP TABLE IF EXISTS public.itest_notes; CREATE TABLE public.itest_notes (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, body text NOT NULL, meta jsonb); SELECT 1 AS one"}`)
	expect(t, "sql editor", st, b, http.StatusOK)
	run := decode[struct {
		Results []struct{ Command string } `json:"results"`
		Error   *struct{ Message string }  `json:"error"`
	}](t, b)
	if run.Error != nil || len(run.Results) != 3 || run.Results[2].Command != "SELECT 1" {
		t.Fatalf("sql run = %s", b)
	}

	st, b = call(http.MethodPost, "/sql", `{"query":"SELECT * FROM no_such_table"}`)
	expect(t, "sql error is a result, not a failure", st, b, http.StatusOK)
	if !bytes.Contains(b, []byte("does not exist")) {
		t.Fatalf("sql error = %s", b)
	}

	st, b = call(http.MethodGet, "/overview", "")
	expect(t, "overview", st, b, http.StatusOK)
	if !bytes.Contains(b, []byte("itest_notes")) {
		t.Fatalf("new table without RLS should be flagged: %s", b)
	}

	st, b = call(http.MethodPost, "/tables/public/itest_notes/rls", `{"enabled":true}`)
	expect(t, "enable rls", st, b, http.StatusOK)
	if !bytes.Contains(b, []byte(`"rls_enabled":true`)) {
		t.Fatalf("rls = %s", b)
	}

	st, b = call(http.MethodPost, "/tables/public/itest_notes/rows", `{"values":{"body":"hello","meta":{"a":1}}}`)
	expect(t, "insert row", st, b, http.StatusCreated)
	row := decode[struct {
		Row struct {
			ID   int64          `json:"id"`
			Meta map[string]int `json:"meta"`
		} `json:"row"`
	}](t, b)
	if row.Row.Meta["a"] != 1 {
		t.Fatalf("jsonb not stored as JSON: %s", b)
	}
	key := `{"id":` + strconv.FormatInt(row.Row.ID, 10) + `}`

	st, b = call(http.MethodPatch, "/tables/public/itest_notes/rows", `{"key":`+key+`,"values":{"body":"edited"}}`)
	expect(t, "update row", st, b, http.StatusOK)
	st, b = call(http.MethodGet, "/tables/public/itest_notes/rows?limit=10", "")
	expect(t, "browse", st, b, http.StatusOK)
	if !bytes.Contains(b, []byte(`"body":"edited"`)) || !bytes.Contains(b, []byte(`"total":1`)) {
		t.Fatalf("browse = %s", b)
	}
	st, b = call(http.MethodDelete, "/tables/public/itest_notes/rows", `{"key":`+key+`}`)
	expect(t, "delete row", st, b, http.StatusOK)
	st, b = call(http.MethodDelete, "/tables/public/itest_notes/rows", `{"key":`+key+`}`)
	expect(t, "delete again", st, b, http.StatusNotFound)

	st, b = call(http.MethodGet, "/tables/auth/users", "")
	expect(t, "internal schemas are hidden", st, b, http.StatusNotFound)

	email := "dash" + time.Now().Format("150405.000000") + "@example.com"
	st, b = call(http.MethodPost, "/users", `{"email":"`+email+`","password":"correct horse battery"}`)
	expect(t, "create user", st, b, http.StatusCreated)
	user := decode[struct {
		User struct{ ID string } `json:"user"`
	}](t, b)
	st, b = call(http.MethodGet, "/users?search="+email, "")
	expect(t, "search users", st, b, http.StatusOK)
	if !bytes.Contains(b, []byte(`"total":1`)) {
		t.Fatalf("search = %s", b)
	}
	st, b = call(http.MethodDelete, "/users/"+user.User.ID, "")
	expect(t, "delete user", st, b, http.StatusNoContent)

	st, b = call(http.MethodPost, "/logout", "")
	expect(t, "logout", st, b, http.StatusNoContent)
	st, b = call(http.MethodGet, "/me", "")
	expect(t, "after logout", st, b, http.StatusUnauthorized)
}
