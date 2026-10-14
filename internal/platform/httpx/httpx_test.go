package httpx_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/internal/platform/httpx"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func decodeError(t *testing.T, body io.Reader) httpx.ErrorDetail {
	t.Helper()
	var b httpx.ErrorBody
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return b.Error
}

func TestWriteErrorMapsKindToStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid", errs.Invalid("x.bad", "bad"), http.StatusBadRequest, "x.bad"},
		{"not found", errs.NotFound("x.missing", "missing"), http.StatusNotFound, "x.missing"},
		{"conflict", errs.Conflict("x.dup", "dup"), http.StatusConflict, "x.dup"},
		{"plain error hides details", io.ErrUnexpectedEOF, http.StatusInternalServerError, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			httpx.WriteError(rec, req, discardLogger(), tt.err)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			got := decodeError(t, rec.Body)
			if got.Code != tt.code {
				t.Fatalf("code = %q, want %q", got.Code, tt.code)
			}
			if strings.Contains(got.Message, "EOF") {
				t.Fatal("internal error details leaked to client")
			}
		})
	}
}

func TestRouterGroupsAndJSONFallbacks(t *testing.T) {
	t.Parallel()

	var order []string
	mark := func(name string) httpx.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	root := httpx.NewRouter()
	api := root.Group("/api/v1", mark("group"))
	api.HandleFunc(http.MethodGet, "/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id")})
	})

	t.Run("matched route with path value and middleware", func(t *testing.T) {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/items/42", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"42"`) {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		if len(order) != 1 || order[0] != "group" {
			t.Fatalf("middleware order = %v", order)
		}
	})

	t.Run("unknown path is JSON 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d", rec.Code)
		}
		if got := decodeError(t, rec.Body); got.Code != "request.route_not_found" {
			t.Fatalf("code = %q", got.Code)
		}
	})

	t.Run("wrong method is JSON 405 with Allow", func(t *testing.T) {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/items/1", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d", rec.Code)
		}
		if rec.Header().Get("Allow") == "" {
			t.Fatal("Allow header missing")
		}
		if got := decodeError(t, rec.Body); got.Code != "request.method_not_allowed" {
			t.Fatalf("code = %q", got.Code)
		}
	})
}

func TestRequestIDIsGeneratedOrPropagated(t *testing.T) {
	t.Parallel()

	var seen string
	h := httpx.Chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
	}), httpx.WithRequestID())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(httpx.RequestIDHeader, "abc-123")
	h.ServeHTTP(rec, req)
	if seen != "abc-123" || rec.Header().Get(httpx.RequestIDHeader) != "abc-123" {
		t.Fatalf("inbound id not propagated: %q", seen)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(httpx.RequestIDHeader, "bad id with spaces\n")
	h.ServeHTTP(rec, req)
	if seen == "" || strings.ContainsAny(seen, " \n") {
		t.Fatalf("unsafe inbound id accepted: %q", seen)
	}
}

func TestRecoveryReturns500(t *testing.T) {
	t.Parallel()

	h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), httpx.WithRequestID(), httpx.WithRecovery(discardLogger()))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := decodeError(t, rec.Body); got.RequestID == "" {
		t.Fatal("request id missing from error envelope")
	}
}

func TestCORS(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := httpx.WithCORS(httpx.CORSOptions{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         time.Hour,
	})(next)

	t.Run("preflight from allowed origin", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/x", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
			t.Fatal("origin not allowed")
		}
		if rec.Header().Get("Access-Control-Max-Age") != "3600" {
			t.Fatalf("max-age = %q", rec.Header().Get("Access-Control-Max-Age"))
		}
	})

	t.Run("disallowed origin gets no CORS headers", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("disallowed origin received CORS headers")
		}
		if rec.Code != http.StatusTeapot {
			t.Fatal("request should still reach handler")
		}
	})
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{"valid", `{"name":"a"}`, nil},
		{"unknown field", `{"name":"a","x":1}`, httpx.ErrMalformedJSON},
		{"trailing data", `{"name":"a"}{}`, httpx.ErrMalformedJSON},
		{"too large", `{"name":"` + strings.Repeat("a", 100) + `"}`, httpx.ErrBodyTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got error
			h := httpx.WithMaxBodySize(64)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				var p payload
				got = httpx.DecodeJSON(r, &p)
			}))
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			h.ServeHTTP(httptest.NewRecorder(), req)

			if tt.wantErr == nil && got != nil {
				t.Fatalf("unexpected error: %v", got)
			}
			if tt.wantErr != nil && !errors.Is(got, tt.wantErr) {
				t.Fatalf("err = %v, want %v", got, tt.wantErr)
			}
		})
	}
}
