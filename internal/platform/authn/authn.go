// Package authn resolves who is calling: it checks the API key, verifies an
// optional bearer access token, and stores the resulting ports.Principal in
// the request context.
//
// Every module API route sits behind Authenticator.Middleware. Health
// endpoints do not.
package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"slices"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/pkg/ports"
)

// APIKeyHeader carries the anon or service key.
const APIKeyHeader = "apikey"

// MinKeyLength is the minimum length of an API key.
const MinKeyLength = 32

// Errors returned to clients.
var (
	ErrMissingAPIKey      = errs.Unauthorized("auth.missing_api_key", "An apikey header is required")
	ErrInvalidAPIKey      = errs.Unauthorized("auth.invalid_api_key", "Invalid API key")
	ErrInvalidToken       = errs.Unauthorized("auth.invalid_token", "Invalid or expired access token")
	ErrTokensUnsupported  = errs.Unauthorized("auth.tokens_unsupported", "Access tokens require the auth module")
	ErrForbiddenRole      = errs.Forbidden("auth.forbidden", "This endpoint is not available for your credentials")
	ErrNotConfigured      = errs.Unavailable("auth.keys_not_configured", "API keys are not configured")
	errMalformedAuthorize = errs.Unauthorized("auth.malformed_authorization", "Authorization header must be: Bearer <token>")
)

// Keys are the two API keys. Treat both as secrets; the anon key is meant to
// be embedded in client apps and only grants what RLS allows.
type Keys struct {
	Anon    string
	Service string
}

// Authenticator implements API key + bearer token authentication.
type Authenticator struct {
	anon, service [sha256.Size]byte
	configured    bool
	verifier      func() ports.TokenVerifier
}

// New returns an Authenticator. verifier is called per request so that it
// can be provided by a module after the Authenticator is created; it may
// return nil when the auth module is disabled.
func New(keys Keys, verifier func() ports.TokenVerifier) *Authenticator {
	return &Authenticator{
		anon:       sha256.Sum256([]byte(keys.Anon)),
		service:    sha256.Sum256([]byte(keys.Service)),
		configured: keys.Anon != "" && keys.Service != "",
		verifier:   verifier,
	}
}

// Configured reports whether both API keys are set. Modules that expose an
// API must refuse to start when it is false.
func (a *Authenticator) Configured() bool { return a.configured }

// Middleware requires a valid API key and, if an Authorization header is
// present, a valid access token. It stores the Principal in the context.
func (a *Authenticator) Middleware() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := a.authenticate(r)
			if err != nil {
				httpx.WriteError(w, r, nil, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

func (a *Authenticator) authenticate(r *http.Request) (ports.Principal, error) {
	if !a.configured {
		return ports.Principal{}, ErrNotConfigured
	}
	key := r.Header.Get(APIKeyHeader)
	if key == "" {
		return ports.Principal{}, ErrMissingAPIKey
	}

	// Compare fixed-size digests in constant time so neither key content
	// nor length leaks through timing.
	digest := sha256.Sum256([]byte(key))
	switch {
	case subtle.ConstantTimeCompare(digest[:], a.service[:]) == 1:
		return ports.Principal{Role: ports.RoleService, Claims: map[string]any{"role": string(ports.RoleService)}}, nil
	case subtle.ConstantTimeCompare(digest[:], a.anon[:]) == 1:
	default:
		return ports.Principal{}, ErrInvalidAPIKey
	}

	authz := r.Header.Get("Authorization")
	if authz == "" {
		return ports.Principal{Role: ports.RoleAnon, Claims: map[string]any{"role": string(ports.RoleAnon)}}, nil
	}
	scheme, token, ok := strings.Cut(authz, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return ports.Principal{}, errMalformedAuthorize
	}

	v := a.verifier()
	if v == nil {
		return ports.Principal{}, ErrTokensUnsupported
	}
	p, err := v.VerifyAccessToken(r.Context(), strings.TrimSpace(token))
	if err != nil {
		return ports.Principal{}, ErrInvalidToken.Wrap(err)
	}
	return p, nil
}

// RequireRole rejects requests whose principal is not one of roles.
// Use it after Middleware.
func RequireRole(roles ...ports.Role) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok || !slices.Contains(roles, p.Role) {
				err := ErrForbiddenRole
				if ok && p.Role == ports.RoleAnon && slices.Contains(roles, ports.RoleAuthenticated) {
					err = errs.Unauthorized("auth.login_required", "Sign in to use this endpoint")
				}
				httpx.WriteError(w, r, nil, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type ctxKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p ports.Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom returns the principal stored by Middleware.
func PrincipalFrom(ctx context.Context) (ports.Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(ports.Principal)
	return p, ok
}
