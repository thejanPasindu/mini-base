// Package errs defines the typed application error used across mini-base.
//
// Every error that can reach an API client is an *Error carrying a Kind (which
// maps to an HTTP status in exactly one place, see httpx.WriteError) and a
// stable machine-readable Code such as "auth.email_taken". The wrapped cause is
// for logs only and is never sent to clients.
package errs

import (
	"errors"
	"fmt"
)

// Kind classifies an error independently of any transport.
type Kind uint8

// Error kinds. KindInternal is the zero value so an unclassified error is
// treated as a server fault, never accidentally as a client error.
const (
	KindInternal Kind = iota
	KindInvalid
	KindUnauthorized
	KindForbidden
	KindNotFound
	KindConflict
	KindTooLarge
	KindRateLimited
	KindUnavailable
)

// String returns the lowercase name of the kind.
func (k Kind) String() string {
	switch k {
	case KindInvalid:
		return "invalid"
	case KindUnauthorized:
		return "unauthorized"
	case KindForbidden:
		return "forbidden"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindTooLarge:
		return "too_large"
	case KindRateLimited:
		return "rate_limited"
	case KindUnavailable:
		return "unavailable"
	default:
		return "internal"
	}
}

// Error is the application error type.
type Error struct {
	Kind    Kind
	Code    string // stable, machine-readable, e.g. "auth.email_taken"
	Message string // human-readable, safe to show to API clients
	Err     error  // underlying cause; logged, never exposed
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause to errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Is reports whether target is an *Error with the same Code, so that
// sentinel errors declared with the constructors below work with errors.Is
// even after Wrap has produced a copy.
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return t.Code == e.Code
}

// Wrap returns a copy of e with cause attached. Use it to keep a sentinel's
// code and message while recording what actually went wrong.
func (e *Error) Wrap(cause error) *Error {
	c := *e
	c.Err = cause
	return &c
}

// New builds an *Error of the given kind.
func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// Invalid is for malformed or semantically invalid input (HTTP 400).
func Invalid(code, message string) *Error { return New(KindInvalid, code, message) }

// Unauthorized is for missing or invalid credentials (HTTP 401).
func Unauthorized(code, message string) *Error { return New(KindUnauthorized, code, message) }

// Forbidden is for valid credentials lacking permission (HTTP 403).
func Forbidden(code, message string) *Error { return New(KindForbidden, code, message) }

// NotFound is for absent resources (HTTP 404).
func NotFound(code, message string) *Error { return New(KindNotFound, code, message) }

// Conflict is for state conflicts such as duplicates (HTTP 409).
func Conflict(code, message string) *Error { return New(KindConflict, code, message) }

// Unavailable is for temporarily unavailable dependencies (HTTP 503).
func Unavailable(code, message string) *Error { return New(KindUnavailable, code, message) }

// Internal wraps an unexpected failure (HTTP 500). The cause is kept for logs.
func Internal(code string, cause error) *Error {
	return &Error{Kind: KindInternal, Code: code, Message: "An internal error occurred", Err: cause}
}

// As extracts the first *Error in err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// KindOf returns the Kind of err, or KindInternal for errors that are not *Error.
func KindOf(err error) Kind {
	if e, ok := As(err); ok {
		return e.Kind
	}
	return KindInternal
}
