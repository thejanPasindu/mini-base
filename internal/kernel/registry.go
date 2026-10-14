package kernel

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var validModuleName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Registry holds every module compiled into the binary and resolves which of
// them run, and in what order.
type Registry struct {
	modules map[string]Module
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{modules: make(map[string]Module)}
}

// Register adds a module. Names must be unique and match ^[a-z][a-z0-9_]*$.
func (r *Registry) Register(m Module) error {
	name := m.Name()
	if !validModuleName.MatchString(name) {
		return fmt.Errorf("register module: invalid name %q", name)
	}
	if _, dup := r.modules[name]; dup {
		return fmt.Errorf("register module: %q already registered", name)
	}
	r.modules[name] = m
	return nil
}

// MustRegister is Register for the composition root, where a failure is a
// programming error.
func (r *Registry) MustRegister(mods ...Module) {
	for _, m := range mods {
		if err := r.Register(m); err != nil {
			panic(err)
		}
	}
}

// Names returns all registered module names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.modules))
	for n := range r.modules {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Lookup returns a registered module by name.
func (r *Registry) Lookup(name string) (Module, bool) {
	m, ok := r.modules[name]
	return m, ok
}

// Resolve validates the enabled set and returns its modules in dependency
// order (dependencies first). The order is deterministic: ties are broken by
// name. Missing modules, missing dependencies and cycles are errors — the
// registry never enables a module implicitly.
func (r *Registry) Resolve(enabled []string) ([]Module, error) {
	set := make(map[string]bool, len(enabled))
	var problems []error
	for _, name := range enabled {
		if _, ok := r.modules[name]; !ok {
			problems = append(problems, fmt.Errorf("unknown module %q (available: %s)", name, strings.Join(r.Names(), ", ")))
			continue
		}
		set[name] = true
	}

	// edges[a] = modules that must come before a.
	edges := make(map[string][]string, len(set))
	for name := range set {
		m := r.modules[name]
		for _, dep := range m.DependsOn() {
			if !set[dep] {
				problems = append(problems, fmt.Errorf("module %q requires %q; add it to MINIBASE_MODULES", name, dep))
				continue
			}
			edges[name] = append(edges[name], dep)
		}
		if od, ok := m.(OptionalDependent); ok {
			for _, dep := range od.OptionalDependsOn() {
				if set[dep] {
					edges[name] = append(edges[name], dep)
				}
			}
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	return r.topoSort(set, edges)
}

// topoSort is Kahn's algorithm with a name-sorted ready list for stable output.
func (r *Registry) topoSort(set map[string]bool, edges map[string][]string) ([]Module, error) {
	pending := make(map[string]int, len(set))         // unmet dependency count
	dependents := make(map[string][]string, len(set)) // dep -> modules waiting on it
	for name := range set {
		pending[name] = len(edges[name])
		for _, dep := range edges[name] {
			dependents[dep] = append(dependents[dep], name)
		}
	}

	var ready []string
	for name, n := range pending {
		if n == 0 {
			ready = append(ready, name)
		}
	}

	ordered := make([]Module, 0, len(set))
	for len(ready) > 0 {
		slices.Sort(ready)
		name := ready[0]
		ready = ready[1:]
		ordered = append(ordered, r.modules[name])
		for _, d := range dependents[name] {
			pending[d]--
			if pending[d] == 0 {
				ready = append(ready, d)
			}
		}
	}

	if len(ordered) != len(set) {
		var cyclic []string
		for name, n := range pending {
			if n > 0 {
				cyclic = append(cyclic, name)
			}
		}
		slices.Sort(cyclic)
		return nil, fmt.Errorf("dependency cycle between modules: %s", strings.Join(cyclic, ", "))
	}
	return ordered, nil
}
