package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// The table designer turns form input into DDL. Everything spliced into SQL
// is either a validated identifier (then quoted), a value from a fixed
// allow-list, or a string literal escaped by QuoteLiteral. Free-form SQL is
// accepted only for custom policy expressions, which admins could equally
// type into the SQL editor.

// Errors for the table designer.
var (
	ErrInvalidName     = errs.Invalid("admin.invalid_name", "Names must start with a lowercase letter or underscore and contain only a-z, 0-9 and _ (max 63)")
	ErrUnknownType     = errs.Invalid("admin.unknown_type", "Unsupported column type")
	ErrDuplicateColumn = errs.Invalid("admin.duplicate_column", "Two columns have the same name")
	ErrNoColumns       = errs.Invalid("admin.no_columns", "Add at least one column")
	ErrSchemaNotFound  = errs.NotFound("admin.schema_not_found", "Schema not found")
	ErrConfirmName     = errs.Invalid("admin.confirm_name", "Type the table name to confirm")
	ErrPolicyNotFound  = errs.NotFound("admin.policy_not_found", "Policy not found")
	ErrUnknownTemplate = errs.Invalid("admin.unknown_template", "Unknown policy template")
)

var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ValidateName checks a table or column name for the designer. Existing
// objects may have any name; new ones get simple names that never need
// quoting by API clients.
func ValidateName(name string) error {
	if !identRE.MatchString(name) {
		return errs.Invalid(ErrInvalidName.Code, fmt.Sprintf("%q is not a valid name. %s", name, ErrInvalidName.Message))
	}
	return nil
}

// QuoteLiteral quotes a string literal. The designer runs DDL with
// standard_conforming_strings on, where doubling quotes is the only escape.
func QuoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ColumnTypes are the types the designer offers, in menu order.
var ColumnTypes = []string{
	"text", "boolean", "smallint", "integer", "bigint", "numeric", "real", "double precision",
	"uuid", "date", "time", "timestamp", "timestamptz", "interval",
	"jsonb", "json", "bytea", "inet",
	"text[]", "integer[]", "bigint[]", "uuid[]",
}

// DefaultExpressions are the non-literal defaults the designer offers.
var DefaultExpressions = []string{"now()", "current_date", "gen_random_uuid()", "auth.uid()"}

// ColumnDefault is either an expression from DefaultExpressions or a literal
// value converted to the column type.
type ColumnDefault struct {
	Expression string  `json:"expression,omitempty"`
	Value      *string `json:"value,omitempty"`
}

// Reference is a foreign key target.
type Reference struct {
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Column   string `json:"column"`
	OnDelete string `json:"on_delete"` // cascade, set null, restrict, no action (default)
}

// AuthUsers is the one internal table the designer may reference, so rows
// can belong to app users.
var AuthUsers = Reference{Schema: "auth", Table: "users", Column: "id"}

// IsAuthUsers reports whether r points at auth.users(id).
func (r Reference) IsAuthUsers() bool {
	return r.Schema == AuthUsers.Schema && r.Table == AuthUsers.Table && r.Column == AuthUsers.Column
}

var onDeleteActions = map[string]string{"": "", "no action": "", "cascade": " ON DELETE CASCADE", "set null": " ON DELETE SET NULL", "restrict": " ON DELETE RESTRICT"}

// ColumnSpec describes a new column.
type ColumnSpec struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Nullable   bool           `json:"nullable"`
	Unique     bool           `json:"unique"`
	Default    *ColumnDefault `json:"default,omitempty"`
	References *Reference     `json:"references,omitempty"`
}

