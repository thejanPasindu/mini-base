// Package token issues and verifies access tokens (HS256 JWTs).
package token

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/mini-base/mini-base/internal/modules/auth/domain"
	"github.com/mini-base/mini-base/internal/platform/jwt"
	"github.com/mini-base/mini-base/pkg/ports"
)

// Fixed claim values.
const (
	Issuer   = "minibase"
	Audience = "authenticated"
)

// clockSkew tolerates small clock differences between issuer and verifier.
const clockSkew = 30 * time.Second

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Claims are the access token's payload. They are also exposed to SQL as
// request.jwt.claims, so auth.uid() returns Subject.
type Claims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  string `json:"aud"`
	Role      string `json:"role"`
	Email     string `json:"email"`
	SessionID string `json:"sid"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// Service issues tokens (port.TokenIssuer) and verifies them
// (ports.TokenVerifier).
type Service struct {
	signer *jwt.HS256
	ttl    time.Duration
	now    func() time.Time
}

// New returns a token service.
func New(secret []byte, ttl time.Duration) (*Service, error) {
	s, err := jwt.NewHS256(secret)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	return &Service{signer: s, ttl: ttl, now: time.Now}, nil
}

// WithClock replaces the verification time source (tests).
func (s *Service) WithClock(now func() time.Time) *Service { s.now = now; return s }

// Issue implements port.TokenIssuer.
func (s *Service) Issue(u domain.User, sessionID string, now time.Time) (string, time.Time, error) {
	exp := now.Add(s.ttl)
	tok, err := s.signer.Sign(Claims{
		Issuer: Issuer, Subject: u.ID, Audience: Audience, Role: string(ports.RoleAuthenticated),
		Email: u.Email, SessionID: sessionID, IssuedAt: now.Unix(), ExpiresAt: exp.Unix(),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return tok, exp, nil
}

// Verification errors (wrapped into auth.invalid_token for clients).
var (
	ErrExpired = errors.New("token: expired")
	ErrClaims  = errors.New("token: unexpected claims")
)

// VerifyAccessToken implements ports.TokenVerifier.
func (s *Service) VerifyAccessToken(_ context.Context, token string) (ports.Principal, error) {
	payload, err := s.signer.Verify(token)
	if err != nil {
		return ports.Principal{}, fmt.Errorf("verify access token: %w", err)
	}

	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return ports.Principal{}, ErrClaims
	}
	now := s.now()
	switch {
	case c.Issuer != Issuer || c.Audience != Audience || c.Role != string(ports.RoleAuthenticated):
		return ports.Principal{}, ErrClaims
	case !uuidPattern.MatchString(c.Subject) || c.SessionID == "":
		return ports.Principal{}, ErrClaims
	case now.After(time.Unix(c.ExpiresAt, 0).Add(clockSkew)):
		return ports.Principal{}, ErrExpired
	case time.Unix(c.IssuedAt, 0).After(now.Add(clockSkew)):
		return ports.Principal{}, ErrClaims // issued in the future
	}

	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ports.Principal{}, ErrClaims
	}
	return ports.Principal{
		Role:      ports.RoleAuthenticated,
		UserID:    c.Subject,
		SessionID: c.SessionID,
		Claims:    raw,
	}, nil
}
