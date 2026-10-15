package http

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/platform/errs"
)

// previewing reports ?preview=1: return the SQL without running it.
func previewing(r *http.Request) bool { return r.URL.Query().Get("preview") == "1" }

// ---- table designer ----

func (h *Handler) designerMeta(w http.ResponseWriter, r *http.Request) {
	schemas, err := h.console.Schemas(r.Context())
	h.respond(w, r, map[string]any{
		"schemas":             schemas,
		"types":               domain.ColumnTypes,
		"default_expressions": domain.DefaultExpressions,
		"policy_templates":    domain.PolicyTemplates,
		"filter_ops":          domain.FilterOps,
	}, err)
}

func (h *Handler) createTable(w http.ResponseWriter, r *http.Request) {
	var spec domain.TableSpec
	if !h.decode(w, r, &spec) {
		return
	}
	res, err := h.console.CreateTable(r.Context(), spec, previewing(r))
	status := http.StatusCreated
	if res.Preview {
		status = http.StatusOK
	}
	h.respondStatus(w, r, status, res, err)
}

func (h *Handler) dropTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !h.decode(w, r, &req) {
		return
	}
	res, err := h.console.DropTable(r.Context(), r.PathValue("schema"), r.PathValue("table"), req.Confirm, previewing(r))
	h.respond(w, r, res, err)
}

func (h *Handler) addColumn(w http.ResponseWriter, r *http.Request) {
	var col domain.ColumnSpec
	if !h.decode(w, r, &col) {
		return
	}
	res, err := h.console.AddColumn(r.Context(), r.PathValue("schema"), r.PathValue("table"), col, previewing(r))
	h.respond(w, r, res, err)
}

func (h *Handler) dropColumn(w http.ResponseWriter, r *http.Request) {
	res, err := h.console.DropColumn(r.Context(), r.PathValue("schema"), r.PathValue("table"), r.PathValue("column"), previewing(r))
	h.respond(w, r, res, err)
}

func (h *Handler) addPolicy(w http.ResponseWriter, r *http.Request) {
	var spec domain.PolicySpec
	if !h.decode(w, r, &spec) {
		return
	}
	res, err := h.console.AddPolicy(r.Context(), r.PathValue("schema"), r.PathValue("table"), spec, previewing(r))
	h.respond(w, r, res, err)
}

func (h *Handler) dropPolicy(w http.ResponseWriter, r *http.Request) {
	res, err := h.console.DropPolicy(r.Context(), r.PathValue("schema"), r.PathValue("table"), r.PathValue("policy"), previewing(r))
	h.respond(w, r, res, err)
}

// ---- CSV export and import ----

// transferTimeout replaces the server's write/read timeout for exports and
// imports, which may take longer than an API call.
const transferTimeout = 10 * time.Minute

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	rq, err := rowQuery(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	started := false
	_, err = h.console.Export(r.Context(), r.PathValue("schema"), r.PathValue("table"), rq, func(filename string) io.Writer {
		started = true
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferTimeout))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		w.WriteHeader(http.StatusOK)
		return w
	})
	switch {
	case err == nil:
	case !started:
		h.fail(w, r, err)
	default:
		// Headers are gone; the truncated download is all the client sees.
		h.log.WarnContext(r.Context(), "csv export interrupted", "error", err)
	}
}

var errFileTooLarge = errs.Invalid("admin.file_too_large", "The file is larger than MINIBASE_HTTP_MAX_BODY_BYTES allows")

// tooLarge turns the body limit's error into a message for the operator.
type tooLarge struct{ r io.Reader }

func (t tooLarge) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return n, errFileTooLarge
	}
	return n, err
}

func (h *Handler) importCSV(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(transferTimeout))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferTimeout))
	emptyAsNull := r.URL.Query().Get("empty") != "string"
	res, err := h.console.Import(r.Context(), r.PathValue("schema"), r.PathValue("table"), tooLarge{r.Body}, emptyAsNull)
	h.respond(w, r, res, err)
}

// ---- snippets and history ----

func (h *Handler) listSnippets(w http.ResponseWriter, r *http.Request) {
	list, err := h.console.Snippets(r.Context())
	h.respond(w, r, map[string]any{"snippets": list}, err)
}

func (h *Handler) saveSnippet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}
	if !h.decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	sn, err := h.console.SaveSnippet(r.Context(), id, req.Name, req.Query)
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	h.respondStatus(w, r, status, map[string]any{"snippet": sn}, err)
}

func (h *Handler) deleteSnippet(w http.ResponseWriter, r *http.Request) {
	h.noContent(w, r, h.console.DeleteSnippet(r.Context(), r.PathValue("id")))
}

func (h *Handler) sqlHistory(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.console.History(r.Context(), limit)
	h.respond(w, r, map[string]any{"history": list}, err)
}

// ---- settings, audit log, admins ----

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	h.respond(w, r, h.console.Settings(), nil)
}

func (h *Handler) auditLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := h.console.AuditLog(r.Context(), domain.AuditQuery{Action: q.Get("action"), Actor: q.Get("actor"), Before: before, Limit: limit})
	h.respond(w, r, map[string]any{"entries": list}, err)
}

func (h *Handler) listAdmins(w http.ResponseWriter, r *http.Request) {
	list, err := h.accounts.List(r.Context())
	if list == nil {
		list = []domain.Account{}
	}
	h.respond(w, r, map[string]any{"admins": list}, err)
}

func (h *Handler) createAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"` // empty: generate one
	}
	if !h.decode(w, r, &req) {
		return
	}
	acct, generated, err := h.accounts.CreateAccount(r.Context(), req.Email, req.Password)
	h.respondStatus(w, r, http.StatusCreated, map[string]any{"admin": acct, "generated_password": generated}, err)
}

func (h *Handler) deleteAdmin(w http.ResponseWriter, r *http.Request) {
	h.noContent(w, r, h.accounts.DeleteAccount(r.Context(), r.PathValue("id")))
}

func (h *Handler) resetAdminPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"` // empty: generate one
	}
	if !h.decode(w, r, &req) {
		return
	}
	generated, err := h.accounts.ResetAccountPassword(r.Context(), r.PathValue("id"), req.Password)
	h.respond(w, r, map[string]any{"generated_password": generated}, err)
}

func (h *Handler) signOutAdmin(w http.ResponseWriter, r *http.Request) {
	h.noContent(w, r, h.accounts.SignOutAccount(r.Context(), r.PathValue("id")))
}
