package httpx

import (
	"net/http"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first one listed is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Router is a thin layer over http.ServeMux (Go 1.22+ patterns) that adds
// prefixed groups, per-group middleware and JSON 404/405 responses.
// It deliberately has no other features: the standard library does the routing.
type Router struct {
	mux    *http.ServeMux
	prefix string
	mws    []Middleware
}

// NewRouter returns an empty root router.
func NewRouter() *Router {
	return &Router{mux: http.NewServeMux()}
}

// Group returns a router whose routes are registered under prefix and wrapped
// by mws (after any middleware inherited from the parent group).
func (r *Router) Group(prefix string, mws ...Middleware) *Router {
	all := make([]Middleware, 0, len(r.mws)+len(mws))
	all = append(all, r.mws...)
	all = append(all, mws...)
	full := r.prefix
	if p := strings.Trim(prefix, "/"); p != "" {
		full += "/" + p
	}
	return &Router{mux: r.mux, prefix: full, mws: all}
}

// Handle registers h for method and path, relative to the group prefix.
// Path wildcards follow net/http rules, e.g. "/items/{id}".
func (r *Router) Handle(method, path string, h http.Handler) {
	full := r.prefix + path
	if full == "" {
		full = "/"
	}
	r.mux.Handle(method+" "+full, Chain(h, r.mws...))
}

// HandleFunc is the http.HandlerFunc form of Handle.
func (r *Router) HandleFunc(method, path string, h http.HandlerFunc) {
	r.Handle(method, path, h)
}

// ServeHTTP dispatches to the matching route. Requests that match no route
// receive the standard JSON error envelope instead of ServeMux's plain text.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if _, pattern := r.mux.Handler(req); pattern == "" {
		r.mux.ServeHTTP(&unmatchedWriter{ResponseWriter: w, req: req}, req)
		return
	}
	r.mux.ServeHTTP(w, req)
}

var (
	errRouteNotFound    = errs.NotFound("request.route_not_found", "No route matches this path")
	errMethodNotAllowed = errs.New(errs.KindInvalid, "request.method_not_allowed", "Method not allowed for this path")
)

// unmatchedWriter replaces ServeMux's text body for 404/405 with JSON while
// keeping headers it set (such as Allow).
type unmatchedWriter struct {
	http.ResponseWriter
	req     *http.Request
	written bool
}

func (u *unmatchedWriter) WriteHeader(code int) {
	if u.written {
		return
	}
	u.written = true
	switch code {
	case http.StatusMethodNotAllowed:
		writeErrorStatus(u.ResponseWriter, u.req, http.StatusMethodNotAllowed, errMethodNotAllowed)
	case http.StatusNotFound:
		WriteError(u.ResponseWriter, u.req, nil, errRouteNotFound)
	default:
		u.ResponseWriter.WriteHeader(code)
	}
}

func (u *unmatchedWriter) Write(b []byte) (int, error) {
	if !u.written {
		u.WriteHeader(http.StatusOK)
	}
	return len(b), nil // discard ServeMux's plain-text body
}

func writeErrorStatus(w http.ResponseWriter, r *http.Request, status int, e *errs.Error) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{
		Code: e.Code, Message: e.Message, RequestID: RequestID(r.Context()),
	}})
}
