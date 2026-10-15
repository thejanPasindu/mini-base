package domain

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// ImportBatchSize is how many CSV rows go into one INSERT.
const ImportBatchSize = 500

// Errors for CSV import.
var (
	ErrEmptyCSV = errs.Invalid("admin.empty_csv", "The file has no header row")
	ErrNoRows   = errs.Invalid("admin.no_rows", "The file has a header but no rows")
)

// ImportPlan maps CSV columns to table columns.
type ImportPlan struct {
	Table   *Table
	Columns []string // table columns written, in CSV order
	Ignored []string // generated columns present in the CSV and skipped
	index   []int    // CSV field index of each entry in Columns
	isJSON  []bool
}

// PlanImport checks the header row against t. Generated columns (identity
// ALWAYS, computed columns) are skipped so a file exported from the
// dashboard can be imported again; any other unknown column is an error.
func PlanImport(t *Table, header []string) (*ImportPlan, error) {
	if t.Kind != "table" {
		return nil, ErrNotEditable
	}
	if len(header) == 0 || (len(header) == 1 && strings.TrimSpace(header[0]) == "") {
		return nil, ErrEmptyCSV
	}
	p := &ImportPlan{Table: t}
	seen := map[string]bool{}
	for i, h := range header {
		name := strings.TrimSpace(h)
		if i == 0 {
			name = strings.TrimPrefix(name, "\ufeff") // byte order mark from spreadsheet apps
		}
		col, ok := t.column(name)
		if !ok {
			return nil, errs.Invalid(ErrUnknownColumn.Code, fmt.Sprintf("Column %q in the file does not exist in %s", name, t.Name))
		}
		if seen[name] {
			return nil, errs.Invalid(ErrDuplicateColumn.Code, fmt.Sprintf("Column %q appears twice in the file", name))
		}
		seen[name] = true
		if col.Generated {
			p.Ignored = append(p.Ignored, name)
			continue
		}
		p.Columns = append(p.Columns, name)
		p.index = append(p.index, i)
		p.isJSON = append(p.isJSON, col.Type == "json" || col.Type == "jsonb")
	}
	if len(p.Columns) == 0 {
		return nil, errs.Invalid("admin.nothing_to_import", "The file has no columns that can be written")
	}
	return p, nil
}

// Statement returns the INSERT for one batch: $1 is a JSON array of
// objects, whose values json_populate_recordset converts to column types.
// It yields the number of rows inserted.
func (p *ImportPlan) Statement() string {
	cols := quoteAll(p.Columns)
	name := p.Table.QualifiedName()
	return fmt.Sprintf("WITH _r AS (INSERT INTO %s (%s) SELECT %s FROM json_populate_recordset(NULL::%s, ($1::text)::json) RETURNING 1) SELECT count(*) FROM _r",
		name, cols, cols, name)
}

// Encode turns CSV records into the JSON batch for Statement. With
// emptyAsNull, empty fields become NULL; otherwise empty strings. Fields of
// json/jsonb columns holding valid JSON are passed as JSON, not as strings.
func (p *ImportPlan) Encode(records [][]string, emptyAsNull bool) (string, error) {
	rows := make([]map[string]any, len(records))
	for r, rec := range records {
		row := make(map[string]any, len(p.Columns))
		for i, col := range p.Columns {
			v := rec[p.index[i]]
			switch {
			case v == "" && emptyAsNull:
				row[col] = nil
			case p.isJSON[i] && json.Valid([]byte(v)):
				row[col] = json.RawMessage(v)
			default:
				row[col] = v
			}
		}
		rows[r] = row
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("encode batch: %w", err)
	}
	return string(b), nil
}
