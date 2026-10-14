// Package port declares what the auth service needs from the outside world.
package port

import (
	"context"
	"time"

	"github.com/mini-base/mini-base/internal/modules/auth/domain"
)

// UserRepository stores accounts.
type UserRepository interface {
	// Create returns domain.ErrEmailTaken if the email exists.
	Create(ctx context.Context, email, passwordHash string) (domain.User, error)
	// FindByEmail returns domain.ErrUserNotFound if absent.
	FindByEmail(ctx context.Context, email string) (user domain.User, passwordHash string, err error)
	// FindByID returns domain.ErrUserNotFound if absent.
	FindByID(ctx context.Context, id string) (domain.User, error)
	RecordSignIn(ctx context.Context, id string, at time.Time) error
}

// RotateOutcome is the result of presenting a refresh token.
type RotateOutcome int

// Outcomes.
const (
	// Rotated: the token was valid; it is now used and the new one stored.
	Rotated RotateOutcome = iota + 1
	// NotFound: no such token.
	NotFound
	// Reused: the token had already been used — a sign of theft.
	Reused
	// Inactive: the session is revoked or expired.
	Inactive
)

// SessionRepository stores sessions and their refresh tokens.
// Refresh tokens are only ever stored as hashes.
type SessionRepository interface {
	Create(ctx context.Context, userID string, refreshHash []byte, expiresAt time.Time, client domain.ClientInfo) (domain.Session, error)

	// Rotate atomically checks the token with oldHash and, only if it is
	// unused and its session is active, marks it used and stores newHash in
	// the same session. The session is returned for every outcome except
	// NotFound.
	Rotate(ctx context.Context, oldHash, newHash []byte, now time.Time) (domain.Session, RotateOutcome, error)

	Revoke(ctx context.Context, sessionID string, at time.Time) error

	// DeleteStale removes sessions expired or revoked before cutoff.
	DeleteStale(ctx context.Context, cutoff time.Time) (int64, error)
}

// PasswordHasher hashes and checks passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (bool, error)
}

// TokenIssuer creates access tokens.
type TokenIssuer interface {
	Issue(user domain.User, sessionID string, now time.Time) (token string, expiresAt time.Time, err error)
}

// EventPublisher is the subset of the event bus the service uses.
type EventPublisher interface {
	Publish(ctx context.Context, name string, payload any)
}
