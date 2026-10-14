// Package http exposes tables at /rest/v1/{table}.
package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/mini-base/mini-base/internal/modules/database/service"
	"github.com/mini-base/mini-base/internal/platform/authn"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/internal/platform/httpx"
)

// UseCases is what the handler needs from the service.
type UseCases interface {
	Read(ctx context.Context, r service.Request) (service.Response, error)
	Create(ctx context.Context, r service.Request) (service.Response, error)
	Update(ctx context.Context, r service.Request) (service.Response, error)
	Delete(ctx context.Context, r service.Request) (service.Response, error)
}

// Handler serves the REST API.
type Handler struct {
	uc   UseCases
	auth *authn.Authenticator
	log  *slog.Logger
}

// NewHandler returns a Handler.
func NewHandler(uc UseCases, auth *authn.Authenticator, log *slog.Logger) *Handler {
	return &Handler{uc: uc, auth: auth, log: log}
}

// Routes registers:
//
//	GET    /rest/v1/{table}?select=…&col=op.value&order=…&limit=…&offset=…
//	POST   /rest/v1/{table}            body: object or array of objects
//	PATCH  /rest/v1/{table}?col=op.v   body: object (filters required)
//	DELETE /rest/v1/{table}?col=op.v   (filters required)
//
// "Prefer: return=representation" makes POST/PATCH/DELETE return the rows.
func (h *Handler) Routes(r *httpx.Router) {
	g := r.Group("/rest/v1", h.auth.Middleware())
	g.HandleFunc(http.MethodGet, "/{table}", h.serve(h.uc.Read, http.StatusOK))
	g.HandleFunc(http.MethodPost, "/{table}", h.serve(h.uc.Create, http.StatusCreated))
	g.HandleFunc(http.MethodPatch, "/{table}", h.serve(h.uc.Update, http.StatusOK))
	g.HandleFunc(http.MethodDelete, "/{table}", h.serve(h.uc.Delete, http.StatusOK))
}

type useCase func(context.Context, service.Request) (service.Response, error)

func (h *Handler) serve(uc useCase, okStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := h.request(r)
		if err != nil {
			httpx.WriteError(w, r, h.log, err)
			return
		}
		res, err := uc(r.Context(), req)
		if err != nil {
			httpx.WriteError(w, r, h.log, err)
			return
		}

		if res.Body == nil { // return=minimal
			w.Header().Set("X-Affected-Rows", strconv.FormatInt(res.Affected, 10))
			if okStatus == http.StatusOK {
				okStatus = http.StatusNoContent
			}
			w.WriteHeader(okStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(okStatus)
		_, _ = w.Write(res.Body) // JSON produced by Postgres
	}
}

var errContentType = errs.Invalid("request.unsupported_media_type", "Content-Type must be application/json")

func (h *Handler) request(r *http.Request) (service.Request, error) {
	p, _ := authn.PrincipalFrom(r.Context())
	req := service.Request{
		Principal: p,
		Table:     r.PathValue("table"),
		Query:     r.URL.Query(),
		Return:    prefers(r, "return=representation"),
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			return service.Request{}, errContentType
		}
		body, err := io.ReadAll(r.Body) // size capped by the kernel's MaxBodySize
		if err != nil {
			return service.Request{}, httpx.ErrBodyTooLarge.Wrap(err)
		}
		req.Body = body
	}
	return req, nil
}

// prefers reports whether the Prefer header (RFC 7240) contains pref.
func prefers(r *http.Request, pref string) bool {
	for _, h := range r.Header.Values("Prefer") {
		for _, p := range strings.Split(h, ",") {
			if strings.EqualFold(strings.TrimSpace(p), pref) {
				return true
			}
		}
	}
	return false
}
