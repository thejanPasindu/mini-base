// Package service implements the admin use cases.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/port"
)

// Accounts implements operator sign-in and account management.
type Accounts struct {
	accounts   port.AccountStore
	sessions   port.SessionStore
	hasher     port.PasswordHasher
	sessionTTL time.Duration
	now        func() time.Time
	dummyHash  string
}

// NewAccounts returns an Accounts service.
func NewAccounts(accounts port.AccountStore, sessions port.SessionStore, hasher port.PasswordHasher, sessionTTL time.Duration) (*Accounts, error) {
	dummy, err := hasher.Hash(context.Background(), "dummy-password-for-timing-equalisation")
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	return &Accounts{accounts: accounts, sessions: sessions, hasher: hasher, sessionTTL: sessionTTL, now: time.Now, dummyHash: dummy}, nil
}

// WithClock replaces the time source (tests).
func (a *Accounts) WithClock(now func() time.Time) *Accounts { a.now = now; return a }

// Session is a signed-in dashboard session. Token goes into a cookie.
type Session struct {
	Token     string
	ExpiresAt time.Time
	Account   domain.Account
}

// SignIn checks credentials and opens a session.
func (a *Accounts) SignIn(ctx context.Context, email, password string, c domain.ClientInfo) (Session, error) {
	email, err := domain.NormalizeEmail(email)
	if err != nil || len(password) > domain.MaxPasswordLength {
		return Session{}, domain.ErrInvalidCredentials
	}
	acct, hash, err := a.accounts.FindByEmail(ctx, email)
	switch {
	case errors.Is(err, domain.ErrAccountNotFound):
		_, _ = a.hasher.Verify(ctx, password, a.dummyHash) // equal timing
		return Session{}, domain.ErrInvalidCredentials
	case err != nil:
		return Session{}, fmt.Errorf("find admin: %w", err)
	}
	ok, err := a.hasher.Verify(ctx, password, hash)
	if err != nil {
		return Session{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return Session{}, domain.ErrInvalidCredentials
	}

	token, tokenHash, err := newToken()
	if err != nil {
		return Session{}, err
	}
	now := a.now()
	exp := now.Add(a.sessionTTL)
	if err := a.sessions.Create(ctx, tokenHash, acct.ID, exp, c); err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	if err := a.accounts.RecordSignIn(ctx, acct.ID, now); err != nil {
		return Session{}, fmt.Errorf("record sign-in: %w", err)
	}
	acct.LastSignInAt = &now
	return Session{Token: token, ExpiresAt: exp, Account: acct}, nil
}

// Authenticate resolves a session token to its account.
func (a *Accounts) Authenticate(ctx context.Context, token string) (domain.Account, error) {
	if token == "" {
		return domain.Account{}, domain.ErrNotSignedIn
	}
	acct, err := a.sessions.Lookup(ctx, hashToken(token), a.now())
	if err != nil {
		return domain.Account{}, err
	}
	return acct, nil
}

// SignOut ends a session.
func (a *Accounts) SignOut(ctx context.Context, token string) error {
	if err := a.sessions.Delete(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ErrWrongPassword is returned when the current password does not match.
var ErrWrongPassword = domain.ErrInvalidCredentials

// ChangePassword sets a new password after checking the current one, and
// signs the account out of every other session.
func (a *Accounts) ChangePassword(ctx context.Context, accountID, current, next string) error {
	_, hash, err := a.accounts.FindByID(ctx, accountID)
	if err != nil {
		return fmt.Errorf("find admin: %w", err)
	}
	ok, err := a.hasher.Verify(ctx, current, hash)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return ErrWrongPassword
	}
	return a.setPassword(ctx, accountID, next)
}

// Create adds an operator (CLI and first-run bootstrap).
func (a *Accounts) Create(ctx context.Context, email, password string) (domain.Account, error) {
	email, err := domain.NormalizeEmail(email)
	if err != nil {
		return domain.Account{}, err
	}
	if err := domain.ValidatePassword(password); err != nil {
		return domain.Account{}, err
	}
	hash, err := a.hasher.Hash(ctx, password)
	if err != nil {
		return domain.Account{}, fmt.Errorf("hash password: %w", err)
	}
	acct, err := a.accounts.Create(ctx, email, hash)
	if err != nil {
		return domain.Account{}, fmt.Errorf("create admin: %w", err)
	}
	return acct, nil
}

// ResetPassword sets a password without the current one (CLI only) and
// ends all of the account's sessions.
func (a *Accounts) ResetPassword(ctx context.Context, email, password string) error {
	email, err := domain.NormalizeEmail(email)
	if err != nil {
		return err
	}
	acct, _, err := a.accounts.FindByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("find admin: %w", err)
	}
	return a.setPassword(ctx, acct.ID, password)
}

func (a *Accounts) setPassword(ctx context.Context, accountID, password string) error {
	if err := domain.ValidatePassword(password); err != nil {
		return err
	}
	hash, err := a.hasher.Hash(ctx, password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := a.accounts.SetPassword(ctx, accountID, hash); err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if err := a.sessions.DeleteForAccount(ctx, accountID); err != nil {
		return fmt.Errorf("end sessions: %w", err)
	}
	return nil
}

// List returns all operators.
func (a *Accounts) List(ctx context.Context) ([]domain.Account, error) { return a.accounts.List(ctx) }

// Delete removes an operator and their sessions.
func (a *Accounts) Delete(ctx context.Context, email string) error {
	email, err := domain.NormalizeEmail(email)
	if err != nil {
		return err
	}
	return a.accounts.Delete(ctx, email)
}

// Count returns how many operators exist.
func (a *Accounts) Count(ctx context.Context) (int, error) { return a.accounts.Count(ctx) }

// PurgeExpired deletes expired sessions.
func (a *Accounts) PurgeExpired(ctx context.Context) (int64, error) {
	return a.sessions.DeleteExpired(ctx, a.now())
}

// GeneratePassword returns a random 24-character password for the CLI.
func GeneratePassword() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func newToken() (string, []byte, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	t := base64.RawURLEncoding.EncodeToString(b[:])
	return t, hashToken(t), nil
}

func hashToken(t string) []byte {
	sum := sha256.Sum256([]byte(t))
	return sum[:]
}