// Validate checks the column spec.
func (c ColumnSpec) Validate() error {
	if err := ValidateName(c.Name); err != nil {
		return err
	}
	if !slices.Contains(ColumnTypes, c.Type) {
		return errs.Invalid(ErrUnknownType.Code, fmt.Sprintf("Column %q: unsupported type %q", c.Name, c.Type))
	}
	if d := c.Default; d != nil {
		if (d.Expression == "") == (d.Value == nil) {
			return errs.Invalid("admin.invalid_default", fmt.Sprintf("Column %q: give a default expression or a value, not both", c.Name))
		}
		if d.Expression != "" && !slices.Contains(DefaultExpressions, d.Expression) {
			return errs.Invalid("admin.invalid_default", fmt.Sprintf("Column %q: unsupported default %q", c.Name, d.Expression))
		}
	}
	if r := c.References; r != nil {
		if _, ok := onDeleteActions[r.OnDelete]; !ok {
			return errs.Invalid("admin.invalid_reference", fmt.Sprintf("Column %q: on delete must be cascade, set null, restrict or no action", c.Name))
		}
		if r.OnDelete == "set null" && !c.Nullable {
			return errs.Invalid("admin.invalid_reference", fmt.Sprintf("Column %q: \"set null\" needs a nullable column", c.Name))
		}
		if r.Schema == "" || r.Table == "" || r.Column == "" {
			return errs.Invalid("admin.invalid_reference", fmt.Sprintf("Column %q: choose the referenced table and column", c.Name))
		}
	}
	return nil
}

// definition renders `"name" type [NOT NULL] [UNIQUE] [DEFAULT ...] [REFERENCES ...]`.
func (c ColumnSpec) definition() string {
	var b strings.Builder
	b.WriteString(QuoteIdent(c.Name) + " " + c.Type)
	if !c.Nullable {
		b.WriteString(" NOT NULL")
	}
	if c.Unique {
		b.WriteString(" UNIQUE")
	}
	if d := c.Default; d != nil {
		if d.Expression != "" {
			b.WriteString(" DEFAULT " + d.Expression)
		} else {
			b.WriteString(" DEFAULT " + QuoteLiteral(*d.Value) + "::" + c.Type)
		}
	}
	if r := c.References; r != nil {
		b.WriteString(fmt.Sprintf(" REFERENCES %s.%s (%s)%s",
			QuoteIdent(r.Schema), QuoteIdent(r.Table), QuoteIdent(r.Column), onDeleteActions[r.OnDelete]))
	}
	return b.String()
}

// Primary key choices for new tables.
const (
	PrimaryKeyBigint = "bigint" // bigint identity, numbered by the database
	PrimaryKeyUUID   = "uuid"   // uuid with gen_random_uuid()
	PrimaryKeyNone   = "none"
)

// TableSpec describes a new table.
type TableSpec struct {
	Schema     string       `json:"schema"`
	Name       string       `json:"name"`
	PrimaryKey string       `json:"primary_key"`
	Columns    []ColumnSpec `json:"columns"`
	Timestamps bool         `json:"timestamps"` // adds created_at
	EnableRLS  bool         `json:"enable_rls"`
}

// BuildCreateTable returns the statements creating the table, to run in one
// transaction.
func BuildCreateTable(s TableSpec) ([]string, error) {
	if err := ValidateName(s.Name); err != nil {
		return nil, err
	}
	var defs []string
	names := map[string]bool{}
	add := func(name, def string) error {
		if names[name] {
			return errs.Invalid(ErrDuplicateColumn.Code, fmt.Sprintf("There are two columns named %q", name))
		}
		names[name] = true
		defs = append(defs, def)
		return nil
	}
	switch s.PrimaryKey {
	case PrimaryKeyBigint, "":
		_ = add("id", `"id" bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY`)
	case PrimaryKeyUUID:
		_ = add("id", `"id" uuid PRIMARY KEY DEFAULT gen_random_uuid()`)
	case PrimaryKeyNone:
	default:
		return nil, errs.Invalid("admin.invalid_primary_key", "Primary key must be bigint, uuid or none")
	}
	for _, c := range s.Columns {
		if err := c.Validate(); err != nil {
			return nil, err
		}
		if err := add(c.Name, c.definition()); err != nil {
			return nil, err
		}
	}
	if s.Timestamps {
		if err := add("created_at", `"created_at" timestamptz NOT NULL DEFAULT now()`); err != nil {
			return nil, err
		}
	}
	if len(defs) == 0 {
		return nil, ErrNoColumns
	}
	name := QuoteIdent(s.Schema) + "." + QuoteIdent(s.Name)
	stmts := []string{"CREATE TABLE " + name + " (\n  " + strings.Join(defs, ",\n  ") + "\n)"}
	if s.EnableRLS {
		stmts = append(stmts, "ALTER TABLE "+name+" ENABLE ROW LEVEL SECURITY")
	}
	return stmts, nil
}

