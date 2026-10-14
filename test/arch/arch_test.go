// Package arch enforces the architecture rules from docs/architecture.md §4.4
// by inspecting imports. A failing test here means a layering violation.
package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/mini-base/mini-base"

type goFile struct {
	path    string // slash-separated, relative to repo root
	imports []string
}

func loadFiles(t *testing.T) []goFile {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	var files []goFile
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, p, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		gf := goFile{path: filepath.ToSlash(rel)}
		for _, imp := range f.Imports {
			v, _ := strconv.Unquote(imp.Path.Value)
			gf.imports = append(gf.imports, v)
		}
		files = append(files, gf)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found; is the test running from test/arch?")
	}
	return files
}

// moduleOf returns "auth" for "internal/modules/auth/..." paths.
func moduleOf(p string) string {
	rest, ok := strings.CutPrefix(p, "internal/modules/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return name
}

func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

func TestLayering(t *testing.T) {
	t.Parallel()

	modules := modulePath + "/internal/modules/"
	for _, f := range loadFiles(t) {
		ownPkg := modulePath + "/" + path.Dir(f.path)
		for _, imp := range f.imports {
			if imp == ownPkg { // external _test packages import the package under test
				continue
			}
			local, isLocal := strings.CutPrefix(imp, modulePath+"/")

			switch {
			case strings.HasPrefix(f.path, "internal/kernel/") && strings.HasPrefix(imp, modules):
				t.Errorf("%s: kernel must not import modules (%s)", f.path, imp)

			case strings.HasPrefix(f.path, "internal/platform/") && isLocal &&
				(strings.HasPrefix(local, "internal/kernel") || strings.HasPrefix(local, "internal/modules")):
				t.Errorf("%s: platform must not import kernel or modules (%s)", f.path, imp)
			}

			if own := moduleOf(f.path); own != "" && strings.HasPrefix(imp, modules) {
				if other := moduleOf(local); other != own {
					t.Errorf("%s: module %q must not import module %q; use pkg/ports or the event bus", f.path, own, other)
				}
			}

			if strings.Contains(f.path, "/domain/") && moduleOf(f.path) != "" &&
				!isStdlib(imp) && imp != modulePath+"/internal/platform/errs" {
				t.Errorf("%s: domain may import only the standard library and platform/errs (%s)", f.path, imp)
			}

			if strings.Contains(f.path, "/service/") && moduleOf(f.path) != "" && !strings.HasSuffix(f.path, "_test.go") {
				if strings.Contains(imp, "/adapter/") || strings.HasPrefix(imp, "github.com/jackc/") || imp == "net/http" {
					t.Errorf("%s: service must depend on ports, not adapters or I/O libraries (%s)", f.path, imp)
				}
			}
		}
	}
}
