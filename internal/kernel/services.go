package kernel

import (
	"fmt"
	"sync"
)

// Services is a typed lookup of ports that modules publish for each other,
// e.g. auth provides a TokenVerifier that storage uses. Only interfaces
// declared in pkg/ports should be provided, never concrete module types.
type Services struct {
	mu sync.RWMutex
	m  map[string]any
}

// NewServices returns an empty Services.
func NewServices() *Services {
	return &Services{m: make(map[string]any)}
}

// Provide publishes svc under name. Names are conventionally "<module>.<port>".
func (s *Services) Provide(name string, svc any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.m[name]; dup {
		return fmt.Errorf("service %q already provided", name)
	}
	s.m[name] = svc
	return nil
}

// Lookup returns the service published under name as T. ok is false when the
// service is absent, which is normal for optional dependencies.
func Lookup[T any](s *Services, name string) (svc T, ok bool, err error) {
	s.mu.RLock()
	v, found := s.m[name]
	s.mu.RUnlock()
	if !found {
		return svc, false, nil
	}
	svc, ok = v.(T)
	if !ok {
		return svc, false, fmt.Errorf("service %q has type %T, not the requested type", name, v)
	}
	return svc, true, nil
}
