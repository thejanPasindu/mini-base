// Package service implements the REST-over-Postgres use cases.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mini-base/mini-base/internal/modules/database/domain"
	"github.com/mini-base/mini-base/internal/modules/database/port"
	"github.com/mini-base/mini-base/internal/platform/errs"
	"github.com/mini-base/mini-base/pkg/ports"
)

// Options configure the service.
type Options struct {
	Schemas    []string // exposed schemas, in lookup order
	MaxRows    int      // hard cap on rows returned by a read
	AllowNoRLS bool     // let anon/authenticated reach tables without RLS
}

// reloadEvery throttles schema reloads triggered by unknown names, so a
// client probing random table names cannot hammer the catalog.
const reloadEvery = time.Second

// Errors.
var ErrTableNotFound = errs.NotFound("database.table_not_found", "Table not found")

// Service implements the use cases.
type Service struct {
	loader port.SchemaLoader
	exec   port.Executor
	opts   Options

	schema   atomic.Pointer[domain.Schema]
	reloadMu sync.Mutex
	reloaded time.Time
	now      func() time.Time
}

// New returns a Service. Call Reload before serving.
func New(loader port.SchemaLoader, exec port.Executor, opts Options) *Service {
	return &Service{loader: loader, exec: exec, opts: opts, now: time.Now}
}

// Reload refreshes the schema snapshot from the catalog.
func (s *Service) Reload(ctx context.Context) error {
	sc, err := s.loader.Load(ctx, s.opts.Schemas)
	if err != nil {
		return fmt.Errorf("load schema: %w", err)
	}
	s.schema.Store(sc)
	return nil
}

// Invalidate forces the next request to see a fresh catalog. Call it when
// the schema is known to have changed (ports.EventSchemaChanged).
func (s *Service) Invalidate(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	s.reloaded = s.now()
	return s.Reload(ctx)
}

// Ready implements a readiness probe: the schema must have loaded.
func (s *Service) Ready(context.Context) error {
	if s.schema.Load() == nil {
		return errors.New("schema not loaded")
	}
	return nil
}

// Request is one REST call.
type Request struct {
	Principal ports.Principal
	Table     string
	Query     url.Values
	Body      []byte
	Return    bool // Prefer: return=representation
}

// Response is the result. Body is a JSON array when the call returns rows.
type Response struct {
	Body     []byte
	Affected int64
}

// Read handles GET.
func (s *Service) Read(ctx context.Context, r Request) (Response, error) {
	q, err := domain.ParseRead(r.Query)
	if err != nil {
		return Response{}, err
	}
	return s.run(ctx, r, func(t *domain.Table) (domain.Statement, error) {
		return domain.BuildSelect(t, q, s.opts.MaxRows)
	})
}

// Create handles POST.
func (s *Service) Create(ctx context.Context, r Request) (Response, error) {
	q, err := domain.ParseWrite(r.Query, false)
	if err != nil {
		return Response{}, err
	}
	cols, rows, err := domain.ParseRows(r.Body)
	if err != nil {
		return Response{}, err
	}
	var ret *domain.WriteQuery
	if r.Return {
		ret = &q
	}
	return s.run(ctx, r, func(t *domain.Table) (domain.Statement, error) {
		return domain.BuildInsert(t, cols, rows, ret)
	})
}

// Update handles PATCH.
func (s *Service) Update(ctx context.Context, r Request) (Response, error) {
	q, err := domain.ParseWrite(r.Query, true)
	if err != nil {
		return Response{}, err
	}
	cols, obj, err := domain.ParseObject(r.Body)
	if err != nil {
		return Response{}, err
	}
	return s.run(ctx, r, func(t *domain.Table) (domain.Statement, error) {
		return domain.BuildUpdate(t, cols, obj, q, r.Return)
	})
}

// Delete handles DELETE.
func (s *Service) Delete(ctx context.Context, r Request) (Response, error) {
	q, err := domain.ParseWrite(r.Query, true)
	if err != nil {
		return Response{}, err
	}
	return s.run(ctx, r, func(t *domain.Table) (domain.Statement, error) {
		return domain.BuildDelete(t, q, r.Return)
	})
}

