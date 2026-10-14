// Package domain holds the auth module's entities, rules and errors.
package domain

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// Limits.
const (
	MaxEmailLength    = 254
	MaxPasswordLength = 256 // bounds hashing cost per request
)

// Errors. Codes are part of the public API.
var (
	ErrInvalidEmail        = errs.Invalid("auth.invalid_email", "Email address is not valid")
	ErrWeakPassword        = errs.Invalid("auth.weak_password", "Password is too short")
	ErrPasswordTooLong     = errs.Invalid("auth.password_too_long", "Password must be at most 256 characters")
	ErrEmailTaken          = errs.Conflict("auth.email_taken", "Email is already registered")
	ErrInvalidCredentials  = errs.Unauthorized("auth.invalid_credentials", "Email or password is incorrect")
	ErrInvalidRefreshToken = errs.Unauthorized("auth.invalid_refresh_token", "Refresh token is invalid")
	ErrRefreshTokenReused  = errs.Unauthorized("auth.refresh_token_reused", "Refresh token was already used; the session has been revoked")
	ErrSessionExpired      = errs.Unauthorized("auth.session_expired", "Session has expired or was revoked")
	ErrSignupDisabled      = errs.Forbidden("auth.signup_disabled", "Sign-up is disabled")
	ErrUserNotFound        = errs.NotFound("auth.user_not_found", "User not found")
)

// User is an account.
type User struct {
	ID           string
	Email        string
	CreatedAt    time.Time
	LastSignInAt *time.Time
}

// Session is a signed-in device. Refresh tokens belong to a session; access
// tokens carry its ID.
type Session struct {
	ID        string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// Active reports whether the session can still be refreshed at now.
func (s Session) Active(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// ClientInfo describes where a session was created, for the user's records.
type ClientInfo struct {
	UserAgent string
	IP        string
}

// NormalizeEmail validates an email address and returns its canonical
// lowercase form. Display names ("Bob <bob@x>") are rejected.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > MaxEmailLength {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@"):], ".") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// ValidatePassword enforces the password policy.
func ValidatePassword(pw string, minLength int) error {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < minLength:
		return ErrWeakPassword
	case len(pw) > MaxPasswordLength:
		return ErrPasswordTooLong
	}
	return nil
}
