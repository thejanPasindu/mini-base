package domain

import (
	"bytes"
	"encoding/json"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// ErrInvalidBody is returned for bodies that are not the expected JSON shape.
var ErrInvalidBody = errs.Invalid("database.invalid_body", "Body must be a JSON object, or for inserts an array of objects")

// ParseRows accepts a JSON object or a non-empty array of objects (POST) and
// returns the union of their keys, in first-seen order, and the rows as a
// JSON array for json_populate_recordset.
func ParseRows(body []byte) (columns []string, rowsJSON string, err error) {
	body = bytes.TrimSpace(body)
	var rows []map[string]json.RawMessage
	switch {
	case len(body) > 0 && body[0] == '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(body, &obj); err != nil {
			return nil, "", ErrInvalidBody
		}
		rows = []map[string]json.RawMessage{obj}
		body = append(append([]byte{'['}, body...), ']')
	case len(body) > 0 && body[0] == '[':
		if err := json.Unmarshal(body, &rows); err != nil || len(rows) == 0 {
			return nil, "", ErrInvalidBody
		}
	default:
		return nil, "", ErrInvalidBody
	}

	// Every row must set the same columns. Otherwise a key missing from one
	// row would become NULL rather than the column default — a silent and
	// surprising difference.
	for i, r := range rows {
		if r == nil {
			return nil, "", ErrInvalidBody // a null array element
		}
		keys := orderedKeys(r)
		if i == 0 {
			columns = keys
			continue
		}
		if !equal(keys, columns) {
			return nil, "", ErrMixedColumns
		}
	}
	return columns, string(body), nil
}

// ErrMixedColumns is returned when bulk-insert rows set different columns.
var ErrMixedColumns = errs.Invalid("database.mixed_columns",
	"All objects in a bulk insert must have the same keys; split the request or set the missing keys explicitly")

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ParseObject accepts a single JSON object (PATCH).
func ParseObject(body []byte) (columns []string, objJSON string, err error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, "", ErrInvalidBody
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, "", ErrInvalidBody
	}
	return orderedKeys(obj), string(body), nil
}

// orderedKeys returns map keys sorted, so generated SQL is deterministic.
func orderedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
