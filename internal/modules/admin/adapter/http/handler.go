// Package http serves the admin dashboard at /_/ and its JSON API at /_/api.
package http

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/service"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/internal/platform/httpx"
	"github.com/mini-base/mini-base/internal/platform/ratelimit"
	"github.com/mini-base/mini-base/pkg/ports"
)

// CookieName is the dashboard session cookie.
const CookieName = "minibase_admin"

// CSRFHeader must accompany every state-changing request. Browsers only send
// custom headers cross-origin after a CORS preflight, which the server never
// grants for it, so a malicious site cannot forge dashboard requests.
const CSRFHeader = "X-Minibase-Admin"

// Accounts is what the handler needs for sign-in.
type Accounts interface {
	SignIn(ctx context.Context, email, password string, c domain.ClientInfo) (service.Session, error)
	Authenticate(ctx context.Context, token string) (domain.Account, error)
	SignOut(ctx context.Context, token string) error
	ChangePassword(ctx context.Context, accountID, current, next string) error
}

// Console is what the handler needs for data features.
type Console interface {
	Overview(ctx context.Context) (service.Overview, error)
	Tables(ctx context.Context) ([]domain.TableSummary, error)
	Table(ctx context.Context, schema, name string) (*domain.Table, error)
	SetRLS(ctx context.Context, schema, name string, enabled bool) (*domain.Table, error)
	Browse(ctx context.Context, schema, name string, sort *domain.SortTerm, limit, offset int) (service.Page, error)
	InsertRow(ctx context.Context, schema, name string, values map[string]json.RawMessage) (json.RawMessage, error)
	UpdateRow(ctx context.Context, schema, name string, key, values map[string]json.RawMessage) (json.RawMessage, error)
	DeleteRow(ctx context.Context, schema, name string, key map[string]json.RawMessage) (json.RawMessage, error)
	RunSQL(ctx context.Context, query string) (domain.SQLRun, error)
	Users() (ports.UserDirectory, error)
}

// Options configure the handler.
type Options struct {
	SecureCookie bool // force the Secure flag (always set when serving TLS)
}

// Handler serves the dashboard.
type Handler struct {
	accounts Accounts
	console  Console
	static   fs.FS
	limiter  *ratelimit.Limiter
	log      *slog.Logger
	opts     Options
}

// NewHandler returns a Handler. static holds index.html and assets/.
func NewHandler(a Accounts, c Console, static fs.FS, limiter *ratelimit.Limiter, log *slog.Logger, opts Options) *Handler {
	return &Handler{accounts: a, console: c, static: static, limiter: limiter, log: log, opts: opts}
}

// Routes registers the dashboard.
func (h *Handler) Routes(r *httpx.Router) {
	r.HandleFunc(http.MethodGet, "/_", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/_/", http.StatusMovedPermanently)
	})

	ui := r.Group("/_", securityHeaders)
	ui.HandleFunc(http.MethodGet, "/{$}", h.index)
	ui.Handle(http.MethodGet, "/assets/", http.StripPrefix("/_/", h.assets()))

	api := ui.Group("/api", noStore, h.csrf)
	api.Handle(http.MethodPost, "/login", h.limiter.Middleware()(http.HandlerFunc(h.login)))

	s := api.Group("", h.requireSession)
	s.HandleFunc(http.MethodGet, "/me", h.me)
	s.HandleFunc(http.MethodPost, "/logout", h.logout)
	s.HandleFunc(http.MethodPost, "/password", h.changePassword)
	s.HandleFunc(http.MethodGet, "/overview", h.overview)
	s.HandleFunc(http.MethodGet, "/tables", h.tables)
	s.HandleFunc(http.MethodGet, "/tables/{schema}/{table}", h.table)
	s.HandleFunc(http.MethodPost, "/tables/{schema}/{table}/rls", h.setRLS)
	s.HandleFunc(http.MethodGet, "/tables/{schema}/{table}/rows", h.browse)
	s.HandleFunc(http.MethodPost, "/tables/{schema}/{table}/rows", h.insertRow)
	s.HandleFunc(http.MethodPatch, "/tables/{schema}/{table}/rows", h.updateRow)
	s.HandleFunc(http.MethodDelete, "/tables/{schema}/{table}/rows", h.deleteRow)
	s.HandleFunc(http.MethodPost, "/sql", h.runSQL)
	s.HandleFunc(http.MethodGet, "/users", h.listUsers)
	s.HandleFunc(http.MethodPost, "/users", h.createUser)
	s.HandleFunc(http.MethodPost, "/users/{id}/signout", h.signOutUser)
	s.HandleFunc(http.MethodDelete, "/users/{id}", h.deleteUser)
}

// ---- static UI ----

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, h.static, "index.html")
}

func (h *Handler) assets() http.Handler {
	fsrv := http.FileServerFS(h.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fsrv.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy allows only same-origin scripts, styles and requests:
// no inline script, no third-party code, no framing.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ---- session & CSRF ----

var errCSRF = errs.Forbidden("admin.csrf", "Request blocked: it did not come from the dashboard")

func (h *Handler) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(CSRFHeader) != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			httpx.WriteError(w, r, h.log, errCSRF)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				httpx.WriteError(w, r, h.log, errCSRF)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type accountKey struct{}

func (h *Handler) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			httpx.WriteError(w, r, h.log, domain.ErrNotSignedIn)
			return
		}
		acct, err := h.accounts.Authenticate(r.Context(), c.Value)
		if err != nil {
			h.clearCookie(w, r)
			httpx.WriteError(w, r, h.log, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountKey{}, acct)))
	})
}

