package service

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/platform/errs"
)

// ---- CSV export and import ----

// Export writes the table's rows matching q as CSV. NULL becomes an empty
// field. open is called once the request has been validated, so errors
// before it can still be reported normally; it returns the writer for the
// file named filename.
func (c *Console) Export(ctx context.Context, schema, name string, q RowQuery, open func(filename string) io.Writer) (int64, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return 0, err
	}
	st, header, err := domain.BuildExport(t, q.Filters, q.Sort)
	if err != nil {
		return 0, err
	}
	w := csv.NewWriter(open(name + ".csv"))
	if err := w.Write(header); err != nil {
		return 0, err
	}
	record := make([]string, len(header))
	n, err := c.data.Export(ctx, st, func(row []*string) error {
		for i, v := range row {
			record[i] = ""
			if v != nil {
				record[i] = *v
			}
		}
		return w.Write(record)
	})
	w.Flush()
	if err == nil {
		err = w.Error()
	}
	c.audit.Record(ctx, domain.ActionExport, schema+"."+name, map[string]any{"rows": n, "filters": q.Filters, "ok": err == nil})
	return n, err
}

// ImportResult reports a CSV import.
type ImportResult struct {
	Rows    int64    `json:"rows"`
	Columns []string `json:"columns"`
	Ignored []string `json:"ignored"` // generated columns skipped
}

// Import inserts the rows of a CSV file (header row first) in one
// transaction: either every row is added or none.
func (c *Console) Import(ctx context.Context, schema, name string, r io.Reader, emptyAsNull bool) (ImportResult, error) {
	t, err := c.Table(ctx, schema, name)
	if err != nil {
		return ImportResult{}, err
	}
	cr := csv.NewReader(r)
	cr.ReuseRecord = false
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return ImportResult{}, domain.ErrEmptyCSV
	}
	if err != nil {
		return ImportResult{}, csvError(err)
	}
	plan, err := domain.PlanImport(t, header)
	if err != nil {
		return ImportResult{}, err
	}

	var (
		batches []string
		chunk   [][]string
		rows    int
	)
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		b, err := plan.Encode(chunk, emptyAsNull)
		if err != nil {
			return err
		}
		batches, chunk = append(batches, b), chunk[:0]
		return nil
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ImportResult{}, csvError(err)
		}
		chunk = append(chunk, rec)
		rows++
		if len(chunk) == domain.ImportBatchSize {
			if err := flush(); err != nil {
				return ImportResult{}, err
			}
		}
	}
	if err := flush(); err != nil {
		return ImportResult{}, err
	}
	if rows == 0 {
		return ImportResult{}, domain.ErrNoRows
	}

	n, failed, err := c.data.Import(ctx, plan.Statement(), batches)
	if err != nil {
		if e, ok := errs.As(err); ok && (e.Kind == errs.KindInvalid || e.Kind == errs.KindConflict) {
			first := failed*domain.ImportBatchSize + 1
			last := min(first+domain.ImportBatchSize-1, rows)
			return ImportResult{}, errs.Invalid("admin.import_failed",
				fmt.Sprintf("Nothing was imported. A row between lines %d and %d of the file failed: %s", first+1, last+1, e.Message)).Wrap(err)
		}
		return ImportResult{}, err
	}
	res := ImportResult{Rows: n, Columns: plan.Columns, Ignored: plan.Ignored}
	if res.Ignored == nil {
		res.Ignored = []string{}
	}
	c.audit.Record(ctx, domain.ActionImport, schema+"."+name, map[string]any{"rows": n, "columns": plan.Columns})
	return res, nil
}

func csvError(err error) error {
	if _, ok := errs.As(err); ok {
		return err // e.g. the upload was too large
	}
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		return errs.Invalid("admin.invalid_csv", fmt.Sprintf("Line %d of the file is not valid CSV: %v", pe.Line, pe.Err)).Wrap(err)
	}
	return errs.Invalid("admin.invalid_csv", "The file could not be read as CSV").Wrap(err)
}

// ---- saved snippets and history (per admin) ----

func (c *Console) accountID(ctx context.Context) (string, error) {
	a, ok := ActorFrom(ctx)
	if !ok || a.AccountID == "" {
		return "", domain.ErrNotSignedIn
	}
	return a.AccountID, nil
}

// Snippets lists the signed-in admin's saved queries.
func (c *Console) Snippets(ctx context.Context) ([]domain.Snippet, error) {
	id, err := c.accountID(ctx)
	if err != nil {
		return nil, err
	}
	out, err := c.snippets.List(ctx, id)
	if out == nil {
		out = []domain.Snippet{}
	}
	return out, err
}

// SaveSnippet creates a snippet (empty snippetID) or updates one.
func (c *Console) SaveSnippet(ctx context.Context, snippetID, name, query string) (domain.Snippet, error) {
	id, err := c.accountID(ctx)
	if err != nil {
		return domain.Snippet{}, err
	}
	name, err = domain.ValidateSnippet(name, query)
	if err != nil {
		return domain.Snippet{}, err
	}
	if snippetID == "" {
		return c.snippets.Create(ctx, id, name, query)
	}
	return c.snippets.Update(ctx, id, snippetID, name, query)
}

// DeleteSnippet removes a snippet.
func (c *Console) DeleteSnippet(ctx context.Context, snippetID string) error {
	id, err := c.accountID(ctx)
	if err != nil {
		return err
	}
	return c.snippets.Delete(ctx, id, snippetID)
}

// History returns the signed-in admin's recent SQL editor runs.
func (c *Console) History(ctx context.Context, limit int) ([]domain.HistoryEntry, error) {
	return c.audit.History(ctx, limit)
}

// AuditLog returns audit entries, newest first.
func (c *Console) AuditLog(ctx context.Context, q domain.AuditQuery) ([]domain.AuditEntry, error) {
	return c.audit.List(ctx, q)
}

// ---- settings and keys ----

// Setting is one configuration variable (see kernel.Setting).
type Setting struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Default string `json:"default"`
	IsSet   bool   `json:"is_set"`
	Secret  bool   `json:"secret"`
}

// SettingsView is the API and settings page.
type SettingsView struct {
	Server      Info      `json:"server"`
	AnonKey     string    `json:"anon_key"`    // public by design
	ServiceKey  string    `json:"service_key"` // masked
	RESTSchemas []string  `json:"rest_schemas"`
	Settings    []Setting `json:"settings"`
}

// Settings returns the configuration in effect.
func (c *Console) Settings() SettingsView {
	v := SettingsView{Server: c.info, Settings: c.settings(), RESTSchemas: []string{}}
	if v.Settings == nil {
		v.Settings = []Setting{}
	}
	for _, s := range v.Settings {
		switch s.Name {
		case "MINIBASE_ANON_KEY":
			v.AnonKey = s.Value
		case "MINIBASE_SERVICE_KEY":
			v.ServiceKey = s.Value
		case "MINIBASE_DATABASE_SCHEMAS":
			for _, p := range strings.Split(s.Value, ",") {
				if p = strings.TrimSpace(p); p != "" {
					v.RESTSchemas = append(v.RESTSchemas, p)
				}
			}
		}
	}
	return v
}
