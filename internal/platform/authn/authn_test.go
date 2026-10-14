package authn_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mini-base/mini-base/internal/platform/authn"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/pkg/ports"
)

const (
	anonKey    = "anon-key-0123456789abcdef0123456789"
	serviceKey = "service-key-0123456789abcdef01234567"
)

type fakeVerifier struct{}

func (fakeVerifier) VerifyAccessToken(_ context.Context, token string) (ports.Principal, error) {
	if token != "good" {
		return ports.Principal{}, errors.New("bad signature")
	}
	return ports.Principal{Role: ports.RoleAuthenticated, UserID: "u1"}, nil
}

func serve(a *authn.Authenticator, headers map[string]string, mws ...httpx.Middleware) (*httptest.ResponseRecorder, ports.Principal) {
	var got ports.Principal
	h := httpx.Chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = authn.PrincipalFrom(r.Context())
	}), append([]httpx.Middleware{a.Middleware()}, mws...)...)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, got
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()

	withVerifier := authn.New(authn.Keys{Anon: anonKey, Service: serviceKey}, func() ports.TokenVerifier { return fakeVerifier{} })
	noVerifier := authn.New(authn.Keys{Anon: anonKey, Service: serviceKey}, func() ports.TokenVerifier { return nil })

	tests := []struct {
		name     string
		a        *authn.Authenticator
		headers  map[string]string
		status   int
		wantRole ports.Role
		code     string
	}{
		{"missing key", withVerifier, nil, 401, "", "auth.missing_api_key"},
		{"wrong key", withVerifier, map[string]string{"apikey": "nope"}, 401, "", "auth.invalid_api_key"},
		{"anon", withVerifier, map[string]string{"apikey": anonKey}, 200, ports.RoleAnon, ""},
		{"service", withVerifier, map[string]string{"apikey": serviceKey}, 200, ports.RoleService, ""},
		{"anon + valid token", withVerifier, map[string]string{"apikey": anonKey, "Authorization": "Bearer good"}, 200, ports.RoleAuthenticated, ""},
		{"anon + bad token", withVerifier, map[string]string{"apikey": anonKey, "Authorization": "Bearer bad"}, 401, "", "auth.invalid_token"},
		{"malformed authorization", withVerifier, map[string]string{"apikey": anonKey, "Authorization": "Basic x"}, 401, "", "auth.malformed_authorization"},
		{"token without auth module", noVerifier, map[string]string{"apikey": anonKey, "Authorization": "Bearer good"}, 401, "", "auth.tokens_unsupported"},
		{"not configured", authn.New(authn.Keys{}, nil), map[string]string{"apikey": anonKey}, 503, "", "auth.keys_not_configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec, p := serve(tt.a, tt.headers)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			if tt.code != "" && !strings.Contains(rec.Body.String(), tt.code) {
				t.Fatalf("body = %s, want code %s", rec.Body, tt.code)
			}
			if p.Role != tt.wantRole {
				t.Fatalf("role = %q, want %q", p.Role, tt.wantRole)
			}
		})
	}
}

func TestRequireRole(t *testing.T) {
	t.Parallel()

	a := authn.New(authn.Keys{Anon: anonKey, Service: serviceKey}, func() ports.TokenVerifier { return fakeVerifier{} })
	onlyUsers := authn.RequireRole(ports.RoleAuthenticated)

	if rec, _ := serve(a, map[string]string{"apikey": anonKey}, onlyUsers); rec.Code != 401 || !strings.Contains(rec.Body.String(), "auth.login_required") {
		t.Fatalf("anon: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := serve(a, map[string]string{"apikey": serviceKey}, onlyUsers); rec.Code != 403 {
		t.Fatalf("service: %d", rec.Code)
	}
	if rec, _ := serve(a, map[string]string{"apikey": anonKey, "Authorization": "Bearer good"}, onlyUsers); rec.Code != 200 {
		t.Fatalf("user: %d", rec.Code)
	}
}