// run resolves the table, checks it may be exposed to the caller, builds and
// executes the statement. Unknown table/column names trigger one schema
// reload, so tables created after startup work without a restart.
func (s *Service) run(ctx context.Context, r Request, build func(*domain.Table) (domain.Statement, error)) (Response, error) {
	st, t, err := s.prepare(r.Table, build)
	if isStale(err) && s.maybeReload(ctx) {
		st, t, err = s.prepare(r.Table, build)
	}
	if err != nil {
		return Response{}, err
	}
	if err := s.authorize(r.Principal, t); err != nil {
		// RLS may have been enabled since the snapshot was taken (e.g. in
		// psql). Re-check once against a fresh catalog before refusing.
		if !s.maybeReload(ctx) {
			return Response{}, err
		}
		if st, t, err = s.prepare(r.Table, build); err != nil {
			return Response{}, err
		}
		if err := s.authorize(r.Principal, t); err != nil {
			return Response{}, err
		}
	}

	if st.Returns {
		body, err := s.exec.Query(ctx, r.Principal, st)
		if err != nil {
			return Response{}, err
		}
		return Response{Body: body}, nil
	}
	n, err := s.exec.Exec(ctx, r.Principal, st)
	if err != nil {
		return Response{}, err
	}
	return Response{Affected: n}, nil
}

func (s *Service) prepare(name string, build func(*domain.Table) (domain.Statement, error)) (domain.Statement, *domain.Table, error) {
	sc := s.schema.Load()
	if sc == nil {
		return domain.Statement{}, nil, errs.Unavailable("database.schema_not_loaded", "Database schema is not loaded yet")
	}
	t, ok := sc.Table(name)
	if !ok {
		return domain.Statement{}, nil, errs.NotFound(ErrTableNotFound.Code, fmt.Sprintf("Table %q not found in exposed schemas (%s)", name, strings.Join(s.opts.Schemas, ", ")))
	}
	st, err := build(t)
	return st, t, err
}

func isStale(err error) bool {
	return errors.Is(err, ErrTableNotFound) || errors.Is(err, domain.ErrUnknownColumn)
}

// maybeReload reloads the schema unless that happened within reloadEvery.
func (s *Service) maybeReload(ctx context.Context) bool {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if s.now().Sub(s.reloaded) < reloadEvery {
		return false
	}
	s.reloaded = s.now()
	return s.Reload(ctx) == nil
}

// Exposure errors.
var (
	ErrRLSDisabled  = errs.Forbidden("database.rls_disabled", "Row level security is not enabled on this table")
	ErrUnsafeView   = errs.Forbidden("database.view_not_security_invoker", "This view runs with its owner's privileges")
	ErrNotExposable = errs.Forbidden("database.not_exposable", "Materialized views and foreign tables cannot be protected by row level security")
)

// Guidance appended to exposure errors so developers know the fix.
const (
	noRLSGuidance  = "Run: ALTER TABLE %s ENABLE ROW LEVEL SECURITY; and add policies. Or set MINIBASE_DATABASE_ALLOW_TABLES_WITHOUT_RLS=true to allow it (unsafe)."
	viewGuidance   = "Recreate it with: CREATE VIEW %s WITH (security_invoker = true) AS ...; so the caller's RLS applies."
	exposeGuidance = "Use the service key, or set MINIBASE_DATABASE_ALLOW_TABLES_WITHOUT_RLS=true (unsafe)."
)

// authorize is the secure-by-default guard: anon and authenticated callers
// may only use relations whose access is governed by RLS. The service role
// is trusted and bypasses RLS anyway.
func (s *Service) authorize(p ports.Principal, t *domain.Table) error {
	if p.Role == ports.RoleService || s.opts.AllowNoRLS {
		return nil
	}
	switch t.Kind {
	case domain.KindTable:
		if !t.RLSEnabled {
			return errs.Forbidden(ErrRLSDisabled.Code, ErrRLSDisabled.Message+". "+fmt.Sprintf(noRLSGuidance, t))
		}
	case domain.KindView:
		if !t.SecurityInvoker {
			return errs.Forbidden(ErrUnsafeView.Code, ErrUnsafeView.Message+". "+fmt.Sprintf(viewGuidance, t))
		}
	default:
		return errs.Forbidden(ErrNotExposable.Code, ErrNotExposable.Message+". "+exposeGuidance)
	}
	return nil
}
