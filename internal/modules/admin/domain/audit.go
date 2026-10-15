package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// Actor is who performs a dashboard action.
type Actor struct {
	AccountID string // empty for the command line
	Email     string // "cli" for the command line
	IP        string
}

// CLIActor performs actions run with `minibase admin ...`.
var CLIActor = Actor{Email: "cli"}

// Audit actions. The prefix before the dot groups them in the log filter.
const (
	ActionSignIn         = "admin.sign_in"
	ActionSignInFailed   = "admin.sign_in_failed"
	ActionSignOut        = "admin.sign_out"
	ActionPasswordChange = "admin.password_change"
	ActionAdminCreate    = "admin.create"
	ActionAdminDelete    = "admin.delete"
	ActionAdminReset     = "admin.password_reset"
	ActionAdminSignOut   = "admin.sign_out_everywhere"

	ActionSQLRun = "sql.run"

	ActionTableCreate  = "schema.table_create"
	ActionTableDrop    = "schema.table_drop"
	ActionColumnAdd    = "schema.column_add"
	ActionColumnDrop   = "schema.column_drop"
	ActionRLSEnable    = "schema.rls_enable"
	ActionRLSDisable   = "schema.rls_disable"
	ActionPolicyCreate = "schema.policy_create"
	ActionPolicyDrop   = "schema.policy_drop"

	ActionRowInsert = "data.row_insert"
	ActionRowUpdate = "data.row_update"
	ActionRowDelete = "data.row_delete"
	ActionImport    = "data.import"
	ActionExport    = "data.export"

	ActionUserCreate  = "users.create"
	ActionUserDelete  = "users.delete"
	ActionUserSignOut = "users.sign_out"
)

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	ID        int64          `json:"id"`
	At        time.Time      `json:"at"`
	AccountID *string        `json:"account_id"`
	Actor     string         `json:"actor"` // email at the time, or "cli"
	IP        string         `json:"ip"`
	Action    string         `json:"action"`
	Target    string         `json:"target"` // e.g. public.todos, an email
	Detail    map[string]any `json:"detail"`
}

// AuditQuery filters the audit log. Results are newest first; pass the
// last ID seen as Before to page.
type AuditQuery struct {
	Action string // exact action, or a group prefix such as "schema."
	Actor  string // exact email
	Before int64
	Limit  int
}

// Normalize applies defaults and bounds.
func (q AuditQuery) Normalize() AuditQuery {
	if q.Limit < 1 || q.Limit > 200 {
		q.Limit = 50
	}
	q.Action = strings.TrimSpace(q.Action)
	q.Actor = strings.ToLower(strings.TrimSpace(q.Actor))
	return q
}

// MaxAuditedSQL bounds how much of a query the audit log keeps.
const MaxAuditedSQL = 100_000

// TruncateSQL shortens a query for the audit log.
func TruncateSQL(q string) string {
	if len(q) <= MaxAuditedSQL {
		return q
	}
	cut := q[:MaxAuditedSQL]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n-- (truncated)"
}

// HistoryEntry is one SQL editor run, from the audit log.
type HistoryEntry struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	Query      string    `json:"query"`
	OK         bool      `json:"ok"`
	DurationMS int64     `json:"duration_ms"`
}

// Snippet is a saved SQL editor query, private to one admin.
type Snippet struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Query     string    `json:"query"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Snippet errors.
var (
	ErrSnippetNotFound = errs.NotFound("admin.snippet_not_found", "Snippet not found")
	ErrSnippetExists   = errs.Conflict("admin.snippet_exists", "You already have a snippet with this name")
)

// ValidateSnippet checks a snippet's name and query.
func ValidateSnippet(name, query string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "" || utf8.RuneCountInString(name) > 200:
		return "", errs.Invalid("admin.snippet_name", "Snippet names must be 1 to 200 characters")
	case strings.TrimSpace(query) == "":
		return "", errs.Invalid("admin.empty_query", "The snippet has no SQL")
	case len(query) > MaxAuditedSQL:
		return "", errs.Invalid("admin.snippet_too_long", "Snippets must be at most 100,000 bytes")
	}
	return name, nil
}

// Errors for managing admins from the dashboard.
var (
	ErrCannotDeleteSelf  = errs.Invalid("admin.cannot_delete_self", "You can't delete your own account. Ask another admin, or use the command line.")
	ErrLastAccount       = errs.Conflict("admin.last_account", "This is the only admin. Add another admin first.")
	ErrUseChangePassword = errs.Invalid("admin.use_change_password", "To change your own password, use Change password (it asks for your current one).")
)
