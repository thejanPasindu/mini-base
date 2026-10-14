// Package domain holds the example module's entities and rules.
// It has no I/O and imports nothing from the rest of the project except errs.
package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

// MaxMessageLength is the longest allowed greeting, in characters.
const MaxMessageLength = 280

// Domain errors. Codes are part of the public API: never change them.
var (
	ErrMessageEmpty   = errs.Invalid("example.message_empty", "Message must not be empty")
	ErrMessageTooLong = errs.Invalid("example.message_too_long", "Message must be at most 280 characters")
)

// Greeting is a persisted message.
type Greeting struct {
	ID        int64
	Message   string
	CreatedAt time.Time
}

// NewGreeting validates input and returns an unsaved Greeting.
func NewGreeting(message string) (Greeting, error) {
	message = strings.TrimSpace(message)
	switch {
	case message == "":
		return Greeting{}, ErrMessageEmpty
	case utf8.RuneCountInString(message) > MaxMessageLength:
		return Greeting{}, ErrMessageTooLong
	}
	return Greeting{Message: message}, nil
}
