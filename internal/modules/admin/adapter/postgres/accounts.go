// Package postgres implements the admin module's stores with pgx.
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

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
)

// Accounts implements port.AccountStore.
type Accounts struct{ db *pgxpool.Pool }

// NewAccounts returns an Accounts store.
func NewAccounts(db *pgxpool.Pool) *Accounts { return &Accounts{db: db} }

const accountCols = `id::text, email, created_at, last_sign_in_at`

func scanAccount(row pgx.Row, extra ...any) (domain.Account, error) {
	var a domain.Account
	if err := row.Scan(append([]any{&a.ID, &a.Email, &a.CreatedAt, &a.LastSignInAt}, extra...)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Account{}, domain.ErrAccountNotFound
		}
		return domain.Account{}, err
	}
	return a, nil
}

// Create implements port.AccountStore.
func (s *Accounts) Create(ctx context.Context, email, hash string) (domain.Account, error) {
	a, err := scanAccount(s.db.QueryRow(ctx,
		`INSERT INTO admin.accounts (email, password_hash) VALUES ($1, $2) RETURNING `+accountCols, email, hash))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Account{}, domain.ErrAccountExists
		}
		return domain.Account{}, fmt.Errorf("insert admin: %w", err)
	}
	return a, nil
}

// FindByEmail implements port.AccountStore.
func (s *Accounts) FindByEmail(ctx context.Context, email string) (domain.Account, string, error) {
	var hash string
	a, err := scanAccount(s.db.QueryRow(ctx,
		`SELECT `+accountCols+`, password_hash FROM admin.accounts WHERE email = $1`, email), &hash)
	return a, hash, err
}

// FindByID implements port.AccountStore.
func (s *Accounts) FindByID(ctx context.Context, id string) (domain.Account, string, error) {
	var hash string
	a, err := scanAccount(s.db.QueryRow(ctx,
		`SELECT `+accountCols+`, password_hash FROM admin.accounts WHERE id = $1::uuid`, id), &hash)
	return a, hash, err
}

// List implements port.AccountStore.
func (s *Accounts) List(ctx context.Context) ([]domain.Account, error) {
	rows, err := s.db.Query(ctx, `SELECT `+accountCols+` FROM admin.accounts ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("list admins: %w", err)
	}
	defer rows.Close()
	var out []domain.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan admin: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Count implements port.AccountStore.
func (s *Accounts) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM admin.accounts`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// Delete implements port.AccountStore.
func (s *Accounts) Delete(ctx context.Context, email string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin.accounts WHERE email = $1`, email)
	if err != nil {
		return fmt.Errorf("delete admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAccountNotFound
	}
	return nil
}

// SetPassword implements port.AccountStore.
func (s *Accounts) SetPassword(ctx context.Context, id, hash string) error {
	if _, err := s.db.Exec(ctx, `UPDATE admin.accounts SET password_hash = $2 WHERE id = $1::uuid`, id, hash); err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

// RecordSignIn implements port.AccountStore.
func (s *Accounts) RecordSignIn(ctx context.Context, id string, at time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE admin.accounts SET last_sign_in_at = $2 WHERE id = $1::uuid`, id, at); err != nil {
		return fmt.Errorf("record sign-in: %w", err)
	}
	return nil
}

// Sessions implements port.SessionStore.
type Sessions struct{ db *pgxpool.Pool }

// NewSessions returns a Sessions store.
func NewSessions(db *pgxpool.Pool) *Sessions { return &Sessions{db: db} }

// Create implements port.SessionStore.
func (s *Sessions) Create(ctx context.Context, tokenHash []byte, accountID string, exp time.Time, c domain.ClientInfo) error {
	if _, err := s.db.Exec(ctx,
		`INSERT INTO admin.sessions (token_hash, account_id, expires_at, ip, user_agent) VALUES ($1, $2::uuid, $3, nullif($4, ''), nullif($5, ''))`,
		tokenHash, accountID, exp, c.IP, truncate(c.UserAgent, 512)); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// Lookup implements port.SessionStore.
func (s *Sessions) Lookup(ctx context.Context, tokenHash []byte, now time.Time) (domain.Account, error) {
	a, err := scanAccount(s.db.QueryRow(ctx, `
		WITH s AS (
		    UPDATE admin.sessions SET last_seen_at = $2
		    WHERE token_hash = $1 AND expires_at > $2
		    RETURNING account_id
		)
		SELECT a.id::text, a.email, a.created_at, a.last_sign_in_at
		FROM admin.accounts a JOIN s ON s.account_id = a.id`, tokenHash, now))
	if errors.Is(err, domain.ErrAccountNotFound) {
		return domain.Account{}, domain.ErrNotSignedIn
	}
	if err != nil {
		return domain.Account{}, fmt.Errorf("lookup session: %w", err)
	}
	return a, nil
}

// Delete implements port.SessionStore.
func (s *Sessions) Delete(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.Exec(ctx, `DELETE FROM admin.sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteForAccount implements port.SessionStore.
func (s *Sessions) DeleteForAccount(ctx context.Context, accountID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM admin.sessions WHERE account_id = $1::uuid`, accountID)
	return err
}

// DeleteExpired implements port.SessionStore.
func (s *Sessions) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin.sessions WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
