// Package http exposes the auth module at /auth/v1.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mini-base/mini-base/internal/modules/auth/domain"
	"github.com/mini-base/mini-base/internal/modules/auth/service"
	"github.com/mini-base/mini-base/internal/platform/authn"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/internal/platform/ratelimit"
	"github.com/mini-base/mini-base/pkg/ports"
)

// UseCases is what the handler needs from the service.
type UseCases interface {
	SignUp(ctx context.Context, email, password string, c domain.ClientInfo) (service.AuthResult, error)
	SignInWithPassword(ctx context.Context, email, password string, c domain.ClientInfo) (service.AuthResult, error)
	Refresh(ctx context.Context, refreshToken string) (service.AuthResult, error)
	SignOut(ctx context.Context, userID, sessionID string) error
	GetUser(ctx context.Context, id string) (domain.User, error)
}

// Handler serves the auth API.
type Handler struct {
	uc      UseCases
	auth    *authn.Authenticator
	limiter *ratelimit.Limiter
	log     *slog.Logger
}

// NewHandler returns a Handler.
func NewHandler(uc UseCases, auth *authn.Authenticator, limiter *ratelimit.Limiter, log *slog.Logger) *Handler {
	return &Handler{uc: uc, auth: auth, limiter: limiter, log: log}
}

// Routes registers:
//
//	POST /auth/v1/signup                        {email, password} → session
//	POST /auth/v1/token?grant_type=password      {email, password} → session
//	POST /auth/v1/token?grant_type=refresh_token {refresh_token}   → session
//	POST /auth/v1/logout                        (signed in)       → 204
//	GET  /auth/v1/user                          (signed in)       → user
func (h *Handler) Routes(r *httpx.Router) {
	api := r.Group("/auth/v1", h.auth.Middleware())
	limited := api.Group("", h.limiter.Middleware())
	limited.HandleFunc(http.MethodPost, "/signup", h.signUp)
	limited.HandleFunc(http.MethodPost, "/token", h.token)

	user := api.Group("", authn.RequireRole(ports.RoleAuthenticated))
	user.HandleFunc(http.MethodPost, "/logout", h.logout)
	user.HandleFunc(http.MethodGet, "/user", h.user)
}

// ---- wire types ----

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type userDTO struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSignInAt *time.Time `json:"last_sign_in_at"`
}

// sessionDTO follows the OAuth 2.0 token response shape.
type sessionDTO struct {
	AccessToken  string  `json:"access_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    int64   `json:"expires_in"`
	ExpiresAt    int64   `json:"expires_at"`
	RefreshToken string  `json:"refresh_token"`
	User         userDTO `json:"user"`
}

func toUserDTO(u domain.User) userDTO {
	return userDTO{ID: u.ID, Email: u.Email, CreatedAt: u.CreatedAt.UTC(), LastSignInAt: u.LastSignInAt}
}

func toSessionDTO(r service.AuthResult) sessionDTO {
	return sessionDTO{
		AccessToken:  r.AccessToken,
		TokenType:    "bearer",
		ExpiresIn:    int64(time.Until(r.AccessExpiresAt).Round(time.Second).Seconds()),
		ExpiresAt:    r.AccessExpiresAt.Unix(),
		RefreshToken: r.RefreshToken,
		User:         toUserDTO(r.User),
	}
}

var errUnsupportedGrant = errs.Invalid("auth.unsupported_grant_type", "grant_type must be password or refresh_token")

// ---- handlers ----

func (h *Handler) signUp(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, h.log, err)
		return
	}
	res, err := h.uc.SignUp(r.Context(), req.Email, req.Password, clientInfo(r))
	if err != nil {
		httpx.WriteError(w, r, h.log, err)
		return
	}
	noStore(w)
	httpx.WriteJSON(w, http.StatusCreated, toSessionDTO(res))
}

func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	var (
		res service.AuthResult
		err error
	)
	switch r.URL.Query().Get("grant_type") {
	case "password":
		var req credentials
		if err = httpx.DecodeJSON(r, &req); err == nil {
			res, err = h.uc.SignInWithPassword(r.Context(), req.Email, req.Password, clientInfo(r))
		}
	case "refresh_token":
		var req refreshRequest
		if err = httpx.DecodeJSON(r, &req); err == nil {
			res, err = h.uc.Refresh(r.Context(), req.RefreshToken)
		}
	default:
		err = errUnsupportedGrant
	}
	if err != nil {
		httpx.WriteError(w, r, h.log, err)
		return
	}
	noStore(w)
	httpx.WriteJSON(w, http.StatusOK, toSessionDTO(res))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	p, _ := authn.PrincipalFrom(r.Context())
	if err := h.uc.SignOut(r.Context(), p.UserID, p.SessionID); err != nil {
		httpx.WriteError(w, r, h.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) user(w http.ResponseWriter, r *http.Request) {
	p, _ := authn.PrincipalFrom(r.Context())
	u, err := h.uc.GetUser(r.Context(), p.UserID)
	if err != nil {
		httpx.WriteError(w, r, h.log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toUserDTO(u))
}

func clientInfo(r *http.Request) domain.ClientInfo {
	return domain.ClientInfo{UserAgent: r.UserAgent(), IP: ratelimit.ClientIP(r)}
}

// noStore stops proxies and browsers caching responses that contain tokens.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
