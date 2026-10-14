// Package service implements the auth use cases: sign-up, sign-in, token
// refresh with rotation and reuse detection, sign-out.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/mini-base/mini-base/internal/modules/auth/domain"
	"github.com/mini-base/mini-base/internal/modules/auth/port"
)

// Events published by the service.
const (
	EventUserCreated    = "auth.user.created"
	EventSessionCreated = "auth.session.created"
	EventSessionRevoked = "auth.session.revoked"
)

// Options are the policy settings.
type Options struct {
	SessionTTL        time.Duration // absolute lifetime of a refresh-token chain
	PasswordMinLength int
	AllowSignup       bool
}

// AuthResult is returned by every flow that signs a user in.
type AuthResult struct {
	User             domain.User
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	SessionExpiresAt time.Time
}

// Service implements the use cases.
type Service struct {
	users    port.UserRepository
	sessions port.SessionRepository
	hasher   port.PasswordHasher
	tokens   port.TokenIssuer
	events   port.EventPublisher
	opts     Options
	now      func() time.Time

	// dummyHash is verified when an email is unknown, so a failed sign-in
	// takes the same time whether or not the account exists.
	dummyHash string
}

// New returns a Service.
func New(users port.UserRepository, sessions port.SessionRepository, hasher port.PasswordHasher,
	tokens port.TokenIssuer, events port.EventPublisher, opts Options,
) (*Service, error) {
	dummy, err := hasher.Hash(context.Background(), "dummy-password-for-timing-equalisation")
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	return &Service{
		users: users, sessions: sessions, hasher: hasher, tokens: tokens, events: events,
		opts: opts, now: time.Now, dummyHash: dummy,
	}, nil
}

// WithClock replaces the time source (tests).
func (s *Service) WithClock(now func() time.Time) *Service { s.now = now; return s }

// SignUp creates an account and signs it in.
func (s *Service) SignUp(ctx context.Context, email, password string, client domain.ClientInfo) (AuthResult, error) {
	if !s.opts.AllowSignup {
		return AuthResult{}, domain.ErrSignupDisabled
	}
	email, err := domain.NormalizeEmail(email)
	if err != nil {
		return AuthResult{}, err
	}
	if err := domain.ValidatePassword(password, s.opts.PasswordMinLength); err != nil {
		return AuthResult{}, err
	}

	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return AuthResult{}, fmt.Errorf("hash password: %w", err)
	}
	user, err := s.users.Create(ctx, email, hash)
	if err != nil {
		return AuthResult{}, fmt.Errorf("create user: %w", err)
	}
	s.events.Publish(ctx, EventUserCreated, user)

	return s.startSession(ctx, user, client)
}

// SignInWithPassword checks credentials and starts a session. Unknown email
// and wrong password are indistinguishable to the caller, including timing.
func (s *Service) SignInWithPassword(ctx context.Context, email, password string, client domain.ClientInfo) (AuthResult, error) {
	email, err := domain.NormalizeEmail(email)
	if err != nil || len(password) > domain.MaxPasswordLength {
		return AuthResult{}, domain.ErrInvalidCredentials
	}

	user, hash, err := s.users.FindByEmail(ctx, email)
	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		_, _ = s.hasher.Verify(ctx, password, s.dummyHash)
		return AuthResult{}, domain.ErrInvalidCredentials
	case err != nil:
		return AuthResult{}, fmt.Errorf("find user: %w", err)
	}

	ok, err := s.hasher.Verify(ctx, password, hash)
	if err != nil {
		return AuthResult{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return AuthResult{}, domain.ErrInvalidCredentials
	}

	now := s.now()
	if err := s.users.RecordSignIn(ctx, user.ID, now); err != nil {
		return AuthResult{}, fmt.Errorf("record sign-in: %w", err)
	}
	user.LastSignInAt = &now
	return s.startSession(ctx, user, client)
}

// Refresh exchanges a refresh token for a new access token and a new refresh
// token. Each refresh token works once. Presenting a used one revokes the
// whole session, because it means the token was copied.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (AuthResult, error) {
	if refreshToken == "" {
		return AuthResult{}, domain.ErrInvalidRefreshToken
	}
	newToken, newHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, err
	}

	now := s.now()
	session, outcome, err := s.sessions.Rotate(ctx, hashToken(refreshToken), newHash, now)
	if err != nil {
		return AuthResult{}, fmt.Errorf("rotate refresh token: %w", err)
	}

	switch outcome {
	case port.Rotated:
	case port.NotFound:
		return AuthResult{}, domain.ErrInvalidRefreshToken
	case port.Inactive:
		return AuthResult{}, domain.ErrSessionExpired
	case port.Reused:
		if err := s.sessions.Revoke(ctx, session.ID, now); err != nil {
			return AuthResult{}, fmt.Errorf("revoke session after token reuse: %w", err)
		}
		s.events.Publish(ctx, EventSessionRevoked, map[string]string{
			"session_id": session.ID, "user_id": session.UserID, "reason": "refresh_token_reused",
		})
		return AuthResult{}, domain.ErrRefreshTokenReused
	default:
		return AuthResult{}, fmt.Errorf("unexpected rotate outcome %d", outcome)
	}

	user, err := s.users.FindByID(ctx, session.UserID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("load user: %w", err)
	}
	access, accessExp, err := s.tokens.Issue(user, session.ID, now)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue access token: %w", err)
	}
	return AuthResult{
		User: user, AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: newToken, SessionExpiresAt: session.ExpiresAt,
	}, nil
}

// SignOut revokes a session. Its refresh tokens stop working immediately;
// access tokens already issued stay valid until they expire (minutes).
func (s *Service) SignOut(ctx context.Context, userID, sessionID string) error {
	if err := s.sessions.Revoke(ctx, sessionID, s.now()); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	s.events.Publish(ctx, EventSessionRevoked, map[string]string{
		"session_id": sessionID, "user_id": userID, "reason": "sign_out",
	})
	return nil
}

// GetUser returns a user by ID.
func (s *Service) GetUser(ctx context.Context, id string) (domain.User, error) {
	u, err := s.users.FindByID(ctx, id)
	if err != nil {
		return domain.User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// PurgeStaleSessions deletes sessions that expired or were revoked more than
// `retain` ago. Run periodically.
func (s *Service) PurgeStaleSessions(ctx context.Context, retain time.Duration) (int64, error) {
	n, err := s.sessions.DeleteStale(ctx, s.now().Add(-retain))
	if err != nil {
		return 0, fmt.Errorf("purge sessions: %w", err)
	}
	return n, nil
}

func (s *Service) startSession(ctx context.Context, user domain.User, client domain.ClientInfo) (AuthResult, error) {
	refresh, refreshHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, err
	}
	now := s.now()
	session, err := s.sessions.Create(ctx, user.ID, refreshHash, now.Add(s.opts.SessionTTL), client)
	if err != nil {
		return AuthResult{}, fmt.Errorf("create session: %w", err)
	}
	access, accessExp, err := s.tokens.Issue(user, session.ID, now)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue access token: %w", err)
	}
	s.events.Publish(ctx, EventSessionCreated, map[string]string{"session_id": session.ID, "user_id": user.ID})

	return AuthResult{
		User: user, AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: refresh, SessionExpiresAt: session.ExpiresAt,
	}, nil
}

// newRefreshToken returns a random opaque token and the hash that is stored.
func newRefreshToken() (token string, hash []byte, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, hashToken(token), nil
}

// hashToken hashes a high-entropy token. SHA-256 is appropriate here (unlike
// for passwords) because the input is 256 random bits.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
