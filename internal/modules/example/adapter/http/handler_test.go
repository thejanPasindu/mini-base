package http_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	examplehttp "github.com/mini-base/mini-base/internal/modules/example/adapter/http"
	"github.com/mini-base/mini-base/internal/modules/example/domain"
	"github.com/mini-base/mini-base/internal/platform/httpx"
)

type fakeUseCases struct {
	createErr error
	lastLimit int
}

func (f *fakeUseCases) Create(_ context.Context, msg string) (domain.Greeting, error) {
	if f.createErr != nil {
		return domain.Greeting{}, f.createErr
	}
	return domain.Greeting{ID: 7, Message: msg, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, nil
}

func (f *fakeUseCases) List(_ context.Context, limit int) ([]domain.Greeting, error) {
	f.lastLimit = limit
	return []domain.Greeting{{ID: 1, Message: "a"}}, nil
}

func newServer(uc *fakeUseCases) http.Handler {
	r := httpx.NewRouter()
	examplehttp.NewHandler(uc, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes(r)
	return r
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateGreeting(t *testing.T) {
	t.Parallel()

	rec := do(newServer(&fakeUseCases{}), http.MethodPost, "/example/v1/greetings", `{"message":"hi"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	want := `{"id":7,"message":"hi","created_at":"2026-01-02T03:04:05Z"}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestCreateGreetingErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		uc     *fakeUseCases
		body   string
		status int
		code   string
	}{
		{"malformed json", &fakeUseCases{}, `{"message":`, http.StatusBadRequest, "request.malformed_json"},
		{"domain validation", &fakeUseCases{createErr: domain.ErrMessageEmpty}, `{"message":""}`, http.StatusBadRequest, "example.message_empty"},
		{"unexpected failure is 500", &fakeUseCases{createErr: errors.New("db down")}, `{"message":"x"}`, http.StatusInternalServerError, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := do(newServer(tt.uc), http.MethodPost, "/example/v1/greetings", tt.body)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), `"code":"`+tt.code+`"`) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "db down") {
				t.Fatal("internal error leaked")
			}
		})
	}
}

func TestListGreetings(t *testing.T) {
	t.Parallel()

	uc := &fakeUseCases{}
	h := newServer(uc)

	rec := do(h, http.MethodGet, "/example/v1/greetings?limit=5", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"data":[{"id":1`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if uc.lastLimit != 5 {
		t.Fatalf("limit = %d", uc.lastLimit)
	}

	if rec := do(h, http.MethodGet, "/example/v1/greetings?limit=abc", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit = %d", rec.Code)
	}
}
