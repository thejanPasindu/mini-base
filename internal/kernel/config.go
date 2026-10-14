package kernel

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix prefixes every environment variable mini-base reads.
const EnvPrefix = "MINIBASE_"

// Environment selects safety defaults.
type Environment string

// Supported environments.
const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

// Config is the kernel configuration. Module settings are read separately
// through ModuleConfig so the kernel never needs to know about them.
type Config struct {
	Env      Environment
	HTTP     HTTPConfig
	Database DatabaseConfig
	Log      LogConfig
	Modules  []string
}

// HTTPConfig configures the HTTP server.
type HTTPConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxBodyBytes      int64
	CORSOrigins       []string
}

// DatabaseConfig configures the PostgreSQL connection pool.
type DatabaseConfig struct {
	URL            string
	MaxConns       int32
	ConnectTimeout time.Duration // total time to keep retrying at startup
}

// LogConfig configures logging.
type LogConfig struct {
	Level  slog.Level
	Format string // "json" or "text"
}

// LoadConfig reads the kernel configuration using getenv (os.Getenv in
// production, a map in tests). All problems are reported together.
func LoadConfig(getenv func(string) string) (Config, error) {
	e := envReader{getenv: getenv, prefix: EnvPrefix}

	env := Environment(e.str("ENV", string(EnvDevelopment)))
	defaultCORS, defaultLogFormat := "*", "text"
	if env == EnvProduction {
		defaultCORS, defaultLogFormat = "", "json"
	}

	cfg := Config{
		Env: env,
		HTTP: HTTPConfig{
			Addr:              e.str("HTTP_ADDR", ":8090"),
			ReadHeaderTimeout: e.duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       e.duration("HTTP_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:      e.duration("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:       e.duration("HTTP_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout:   e.duration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
			MaxBodyBytes:      int64(e.int("HTTP_MAX_BODY_BYTES", 10<<20)),
			CORSOrigins:       e.list("CORS_ALLOWED_ORIGINS", defaultCORS),
		},
		Database: DatabaseConfig{
			URL:            e.str("DATABASE_URL", ""),
			MaxConns:       int32(e.int("DATABASE_MAX_CONNS", 10)), //nolint:gosec // range-checked in Validate
			ConnectTimeout: e.duration("DATABASE_CONNECT_TIMEOUT", 30*time.Second),
		},
		Log: LogConfig{
			Level:  e.level("LOG_LEVEL", slog.LevelInfo),
			Format: e.str("LOG_FORMAT", defaultLogFormat),
		},
		Modules: e.list("MODULES", ""),
	}

	if err := errors.Join(e.err(), cfg.Validate()); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// Validate checks semantic constraints.
func (c Config) Validate() error {
	var errs []error
	if c.Env != EnvDevelopment && c.Env != EnvProduction {
		errs = append(errs, fmt.Errorf("%sENV must be %q or %q, got %q", EnvPrefix, EnvDevelopment, EnvProduction, c.Env))
	}
	if c.Database.URL == "" {
		errs = append(errs, fmt.Errorf("%sDATABASE_URL is required", EnvPrefix))
	}
	if c.Database.MaxConns < 1 || c.Database.MaxConns > 1000 {
		errs = append(errs, fmt.Errorf("%sDATABASE_MAX_CONNS must be between 1 and 1000", EnvPrefix))
	}
	if c.HTTP.MaxBodyBytes < 1 {
		errs = append(errs, fmt.Errorf("%sHTTP_MAX_BODY_BYTES must be positive", EnvPrefix))
	}
	for name, d := range map[string]time.Duration{
		"HTTP_READ_HEADER_TIMEOUT": c.HTTP.ReadHeaderTimeout,
		"HTTP_SHUTDOWN_TIMEOUT":    c.HTTP.ShutdownTimeout,
		"DATABASE_CONNECT_TIMEOUT": c.Database.ConnectTimeout,
	} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s%s must be positive", EnvPrefix, name))
		}
	}
	if c.Log.Format != "json" && c.Log.Format != "text" {
		errs = append(errs, fmt.Errorf("%sLOG_FORMAT must be \"json\" or \"text\"", EnvPrefix))
	}
	seen := make(map[string]bool, len(c.Modules))
	for _, m := range c.Modules {
		if seen[m] {
			errs = append(errs, fmt.Errorf("%sMODULES lists %q twice", EnvPrefix, m))
		}
		seen[m] = true
	}
	return errors.Join(errs...)
}

// ModuleConfig reads one module's settings from MINIBASE_<MODULE>_<KEY>.
// Parse errors are collected; call Err after reading all keys.
type ModuleConfig struct {
	r envReader
}

// NewModuleConfig returns the config reader for module name.
func NewModuleConfig(name string, getenv func(string) string) *ModuleConfig {
	return &ModuleConfig{r: envReader{getenv: getenv, prefix: EnvPrefix + strings.ToUpper(name) + "_"}}
}

// String returns the value of key or def.
func (c *ModuleConfig) String(key, def string) string { return c.r.str(key, def) }

// Int returns the integer value of key or def.
func (c *ModuleConfig) Int(key string, def int) int { return c.r.int(key, def) }

// Bool returns the boolean value of key or def.
func (c *ModuleConfig) Bool(key string, def bool) bool { return c.r.bool(key, def) }

// Duration returns the duration value of key (e.g. "15m") or def.
func (c *ModuleConfig) Duration(key string, def time.Duration) time.Duration {
	return c.r.duration(key, def)
}

// List returns the comma-separated value of key or def.
func (c *ModuleConfig) List(key, def string) []string { return c.r.list(key, def) }

// Err returns all parse errors encountered so far.
func (c *ModuleConfig) Err() error { return c.r.err() }

// envReader reads prefixed variables and accumulates parse errors.
type envReader struct {
	getenv func(string) string
	prefix string
	errs   []error
}

func (e *envReader) raw(key string) (string, bool) {
	v := strings.TrimSpace(e.getenv(e.prefix + key))
	return v, v != ""
}

func (e *envReader) str(key, def string) string {
	if v, ok := e.raw(key); ok {
		return v
	}
	return def
}

func (e *envReader) int(key string, def int) int {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s%s: %q is not an integer", e.prefix, key, v))
		return def
	}
	return n
}

func (e *envReader) bool(key string, def bool) bool {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s%s: %q is not a boolean", e.prefix, key, v))
		return def
	}
	return b
}

func (e *envReader) duration(key string, def time.Duration) time.Duration {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s%s: %q is not a duration (e.g. 30s, 5m)", e.prefix, key, v))
		return def
	}
	return d
}

func (e *envReader) level(key string, def slog.Level) slog.Level {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s%s: %q is not a log level (debug, info, warn, error)", e.prefix, key, v))
		return def
	}
	return l
}

func (e *envReader) list(key, def string) []string {
	v := e.str(key, def)
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (e *envReader) err() error { return errors.Join(e.errs...) }
