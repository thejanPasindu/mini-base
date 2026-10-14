package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/modules/auth/domain"
)

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"  Alice@Example.COM ":   "alice@example.com",
		"a.b+tag@sub.example.lk": "a.b+tag@sub.example.lk",
	}
	for in, want := range valid {
		got, err := domain.NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	for _, in := range []string{"", "nope", "a@b", "Bob <bob@example.com>", "a@@example.com", strings.Repeat("a", 250) + "@x.com"} {
		if _, err := domain.NormalizeEmail(in); !errors.Is(err, domain.ErrInvalidEmail) {
			t.Errorf("NormalizeEmail(%q) err = %v, want invalid", in, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()

	if err := domain.ValidatePassword("1234567", 8); !errors.Is(err, domain.ErrWeakPassword) {
		t.Fatalf("short: %v", err)
	}
	if err := domain.ValidatePassword("12345678", 8); err != nil {
		t.Fatalf("ok: %v", err)
	}
	if err := domain.ValidatePassword(strings.Repeat("x", 257), 8); !errors.Is(err, domain.ErrPasswordTooLong) {
		t.Fatalf("long: %v", err)
	}
}

func TestSessionActive(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	s := domain.Session{ExpiresAt: now.Add(time.Hour)}
	if !s.Active(now) {
		t.Fatal("fresh session should be active")
	}
	if s.Active(now.Add(2 * time.Hour)) {
		t.Fatal("expired session should be inactive")
	}
	s.RevokedAt = &now
	if s.Active(now) {
		t.Fatal("revoked session should be inactive")
	}
}
