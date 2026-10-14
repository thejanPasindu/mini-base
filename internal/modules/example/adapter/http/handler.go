// Package http exposes the example module over REST.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mini-base/mini-base/internal/modules/example/domain"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/internal/platform/httpx"
)

// UseCases is what the handler needs from the service layer.
type UseCases interface {
	Create(ctx context.Context, message string) (domain.Greeting, error)
	List(ctx context.Context, limit int) ([]domain.Greeting, error)
}

// Handler serves /example/v1.
type Handler struct {
	uc  UseCases
	log *slog.Logger
}

// NewHandler returns a Handler.
func NewHandler(uc UseCases, log *slog.Logger) *Handler {
	return &Handler{uc: uc, log: log}
}

// Routes registers the module's endpoints.
func (h *Handler) Routes(r *httpx.Router) {
	g := r.Group("/example/v1")
	g.HandleFunc(http.MethodGet, "/ping", h.ping)
	g.HandleFunc(http.MethodGet, "/greetings", h.list)
	g.HandleFunc(http.MethodPost, "/greetings", h.create)
}

// greetingDTO is the wire format. Domain types never leak into JSON directly.
type greetingDTO struct {
	ID        int64     `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func toDTO(g domain.Greeting) greetingDTO {
	return greetingDTO{ID: g.ID, Message: g.Message, CreatedAt: g.CreatedAt.UTC()}
}

type createRequest struct {
	Message string `json:"message"`
}

type listResponse struct {
	Data []greetingDTO `json:"data"`
}

var errInvalidLimit = errs.Invalid("example.invalid_limit", "limit must be an integer")

func (h *Handler) ping(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"message": "pong"})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, h.log, err)
		return
	}
	g, err := h.uc.Create(r.Context(), req.Message)
	if err != nil {
		httpx.WriteError(w, r, h.log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toDTO(g))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			httpx.WriteError(w, r, h.log, errInvalidLimit)
			return
		}
		limit = n
	}
	found, err := h.uc.List(r.Context(), limit)
	if err != nil {
		httpx.WriteError(w, r, h.log, err)
		return
	}
	resp := listResponse{Data: make([]greetingDTO, len(found))}
	for i, g := range found {
		resp.Data[i] = toDTO(g)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
