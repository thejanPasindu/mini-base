package auth

import (
	"context"

	"github.com/mini-base/mini-base/internal/modules/auth/domain"
	"github.com/mini-base/mini-base/internal/modules/auth/service"
	"github.com/mini-base/mini-base/pkg/ports"
)

// directory adapts the auth service to ports.UserDirectory for operator
// tools such as the admin dashboard.
type directory struct{ svc *service.Service }

var _ ports.UserDirectory = directory{}

func (d directory) ListUsers(ctx context.Context, q ports.UserQuery) ([]ports.UserSummary, int, error) {
	listing, total, err := d.svc.ListUsers(ctx, q.Search, q.Limit, q.Offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]ports.UserSummary, len(listing))
	for i, l := range listing {
		out[i] = summary(l.User)
		out[i].ActiveSessions = l.ActiveSessions
	}
	return out, total, nil
}

func (d directory) CreateUser(ctx context.Context, email, password string) (ports.UserSummary, error) {
	u, err := d.svc.CreateUser(ctx, email, password)
	if err != nil {
		return ports.UserSummary{}, err
	}
	return summary(u), nil
}

func (d directory) DeleteUser(ctx context.Context, id string) error { return d.svc.DeleteUser(ctx, id) }

func (d directory) RevokeSessions(ctx context.Context, id string) (int, error) {
	return d.svc.RevokeUserSessions(ctx, id)
}

func summary(u domain.User) ports.UserSummary {
	return ports.UserSummary{ID: u.ID, Email: u.Email, CreatedAt: u.CreatedAt.UTC(), LastSignInAt: u.LastSignInAt}
}