func account(r *http.Request) domain.Account {
	a, _ := r.Context().Value(accountKey{}).(domain.Account)
	return a
}

func (h *Handler) setCookie(w http.ResponseWriter, r *http.Request, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/_/", Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: h.opts.SecureCookie || r.TLS != nil,
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/_/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: h.opts.SecureCookie || r.TLS != nil,
	})
}

// ---- account endpoints ----

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !h.decode(w, r, &req) {
		return
	}
	s, err := h.accounts.SignIn(r.Context(), req.Email, req.Password,
		domain.ClientInfo{IP: ratelimit.ClientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.setCookie(w, r, s.Token, s.ExpiresAt)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": s.Account})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": account(r)})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		if err := h.accounts.SignOut(r.Context(), c.Value); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	h.clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !h.decode(w, r, &req) {
		return
	}
	if err := h.accounts.ChangePassword(r.Context(), account(r).ID, req.Current, req.New); err != nil {
		h.fail(w, r, err)
		return
	}
	h.clearCookie(w, r) // every session ended; sign in again with the new password
	w.WriteHeader(http.StatusNoContent)
}

// ---- data endpoints ----

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ov, err := h.console.Overview(r.Context())
	h.respond(w, r, ov, err)
}

func (h *Handler) tables(w http.ResponseWriter, r *http.Request) {
	ts, err := h.console.Tables(r.Context())
	h.respond(w, r, map[string]any{"tables": ts}, err)
}

func (h *Handler) table(w http.ResponseWriter, r *http.Request) {
	t, err := h.console.Table(r.Context(), r.PathValue("schema"), r.PathValue("table"))
	h.respond(w, r, tableResponse(t), err)
}

func (h *Handler) setRLS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !h.decode(w, r, &req) {
		return
	}
	t, err := h.console.SetRLS(r.Context(), r.PathValue("schema"), r.PathValue("table"), req.Enabled)
	h.respond(w, r, tableResponse(t), err)
}

func tableResponse(t *domain.Table) any {
	if t == nil {
		return nil
	}
	return map[string]any{"table": t, "editable": t.Editable(), "primary_key": t.PrimaryKey()}
}

func (h *Handler) browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	var sort *domain.SortTerm
	if col := q.Get("sort"); col != "" {
		sort = &domain.SortTerm{Column: col, Desc: q.Get("dir") == "desc"}
	}
	page, err := h.console.Browse(r.Context(), r.PathValue("schema"), r.PathValue("table"), sort, limit, offset)
	h.respond(w, r, page, err)
}

type rowRequest struct {
	Key    map[string]json.RawMessage `json:"key"`
	Values map[string]json.RawMessage `json:"values"`
}

func (h *Handler) insertRow(w http.ResponseWriter, r *http.Request) {
	var req rowRequest
	if !h.decode(w, r, &req) {
		return
	}
	row, err := h.console.InsertRow(r.Context(), r.PathValue("schema"), r.PathValue("table"), req.Values)
	h.respondStatus(w, r, http.StatusCreated, map[string]any{"row": row}, err)
}

func (h *Handler) updateRow(w http.ResponseWriter, r *http.Request) {
	var req rowRequest
	if !h.decode(w, r, &req) {
		return
	}
	row, err := h.console.UpdateRow(r.Context(), r.PathValue("schema"), r.PathValue("table"), req.Key, req.Values)
	h.respond(w, r, map[string]any{"row": row}, err)
}

func (h *Handler) deleteRow(w http.ResponseWriter, r *http.Request) {
	var req rowRequest
	if !h.decode(w, r, &req) {
		return
	}
	row, err := h.console.DeleteRow(r.Context(), r.PathValue("schema"), r.PathValue("table"), req.Key)
	h.respond(w, r, map[string]any{"row": row}, err)
}

func (h *Handler) runSQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if !h.decode(w, r, &req) {
		return
	}
	h.log.InfoContext(r.Context(), "sql editor query", slog.String("admin", account(r).Email), slog.Int("bytes", len(req.Query)))
	run, err := h.console.RunSQL(r.Context(), req.Query)
	h.respond(w, r, run, err)
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	dir, err := h.console.Users()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	users, total, err := dir.ListUsers(r.Context(), ports.UserQuery{Search: q.Get("search"), Limit: limit, Offset: offset})
	if users == nil {
		users = []ports.UserSummary{}
	}
	h.respond(w, r, map[string]any{"users": users, "total": total}, err)
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	dir, err := h.console.Users()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !h.decode(w, r, &req) {
		return
	}
	u, err := dir.CreateUser(r.Context(), req.Email, req.Password)
	h.respondStatus(w, r, http.StatusCreated, map[string]any{"user": u}, err)
}

func (h *Handler) signOutUser(w http.ResponseWriter, r *http.Request) {
	dir, err := h.console.Users()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	n, err := dir.RevokeSessions(r.Context(), r.PathValue("id"))
	h.respond(w, r, map[string]any{"revoked_sessions": n}, err)
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	dir, err := h.console.Users()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := dir.DeleteUser(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ----

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(r, dst); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, v any, err error) {
	h.respondStatus(w, r, http.StatusOK, v, err)
}

func (h *Handler) respondStatus(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, v)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteError(w, r, h.log, err)
}
