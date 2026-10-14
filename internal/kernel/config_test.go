package kernel_test

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/kernel"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := kernel.LoadConfig(envFrom(map[string]string{
		"MINIBASE_DATABASE_URL": "postgres://localhost/db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Env != kernel.EnvDevelopment || cfg.HTTP.Addr != ":8090" || cfg.Log.Format != "text" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !slices.Equal(cfg.HTTP.CORSOrigins, []string{"*"}) {
		t.Fatalf("dev CORS default = %v", cfg.HTTP.CORSOrigins)
	}
	if len(cfg.Modules) != 0 {
		t.Fatalf("modules = %v, want none", cfg.Modules)
	}
}

func TestLoadConfigProductionIsStricterByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := kernel.LoadConfig(envFrom(map[string]string{
		"MINIBASE_ENV":          "production",
		"MINIBASE_DATABASE_URL": "postgres://localhost/db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HTTP.CORSOrigins) != 0 || cfg.Log.Format != "json" {
		t.Fatalf("unexpected production defaults: %+v", cfg)
	}
}

func TestLoadConfigParsesValues(t *testing.T) {
	t.Parallel()

	cfg, err := kernel.LoadConfig(envFrom(map[string]string{
		"MINIBASE_DATABASE_URL":          "postgres://localhost/db",
		"MINIBASE_MODULES":               " auth, database ,,storage ",
		"MINIBASE_HTTP_SHUTDOWN_TIMEOUT": "3s",
		"MINIBASE_LOG_LEVEL":             "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Modules, []string{"auth", "database", "storage"}) {
		t.Fatalf("modules = %v", cfg.Modules)
	}
	if cfg.HTTP.ShutdownTimeout != 3*time.Second || cfg.Log.Level != slog.LevelDebug {
		t.Fatalf("unexpected: %+v", cfg)
	}
}

func TestLoadConfigReportsAllProblems(t *testing.T) {
	t.Parallel()

	_, err := kernel.LoadConfig(envFrom(map[string]string{
		"MINIBASE_ENV":                   "staging",
		"MINIBASE_HTTP_SHUTDOWN_TIMEOUT": "soon",
		"MINIBASE_MODULES":               "auth,auth",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"MINIBASE_ENV", "HTTP_SHUTDOWN_TIMEOUT", "DATABASE_URL is required", `"auth" twice`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func TestModuleConfigIsScopedAndCollectsErrors(t *testing.T) {
	t.Parallel()

	c := kernel.NewModuleConfig("storage", envFrom(map[string]string{
		"MINIBASE_STORAGE_DRIVER":   "s3",
		"MINIBASE_STORAGE_MAX_SIZE": "big",
		"MINIBASE_DRIVER":           "ignored",
	}))
	if got := c.String("DRIVER", "local"); got != "s3" {
		t.Fatalf("driver = %q", got)
	}
	if got := c.Int("MAX_SIZE", 5); got != 5 {
		t.Fatalf("invalid int should fall back to default, got %d", got)
	}
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "MINIBASE_STORAGE_MAX_SIZE") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadConfigValidatesKeys(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 32)
	tests := map[string]struct {
		anon, service, want string
	}{
		"unset is allowed (modules decide)": {"", "", ""},
		"too short":                         {"short", long, "ANON_KEY must be at least 32"},
		"identical":                         {long, long, "must differ"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := kernel.LoadConfig(envFrom(map[string]string{
				"MINIBASE_DATABASE_URL": "postgres://localhost/db",
				"MINIBASE_ANON_KEY":     tt.anon,
				"MINIBASE_SERVICE_KEY":  tt.service,
			}))
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