// BuildDropTable drops t. confirm must equal the table name. Dependent
// objects are not dropped: Postgres refuses and says what depends on it.
func BuildDropTable(t *Table, confirm string) ([]string, error) {
	if confirm != t.Name {
		return nil, ErrConfirmName
	}
	kind := map[string]string{"table": "TABLE", "view": "VIEW", "materialized_view": "MATERIALIZED VIEW", "foreign_table": "FOREIGN TABLE"}[t.Kind]
	return []string{"DROP " + kind + " " + t.QualifiedName()}, nil
}

// BuildAddColumn adds a column to t.
func BuildAddColumn(t *Table, c ColumnSpec) ([]string, error) {
	if t.Kind != "table" {
		return nil, ErrNotEditable
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if _, ok := t.column(c.Name); ok {
		return nil, errs.Invalid(ErrDuplicateColumn.Code, fmt.Sprintf("Column %q already exists", c.Name))
	}
	return []string{"ALTER TABLE " + t.QualifiedName() + " ADD COLUMN " + c.definition()}, nil
}

// BuildDropColumn drops a column from t.
func BuildDropColumn(t *Table, column string) ([]string, error) {
	if t.Kind != "table" {
		return nil, ErrNotEditable
	}
	if _, ok := t.column(column); !ok {
		return nil, unknownColumn(column)
	}
	return []string{"ALTER TABLE " + t.QualifiedName() + " DROP COLUMN " + QuoteIdent(column)}, nil
}

// ---- row level security policies ----

// Database roles behind the API's anon and signed-in callers.
const (
	RoleAnon          = "minibase_anon"
	RoleAuthenticated = "minibase_authenticated"
)

// PolicyTemplate is a ready-made policy.
type PolicyTemplate struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	NeedsColumn bool   `json:"needs_column"` // asks for the owner column (uuid)
}

// PolicyTemplates are offered in this order; "custom" takes free-form
// expressions.
var PolicyTemplates = []PolicyTemplate{
	{ID: "public_read", Label: "Anyone can read", Description: "Everyone, signed in or not, can read every row."},
	{ID: "signed_in_read", Label: "Signed-in users can read", Description: "Any signed-in user can read every row."},
	{ID: "signed_in_insert", Label: "Signed-in users can add rows", Description: "Any signed-in user can insert rows."},
	{ID: "owner_all", Label: "Owners manage their own rows", NeedsColumn: true,
		Description: "Signed-in users can read, add, change and delete only rows whose owner column holds their user id."},
	{ID: "owner_read", Label: "Owners can read their own rows", NeedsColumn: true,
		Description: "Signed-in users can read only rows whose owner column holds their user id."},
	{ID: "custom", Label: "Custom", Description: "Write the USING and WITH CHECK expressions yourself."},
}

// PolicySpec is the request to add a policy.
type PolicySpec struct {
	Template  string   `json:"template"`
	Name      string   `json:"name"`       // optional; defaults to the template label
	Column    string   `json:"column"`     // owner column for owner_* templates
	Command   string   `json:"command"`    // custom: ALL, SELECT, INSERT, UPDATE, DELETE
	Roles     []string `json:"roles"`      // custom: "anon" and/or "authenticated"
	Using     string   `json:"using"`      // custom
	WithCheck string   `json:"with_check"` // custom
}

