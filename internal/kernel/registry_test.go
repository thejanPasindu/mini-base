package kernel_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mini-base/mini-base/internal/kernel"
)

type fakeModule struct {
	kernel.BaseModule
	name     string
	deps     []string
	optional []string
}

func (f fakeModule) Name() string                            { return f.name }
func (f fakeModule) DependsOn() []string                     { return f.deps }
func (f fakeModule) OptionalDependsOn() []string             { return f.optional }
func (f fakeModule) Init(context.Context, kernel.Deps) error { return nil }

func newRegistry(t *testing.T, mods ...kernel.Module) *kernel.Registry {
	t.Helper()
	r := kernel.NewRegistry()
	for _, m := range mods {
		if err := r.Register(m); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func names(mods []kernel.Module) string {
	out := make([]string, len(mods))
	for i, m := range mods {
		out[i] = m.Name()
	}
	return strings.Join(out, ",")
}

func TestResolveOrdersDependenciesFirst(t *testing.T) {
	t.Parallel()

	r := newRegistry(t,
		fakeModule{name: "storage", deps: []string{"auth"}},
		fakeModule{name: "auth"},
		fakeModule{name: "database", optional: []string{"auth"}},
		fakeModule{name: "realtime", deps: []string{"database"}},
	)

	got, err := r.Resolve([]string{"realtime", "storage", "database", "auth"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "auth,database,realtime,storage"; names(got) != want {
		t.Fatalf("order = %s, want %s", names(got), want)
	}
}

func TestResolveIgnoresDisabledOptionalDependency(t *testing.T) {
	t.Parallel()

	r := newRegistry(t,
		fakeModule{name: "auth"},
		fakeModule{name: "database", optional: []string{"auth"}},
	)
	got, err := r.Resolve([]string{"database"})
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "database" {
		t.Fatalf("got %s", names(got))
	}
}

func TestResolveErrors(t *testing.T) {
	t.Parallel()

	r := newRegistry(t,
		fakeModule{name: "auth"},
		fakeModule{name: "storage", deps: []string{"auth"}},
		fakeModule{name: "a", deps: []string{"b"}},
		fakeModule{name: "b", deps: []string{"a"}},
	)

	tests := []struct {
		name    string
		enabled []string
		want    string
	}{
		{"unknown module", []string{"nope"}, `unknown module "nope"`},
		{"missing hard dependency is never auto-enabled", []string{"storage"}, `requires "auth"`},
		{"cycle", []string{"a", "b"}, "dependency cycle between modules: a, b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := r.Resolve(tt.enabled)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRegisterRejectsDuplicatesAndBadNames(t *testing.T) {
	t.Parallel()

	r := kernel.NewRegistry()
	if err := r.Register(fakeModule{name: "auth"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeModule{name: "auth"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, bad := range []string{"", "Auth", "1auth", "auth-x"} {
		if err := r.Register(fakeModule{name: bad}); err == nil {
			t.Fatalf("bad name %q accepted", bad)
		}
	}
}

func TestResolveEmptyIsKernelOnly(t *testing.T) {
	t.Parallel()

	got, err := newRegistry(t, fakeModule{name: "auth"}).Resolve(nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}
