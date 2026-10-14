package token_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/modules/auth/adapter/token"
	"github.com/mini-base/mini-base/internal/modules/auth/domain"
	"github.com/mini-base/mini-base/internal/platform/jwt"
	"github.com/mini-base/mini-base/pkg/ports"
)

var (
	secret = []byte("0123456789abcdef0123456789abcdef")
	user   = domain.User{ID: "0b9f6c4e-3f0a-4d7e-9a43-5b1d2e3f4a5b", Email: "a@example.com"}
	t0     = time.Unix(1_700_000_000, 0)
)

func newSvc(t *testing.T, now time.Time) *token.Service {
	t.Helper()
	s, err := token.New(secret, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s.WithClock(func() time.Time { return now })
}

func TestIssueThenVerify(t *testing.T) {
	t.Parallel()

	tok, exp, err := newSvc(t, t0).Issue(user, "sess-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(t0.Add(15 * time.Minute)) {
		t.Fatalf("exp = %v", exp)
	}

	p, err := newSvc(t, t0.Add(time.Minute)).VerifyAccessToken(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if p.Role != ports.RoleAuthenticated || p.UserID != user.ID || p.SessionID != "sess-1" {
		t.Fatalf("principal = %+v", p)
	}
	if p.Claims["sub"] != user.ID || p.Claims["role"] != "authenticated" {
		t.Fatalf("claims = %v", p.Claims)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	t.Parallel()

	tok, _, _ := newSvc(t, t0).Issue(user, "sess-1", t0)
	_, err := newSvc(t, t0.Add(16*time.Minute)).VerifyAccessToken(context.Background(), tok)
	if !errors.Is(err, token.ErrExpired) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyRejectsForeignClaims(t *testing.T) {
	t.Parallel()

	signer, _ := jwt.NewHS256(secret)
	base := token.Claims{
		Issuer: token.Issuer, Subject: user.ID, Audience: token.Audience, Role: "authenticated",
		SessionID: "s", IssuedAt: t0.Unix(), ExpiresAt: t0.Add(time.Hour).Unix(),
	}
	cases := map[string]func(*token.Claims){
		"other issuer":     func(c *token.Claims) { c.Issuer = "someone-else" },
		"service role":     func(c *token.Claims) { c.Role = "service" },
		"subject not uuid": func(c *token.Claims) { c.Subject = "admin" },
		"issued in future": func(c *token.Claims) { c.IssuedAt = t0.Add(time.Hour).Unix() },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := base
			mutate(&c)
			tok, _ := signer.Sign(c)
			if _, err := newSvc(t, t0).VerifyAccessToken(context.Background(), tok); !errors.Is(err, token.ErrClaims) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