// BuildPolicy renders CREATE POLICY for t.
func BuildPolicy(t *Table, s PolicySpec) (string, error) {
	if t.Kind != "table" {
		return "", errs.Invalid("admin.rls_not_supported", "Row level security applies to tables only")
	}
	var (
		command, using, check string
		roles                 []string
	)
	owner := func() (string, error) {
		col, ok := t.column(s.Column)
		if !ok {
			return "", errs.Invalid("admin.owner_column", "Choose the column that holds the owner's user id")
		}
		if col.Type != "uuid" {
			return "", errs.Invalid("admin.owner_column", fmt.Sprintf("Owner column %q must be of type uuid (it is %s)", col.Name, col.Type))
		}
		return QuoteIdent(col.Name) + " = auth.uid()", nil
	}
	label := ""
	for _, tpl := range PolicyTemplates {
		if tpl.ID == s.Template {
			label = tpl.Label
		}
	}
	switch s.Template {
	case "public_read":
		command, roles, using = "SELECT", []string{RoleAnon, RoleAuthenticated}, "true"
	case "signed_in_read":
		command, roles, using = "SELECT", []string{RoleAuthenticated}, "true"
	case "signed_in_insert":
		command, roles, check = "INSERT", []string{RoleAuthenticated}, "true"
	case "owner_all":
		expr, err := owner()
		if err != nil {
			return "", err
		}
		command, roles, using, check = "ALL", []string{RoleAuthenticated}, expr, expr
	case "owner_read":
		expr, err := owner()
		if err != nil {
			return "", err
		}
		command, roles, using = "SELECT", []string{RoleAuthenticated}, expr
	case "custom":
		var err error
		if command, roles, err = customPolicy(s); err != nil {
			return "", err
		}
		using, check = strings.TrimSpace(s.Using), strings.TrimSpace(s.WithCheck)
	default:
		return "", ErrUnknownTemplate
	}

	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = label
	}
	if name == "" || len(name) > 63 {
		return "", errs.Invalid("admin.policy_name", "Policy names must be 1 to 63 characters")
	}
	sql := fmt.Sprintf("CREATE POLICY %s ON %s AS PERMISSIVE FOR %s TO %s", QuoteIdent(name), t.QualifiedName(), command, strings.Join(roles, ", "))
	if using != "" {
		sql += " USING (" + using + ")"
	}
	if check != "" {
		sql += " WITH CHECK (" + check + ")"
	}
	return sql, nil
}

func customPolicy(s PolicySpec) (string, []string, error) {
	command := strings.ToUpper(strings.TrimSpace(s.Command))
	if !slices.Contains([]string{"ALL", "SELECT", "INSERT", "UPDATE", "DELETE"}, command) {
		return "", nil, errs.Invalid("admin.policy_command", "Command must be ALL, SELECT, INSERT, UPDATE or DELETE")
	}
	var roles []string
	for _, r := range s.Roles {
		switch r {
		case "anon":
			roles = append(roles, RoleAnon)
		case "authenticated":
			roles = append(roles, RoleAuthenticated)
		default:
			return "", nil, errs.Invalid("admin.policy_roles", fmt.Sprintf("Unknown role %q (use anon or authenticated)", r))
		}
	}
	if len(roles) == 0 {
		return "", nil, errs.Invalid("admin.policy_roles", "Choose who the policy applies to")
	}
	using, check := strings.TrimSpace(s.Using), strings.TrimSpace(s.WithCheck)
	switch {
	case len(using) > 2000 || len(check) > 2000:
		return "", nil, errs.Invalid("admin.policy_expression", "Expressions must be at most 2000 characters")
	case command == "INSERT" && using != "":
		return "", nil, errs.Invalid("admin.policy_expression", "INSERT policies take only a WITH CHECK expression")
	case (command == "SELECT" || command == "DELETE") && check != "":
		return "", nil, errs.Invalid("admin.policy_expression", command+" policies take only a USING expression")
	case command == "INSERT" && check == "":
		return "", nil, errs.Invalid("admin.policy_expression", "Enter the WITH CHECK expression")
	case command != "INSERT" && using == "":
		return "", nil, errs.Invalid("admin.policy_expression", "Enter the USING expression")
	}
	return command, roles, nil
}

// BuildDropPolicy drops the named policy from t.
func BuildDropPolicy(t *Table, name string) (string, error) {
	if !slices.ContainsFunc(t.Policies, func(p Policy) bool { return p.Name == name }) {
		return "", ErrPolicyNotFound
	}
	return "DROP POLICY " + QuoteIdent(name) + " ON " + t.QualifiedName(), nil
}
