package kernel

import (
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Setting is one configuration variable as the server sees it.
type Setting struct {
	Name    string `json:"name"`    // full variable name, e.g. MINIBASE_AUTH_SESSION_TTL
	Value   string `json:"value"`   // effective value; masked or empty for secrets
	Default string `json:"default"` // value used when unset
	IsSet   bool   `json:"is_set"`  // set in the environment (otherwise the default applies)
	Secret  bool   `json:"secret"`
}

// Settings records every variable the kernel and modules read, so the admin
// dashboard can show the configuration in effect without each module
// reporting it. It is safe for concurrent use; a nil *Settings records
// nothing.
type Settings struct {
	mu  sync.RWMutex
	all map[string]Setting
	raw map[string]string // unmasked values of non-secret settings
}

// NewSettings returns an empty recorder.
func NewSettings() *Settings {
	return &Settings{all: map[string]Setting{}, raw: map[string]string{}}
}

func (s *Settings) record(name, value, def string) {
	if s == nil {
		return
	}
	set := value != ""
	effective := def
	if set {
		effective = value
	}
	st := Setting{Name: name, Default: def, IsSet: set}
	st.Value, st.Secret = mask(name, effective)
	if st.Secret {
		st.Default, _ = mask(name, def)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.all[name] = st
	if !st.Secret {
		s.raw[name] = effective
	}
}

// List returns all recorded settings sorted by name.
func (s *Settings) List() []Setting {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Setting, 0, len(s.all))
	for _, st := range s.all {
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b Setting) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Raw returns the effective value of a non-secret setting. Secrets are
// never returned unmasked.
func (s *Settings) Raw(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.raw[name]
	return v, ok
}

// mask hides secret values. Rules, by variable name:
//   - *_URL: the password inside the URL is replaced (url.Redacted);
//   - names containing SECRET or PASSWORD: hidden entirely;
//   - *_KEY other than ANON_KEY (which is public by design): the first and
//     last four characters remain so an operator can tell keys apart.
func mask(name, value string) (string, bool) {
	switch {
	case strings.HasSuffix(name, "_URL"):
		u, err := url.Parse(value)
		if err != nil {
			return "", true
		}
		return u.Redacted(), true
	case strings.Contains(name, "SECRET"), strings.Contains(name, "PASSWORD"):
		return "", true
	case strings.HasSuffix(name, "_KEY") && !strings.HasSuffix(name, "ANON_KEY"):
		if len(value) < 16 {
			return "", true
		}
		return value[:4] + "…" + value[len(value)-4:], true
	}
	return value, false
}
