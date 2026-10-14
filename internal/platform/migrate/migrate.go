// Package migrate loads versioned SQL migrations from an fs.FS.
// Applying them is the job of a store (see internal/platform/postgres).
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Migration is one forward-only SQL migration.
type Migration struct {
	Version  int64
	Name     string
	SQL      string
	Checksum string // sha256 of SQL; detects edits to applied migrations
}

var fileName = regexp.MustCompile(`^(\d{4,})_([a-z0-9_]+)\.sql$`)

// Load reads every *.sql file at the root of fsys. Files must be named
// NNNN_description.sql (lowercase, at least four digits). Versions must be
// unique; the result is sorted by version. A nil fsys yields no migrations.
func Load(fsys fs.FS) ([]Migration, error) {
	if fsys == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	seen := make(map[int64]string)
	var out []Migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		m := fileName.FindStringSubmatch(entry.Name())
		if m == nil {
			return nil, fmt.Errorf("migration %q: name must match NNNN_description.sql", entry.Name())
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %q: %w", entry.Name(), err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", prev, entry.Name(), version)
		}
		seen[version] = entry.Name()

		body, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sql := strings.TrimSpace(string(body))
		if sql == "" {
			return nil, fmt.Errorf("migration %q is empty", entry.Name())
		}
		sum := sha256.Sum256([]byte(sql))
		out = append(out, Migration{
			Version:  version,
			Name:     m[2],
			SQL:      sql,
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	slices.SortFunc(out, func(a, b Migration) int {
		switch {
		case a.Version < b.Version:
			return -1
		case a.Version > b.Version:
			return 1
		default:
			return 0
		}
	})
	return out, nil
}
