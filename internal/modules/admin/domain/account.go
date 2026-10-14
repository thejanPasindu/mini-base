// Package domain holds the admin module's entities, rules and pure SQL
// builders. Nothing here performs I/O.
package domain

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// Admin passwords are held to a higher bar than app users': these accounts
// can run arbitrary SQL.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 256
)

// Errors. Codes are part of the dashboard API.
var (
	ErrInvalidEmail       = errs.Invalid("admin.invalid_email", "Email address is not valid")
	ErrWeakPassword       = errs.Invalid("admin.weak_password", "Admin passwords must be at least 12 characters")
	ErrPasswordTooLong    = errs.Invalid("admin.password_too_long", "Password must be at most 256 characters")
	ErrAccountExists      = errs.Conflict("admin.account_exists", "An admin with this email already exists")
	ErrAccountNotFound    = errs.NotFound("admin.account_not_found", "Admin account not found")
	ErrInvalidCredentials = errs.Unauthorized("admin.invalid_credentials", "Email or password is incorrect")
	ErrNotSignedIn        = errs.Unauthorized("admin.not_signed_in", "Sign in to the dashboard")
)

// Account is a dashboard operator.
type Account struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSignInAt *time.Time `json:"last_sign_in_at"`
}

// ClientInfo records where a session was opened.
type ClientInfo struct {
	IP        string
	UserAgent string
}

// NormalizeEmail validates an email and returns its lowercase form.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 254 {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@"):], ".") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// ValidatePassword enforces the admin password policy.
func ValidatePassword(pw string) error {
	switch {
	case utf8.RuneCountInString(pw) < MinPasswordLength:
		return ErrWeakPassword
	case len(pw) > MaxPasswordLength:
		return ErrPasswordTooLong
	}
	return nil
}
