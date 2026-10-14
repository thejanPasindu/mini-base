package http_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbhttp "github.com/mini-base/mini-base/internal/modules/database/adapter/http"
	"github.com/mini-base/mini-base/internal/modules/database/service"
	"github.com/mini-base/mini-base/internal/platform/authn"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/pkg/ports"
)

const anonKey = "anon-key-0123456789abcdef0123456789"

type fakeUC struct{ last service.Request }

func (f *fakeUC) respond(r service.Request) (service.Response, error) {
	f.last = r
	if r.Return || r.Body == nil {
		return service.Response{Body: []byte(`[{"id":1}]`)}, nil
	}
	return service.Response{Affected: 1}, nil
}

func (f *fakeUC) Read(_ context.Context, r service.Request) (service.Response, error) {
	return f.respond(r)
}
func (f *fakeUC) Create(_ context.Context, r service.Request) (service.Response, error) {
	return f.respond(r)
}
func (f *fakeUC) Update(_ context.Context, r service.Request) (service.Response, error) {
	return f.respond(r)
}
func (f *fakeUC) Delete(_ context.Context, r service.Request) (service.Response, error) {
	f.last = r
	return service.Response{Affected: 3}, nil
}

func newServer(uc *fakeUC) http.Handler {
	a := authn.New(authn.Keys{Anon: anonKey, Service: "service-key-0123456789abcdef01234567"}, func() ports.TokenVerifier { return nil })
	r := httpx.NewRouter()
	dbhttp.NewHandler(uc, a, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes(r)
	return r
}

func call(h http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("apikey", anonKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var jsonCT = map[string]string{"Content-Type": "application/json"}

func TestReadPassesTableQueryAndPrincipal(t *testing.T) {
	t.Parallel()
	uc := &fakeUC{}
	rec := call(newServer(uc), http.MethodGet, "/rest/v1/todos?done=eq.false&select=id", "", nil)

	if rec.Code != http.StatusOK || rec.Body.String() != `[{"id":1}]` || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("got %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	if uc.last.Table != "todos" || uc.last.Query.Get("done") != "eq.false" || uc.last.Principal.Role != ports.RoleAnon {
		t.Fatalf("request = %+v", uc.last)
	}
}

func TestCreateStatusAndPrefer(t *testing.T) {
	t.Parallel()
	uc := &fakeUC{}
	h := newServer(uc)

	rec := call(h, http.MethodPost, "/rest/v1/todos", `{"title":"x"}`, jsonCT)
	if rec.Code != http.StatusCreated || rec.Body.Len() != 0 || rec.Header().Get("X-Affected-Rows") != "1" {
		t.Fatalf("minimal: %d %q", rec.Code, rec.Body)
	}

	rec = call(h, http.MethodPost, "/rest/v1/todos", `{"title":"x"}`,
		map[string]string{"Content-Type": "application/json", "Prefer": "count=none, return=representation"})
	if rec.Code != http.StatusCreated || rec.Body.String() != `[{"id":1}]` || !uc.last.Return {
		t.Fatalf("representation: %d %q", rec.Code, rec.Body)
	}

	if rec := call(h, http.MethodPost, "/rest/v1/todos", `title=x`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("form body = %d", rec.Code)
	}
}

func TestDeleteMinimalIs204(t *testing.T) {
	t.Parallel()
	rec := call(newServer(&fakeUC{}), http.MethodDelete, "/rest/v1/todos?id=eq.1", "", nil)
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Affected-Rows") != "3" {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
}

func TestRequiresAPIKey(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/rest/v1/todos", nil)
	rec := httptest.NewRecorder()
	newServer(&fakeUC{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}
