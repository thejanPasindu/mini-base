// Package postgres implements the auth repositories with pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/modules/auth/domain"
	"github.com/mini-base/mini-base/internal/modules/auth/port"
)

const uniqueViolation = "23505"

// Users implements port.UserRepository.
type Users struct{ db *pgxpool.Pool }

// NewUsers returns a Users repository.
func NewUsers(db *pgxpool.Pool) *Users { return &Users{db: db} }

const userColumns = `id::text, email, created_at, last_sign_in_at`

func scanUser(row pgx.Row, extra ...any) (domain.User, error) {
	var u domain.User
	dest := append([]any{&u.ID, &u.Email, &u.CreatedAt, &u.LastSignInAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, err
	}
	return u, nil
}

// Create implements port.UserRepository.
func (r *Users) Create(ctx context.Context, email, passwordHash string) (domain.User, error) {
	u, err := scanUser(r.db.QueryRow(ctx,
		`INSERT INTO auth.users (email, password_hash) VALUES ($1, $2) RETURNING `+userColumns,
		email, passwordHash))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return domain.User{}, domain.ErrEmailTaken
		}
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// FindByEmail implements port.UserRepository.
func (r *Users) FindByEmail(ctx context.Context, email string) (domain.User, string, error) {
	var hash string
	u, err := scanUser(r.db.QueryRow(ctx,
		`SELECT `+userColumns+`, password_hash FROM auth.users WHERE email = $1`, email), &hash)
	if err != nil {
		return domain.User{}, "", err
	}
	return u, hash, nil
}

// FindByID implements port.UserRepository.
func (r *Users) FindByID(ctx context.Context, id string) (domain.User, error) {
	return scanUser(r.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM auth.users WHERE id = $1::uuid`, id))
}

// RecordSignIn implements port.UserRepository.
func (r *Users) RecordSignIn(ctx context.Context, id string, at time.Time) error {
	if _, err := r.db.Exec(ctx,
		`UPDATE auth.users SET last_sign_in_at = $2, updated_at = $2 WHERE id = $1::uuid`, id, at); err != nil {
		return fmt.Errorf("record sign-in: %w", err)
	}
	return nil
}

// Sessions implements port.SessionRepository.
type Sessions struct{ db *pgxpool.Pool }

// NewSessions returns a Sessions repository.
func NewSessions(db *pgxpool.Pool) *Sessions { return &Sessions{db: db} }

// Create implements port.SessionRepository. Session and first refresh token
// are inserted in one statement, so they exist together or not at all.
func (r *Sessions) Create(ctx context.Context, userID string, refreshHash []byte, expiresAt time.Time, c domain.ClientInfo) (domain.Session, error) {
	s := domain.Session{UserID: userID}
	err := r.db.QueryRow(ctx, `
		WITH s AS (
		    INSERT INTO auth.sessions (user_id, expires_at, user_agent, ip)
		    VALUES ($1::uuid, $2, nullif($3, ''), nullif($4, ''))
		    RETURNING id, created_at, expires_at
		), t AS (
		    INSERT INTO auth.refresh_tokens (session_id, token_hash) SELECT id, $5 FROM s
		)
		SELECT id::text, created_at, expires_at FROM s`,
		userID, expiresAt, truncate(c.UserAgent, 512), c.IP, refreshHash,
	).Scan(&s.ID, &s.CreatedAt, &s.ExpiresAt)
	if err != nil {
		return domain.Session{}, fmt.Errorf("insert session: %w", err)
	}
	return s, nil
}

// Rotate implements port.SessionRepository. The token row is locked for the
// duration of the transaction, so two concurrent refreshes with the same
// token cannot both succeed: the second sees it as used.
func (r *Sessions) Rotate(ctx context.Context, oldHash, newHash []byte, now time.Time) (s domain.Session, outcome port.RotateOutcome, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Session{}, 0, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil || outcome != port.Rotated {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	var (
		tokenID int64
		used    bool
	)
	err = tx.QueryRow(ctx, `
		SELECT t.id, t.used_at IS NOT NULL,
		       s.id::text, s.user_id::text, s.created_at, s.expires_at, s.revoked_at
		FROM auth.refresh_tokens t
		JOIN auth.sessions s ON s.id = t.session_id
		WHERE t.token_hash = $1
		FOR UPDATE OF t`, oldHash,
	).Scan(&tokenID, &used, &s.ID, &s.UserID, &s.CreatedAt, &s.ExpiresAt, &s.RevokedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Session{}, port.NotFound, nil
	case err != nil:
		return domain.Session{}, 0, fmt.Errorf("find refresh token: %w", err)
	case used:
		return s, port.Reused, nil
	case !s.Active(now):
		return s, port.Inactive, nil
	}

	if _, err = tx.Exec(ctx, `UPDATE auth.refresh_tokens SET used_at = $2 WHERE id = $1`, tokenID, now); err != nil {
		return domain.Session{}, 0, fmt.Errorf("mark token used: %w", err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO auth.refresh_tokens (session_id, token_hash, parent_id) VALUES ($1::uuid, $2, $3)`,
		s.ID, newHash, tokenID); err != nil {
		return domain.Session{}, 0, fmt.Errorf("insert refresh token: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.sessions SET refreshed_at = $2 WHERE id = $1::uuid`, s.ID, now); err != nil {
		return domain.Session{}, 0, fmt.Errorf("touch session: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Session{}, 0, fmt.Errorf("commit rotation: %w", err)
	}
	return s, port.Rotated, nil
}

// Revoke implements port.SessionRepository.
func (r *Sessions) Revoke(ctx context.Context, sessionID string, at time.Time) error {
	if _, err := r.db.Exec(ctx,
		`UPDATE auth.sessions SET revoked_at = $2 WHERE id = $1::uuid AND revoked_at IS NULL`,
		sessionID, at); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// DeleteStale implements port.SessionRepository.
func (r *Sessions) DeleteStale(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM auth.sessions WHERE expires_at < $1 OR revoked_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete stale sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// truncate shortens s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
