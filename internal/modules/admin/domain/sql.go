package domain

import "strings"

// SQLColumn is a result column of the SQL editor.
type SQLColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SQLResult is the outcome of one statement in the SQL editor. Values are
// Postgres's text representation; nil means NULL.
type SQLResult struct {
	Columns   []SQLColumn `json:"columns"`
	Rows      [][]*string `json:"rows"`
	Command   string      `json:"command"` // e.g. "SELECT 3", "CREATE TABLE"
	Truncated bool        `json:"truncated"`
}

// SQLError describes a failed statement.
type SQLError struct {
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Code     string `json:"code,omitempty"`
	Position int32  `json:"position,omitempty"` // 1-based character offset in the query
}

// SQLRun is the full outcome of running a script.
type SQLRun struct {
	Results    []SQLResult `json:"results"`
	Error      *SQLError   `json:"error"`
	DurationMS int64       `json:"duration_ms"`
}

// ChangesSchema reports whether any executed command may have changed the
// schema or its security, so caches should be refreshed.
func (r SQLRun) ChangesSchema() bool {
	for _, res := range r.Results {
		verb, _, _ := strings.Cut(res.Command, " ")
		switch verb {
		case "CREATE", "ALTER", "DROP", "GRANT", "REVOKE", "COMMENT", "TRUNCATE":
			return true
		}
	}
	return false
}
