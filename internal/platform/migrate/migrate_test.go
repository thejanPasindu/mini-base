package migrate_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mini-base/mini-base/internal/platform/migrate"
)

func TestLoadSortsAndChecksums(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"0002_add_index.sql": {Data: []byte("CREATE INDEX i ON t (c);")},
		"0001_init.sql":      {Data: []byte("  CREATE TABLE t (c int);\n")},
		"README.md":          {Data: []byte("ignored")},
	}
	got, err := migrate.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("unexpected order: %+v", got)
	}
	if got[0].Name != "init" || got[0].SQL != "CREATE TABLE t (c int);" {
		t.Fatalf("unexpected migration: %+v", got[0])
	}
	if len(got[0].Checksum) != 64 {
		t.Fatalf("checksum = %q", got[0].Checksum)
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := map[string]fstest.MapFS{
		"bad name":          {"init.sql": {Data: []byte("SELECT 1")}},
		"uppercase":         {"0001_Init.sql": {Data: []byte("SELECT 1")}},
		"duplicate version": {"0001_a.sql": {Data: []byte("SELECT 1")}, "00001_b.sql": {Data: []byte("SELECT 1")}},
		"empty":             {"0001_a.sql": {Data: []byte("   ")}},
	}
	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := migrate.Load(fsys); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadNil(t *testing.T) {
	t.Parallel()

	got, err := migrate.Load(nil)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestChecksumIgnoresSurroundingWhitespaceOnly(t *testing.T) {
	t.Parallel()

	a, _ := migrate.Load(fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 1;")}})
	b, _ := migrate.Load(fstest.MapFS{"0001_a.sql": {Data: []byte("\nSELECT 1;\n")}})
	c, _ := migrate.Load(fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 2;")}})
	if a[0].Checksum != b[0].Checksum {
		t.Fatal("trailing whitespace changed checksum")
	}
	if strings.EqualFold(a[0].Checksum, c[0].Checksum) {
		t.Fatal("different SQL produced same checksum")
	}
}
