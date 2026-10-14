package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mini-base/mini-base/internal/platform/errs"
)

var errEmailTaken = errs.Conflict("auth.email_taken", "Email is already registered")

func TestWrapKeepsIdentityAndCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("unique_violation")
	err := fmt.Errorf("create user: %w", errEmailTaken.Wrap(cause))

	if !errors.Is(err, errEmailTaken) {
		t.Fatal("wrapped error should match its sentinel")
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped error should expose its cause")
	}
	if got := errs.KindOf(err); got != errs.KindConflict {
		t.Fatalf("KindOf = %v, want conflict", got)
	}
	if errEmailTaken.Err != nil {
		t.Fatal("Wrap must not mutate the sentinel")
	}
}

func TestKindOfPlainErrorIsInternal(t *testing.T) {
	t.Parallel()

	if got := errs.KindOf(errors.New("boom")); got != errs.KindInternal {
		t.Fatalf("KindOf = %v, want internal", got)
	}
}

func TestDifferentCodesAreNotEqual(t *testing.T) {
	t.Parallel()

	other := errs.Conflict("auth.phone_taken", "Phone is already registered")
	if errors.Is(other, errEmailTaken) {
		t.Fatal("errors with different codes must not match")
	}
}
