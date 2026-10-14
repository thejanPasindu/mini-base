package http_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authhttp "github.com/mini-base/mini-base/internal/modules/auth/adapter/http"
	"github.com/mini-base/mini-base/internal/modules/auth/domain"
	"github.com/mini-base/mini-base/internal/modules/auth/service"
	"github.com/mini-base/mini-base/internal/platform/authn"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/internal/platform/ratelimit"
	"github.com/mini-base/mini-base/pkg/ports"
)

const (
	anonKey    = "anon-key-0123456789abcdef0123456789"
	serviceKey = "service-key-0123456789abcdef01234567"
)

type fakeUC struct {
	signedOut string
	refreshed string
}

func result() service.AuthResult {
	return service.AuthResult{
		User:            domain.User{ID: "u1", Email: "a@example.com"},
		AccessToken:     "access",
		AccessExpiresAt: time.Now().Add(15 * time.Minute),
		RefreshToken:    "refresh",
	}
}

func (f *fakeUC) SignUp(_ context.Context, email, _ string, _ domain.ClientInfo) (service.AuthResult, error) {
	if email == "taken@example.com" {
		return service.AuthResult{}, domain.ErrEmailTaken
	}
	return result(), nil
}

func (f *fakeUC) SignInWithPassword(context.Context, string, string, domain.ClientInfo) (service.AuthResult, error) {
	return service.AuthResult{}, domain.ErrInvalidCredentials
}

func (f *fakeUC) Refresh(_ context.Context, tok string) (service.AuthResult, error) {
	f.refreshed = tok
	return result(), nil
}

func (f *fakeUC) SignOut(_ context.Context, _, sid string) error { f.signedOut = sid; return nil }

func (f *fakeUC) GetUser(context.Context, string) (domain.User, error) {
	return domain.User{ID: "u1", Email: "a@example.com"}, nil
}

type userVerifier struct{}

func (userVerifier) VerifyAccessToken(context.Context, string) (ports.Principal, error) {
	return ports.Principal{Role: ports.RoleAuthenticated, UserID: "u1", SessionID: "s1"}, nil
}

func newServer(uc *fakeUC, perMinute int) http.Handler {
	a := authn.New(authn.Keys{Anon: anonKey, Service: serviceKey}, func() ports.TokenVerifier { return userVerifier{} })
	r := httpx.NewRouter()
	authhttp.NewHandler(uc, a, ratelimit.New(perMinute, 1), slog.New(slog.NewTextHandler(io.Discard, nil))).Routes(r)
	return r
}

func call(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSignUp(t *testing.T) {
	t.Parallel()
	h := newServer(&fakeUC{}, 0)

	rec := call(h, http.MethodPost, "/auth/v1/signup", `{"email":"a@example.com","password":"correct horse"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{`"access_token":"access"`, `"token_type":"bearer"`, `"refresh_token":"refresh"`, `"email":"a@example.com"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body missing %s: %s", want, rec.Body)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("token responses must not be cached")
	}

	if rec := call(h, http.MethodPost, "/auth/v1/signup", `{"email":"taken@example.com","password":"x"}`, nil); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d", rec.Code)
	}
}

func TestTokenGrants(t *testing.T) {
	t.Parallel()
	uc := &fakeUC{}
	h := newServer(uc, 0)

	if rec := call(h, http.MethodPost, "/auth/v1/token?grant_type=password", `{"email":"a@example.com","password":"x"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/auth/v1/token?grant_type=refresh_token", `{"refresh_token":"r1"}`, nil); rec.Code != http.StatusOK || uc.refreshed != "r1" {
		t.Fatalf("refresh = %d (%q)", rec.Code, uc.refreshed)
	}
	if rec := call(h, http.MethodPost, "/auth/v1/token?grant_type=magic", `{}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown grant = %d", rec.Code)
	}
}

func TestUserEndpointsNeedSignIn(t *testing.T) {
	t.Parallel()
	uc := &fakeUC{}
	h := newServer(uc, 0)

	if rec := call(h, http.MethodGet, "/auth/v1/user", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon /user = %d", rec.Code)
	}
	bearer := map[string]string{"Authorization": "Bearer t"}
	if rec := call(h, http.MethodGet, "/auth/v1/user", "", bearer); rec.Code != http.StatusOK {
		t.Fatalf("user /user = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/auth/v1/logout", "", bearer); rec.Code != http.StatusNoContent || uc.signedOut != "s1" {
		t.Fatalf("logout = %d (%q)", rec.Code, uc.signedOut)
	}
}

func TestSignUpIsRateLimited(t *testing.T) {
	t.Parallel()
	h := newServer(&fakeUC{}, 1)
	body := `{"email":"a@example.com","password":"correct horse"}`

	if rec := call(h, http.MethodPost, "/auth/v1/signup", body, nil); rec.Code != http.StatusCreated {
		t.Fatalf("first = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/auth/v1/signup", body, nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second = %d", rec.Code)
	}
}

func TestMissingAPIKey(t *testing.T) {
	t.Parallel()
	h := newServer(&fakeUC{}, 0)
	req := httptest.NewRequest(http.MethodGet, "/auth/v1/user", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "auth.missing_api_key") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}
