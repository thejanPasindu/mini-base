package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/port"
)

type actorKey struct{}

// WithActor returns ctx carrying who performs the request; audit entries
// recorded under ctx are attributed to them.
func WithActor(ctx context.Context, a domain.Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor carried by ctx.
func ActorFrom(ctx context.Context) (domain.Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(domain.Actor)
	return a, ok
}

// Audit records dashboard actions. A nil *Audit records nothing.
type Audit struct {
	store port.AuditStore
	log   *slog.Logger
	now   func() time.Time
}

// NewAudit returns an Audit writing to store.
func NewAudit(store port.AuditStore, log *slog.Logger) *Audit {
	return &Audit{store: store, log: log, now: time.Now}
}

// Record appends an entry attributed to the actor in ctx. A failure to
// write is logged, not returned: the action itself has already happened.
func (a *Audit) Record(ctx context.Context, action, target string, detail map[string]any) {
	actor, ok := ActorFrom(ctx)
	if !ok {
		actor = domain.Actor{Email: "unknown"}
	}
	a.RecordAs(ctx, actor, action, target, detail)
}

// RecordAs appends an entry attributed to actor.
func (a *Audit) RecordAs(ctx context.Context, actor domain.Actor, action, target string, detail map[string]any) {
	if a == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	e := domain.AuditEntry{At: a.now(), Actor: actor.Email, IP: actor.IP, Action: action, Target: target, Detail: detail}
	if actor.AccountID != "" {
		id := actor.AccountID
		e.AccountID = &id
	}
	// The request may be cancelled right after the action; still record it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.store.Append(ctx, e); err != nil {
		a.log.ErrorContext(ctx, "write audit log", slog.String("action", action), slog.String("actor", actor.Email), slog.Any("error", err))
	}
}

// List returns audit entries, newest first.
func (a *Audit) List(ctx context.Context, q domain.AuditQuery) ([]domain.AuditEntry, error) {
	if a == nil {
		return []domain.AuditEntry{}, nil
	}
	out, err := a.store.List(ctx, q.Normalize())
	if err != nil {
		return nil, fmt.Errorf("list audit log: %w", err)
	}
	if out == nil {
		out = []domain.AuditEntry{}
	}
	return out, nil
}

// History returns the SQL editor runs of the actor in ctx.
func (a *Audit) History(ctx context.Context, limit int) ([]domain.HistoryEntry, error) {
	actor, ok := ActorFrom(ctx)
	if a == nil || !ok || actor.AccountID == "" {
		return []domain.HistoryEntry{}, nil
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	out, err := a.store.SQLHistory(ctx, actor.AccountID, limit)
	if err != nil {
		return nil, fmt.Errorf("sql history: %w", err)
	}
	if out == nil {
		out = []domain.HistoryEntry{}
	}
	return out, nil
}

// Purge deletes entries older than retention.
func (a *Audit) Purge(ctx context.Context, retention time.Duration) (int64, error) {
	if a == nil || retention <= 0 {
		return 0, nil
	}
	return a.store.DeleteBefore(ctx, a.now().Add(-retention))
}
