package postgres

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPermanentFailure(t *testing.T) {
	t.Parallel()

	wrapped := func(code string) error {
		return fmt.Errorf("failed to connect: %w", &pgconn.PgError{Code: code})
	}
	if h := permanentFailure(wrapped("28P01")); !strings.Contains(h, "POSTGRES_PASSWORD") {
		t.Fatalf("auth failure hint = %q", h)
	}
	if h := permanentFailure(wrapped("3D000")); !strings.Contains(h, "does not exist") {
		t.Fatalf("missing db hint = %q", h)
	}
	if h := permanentFailure(wrapped("57P03")); h != "" { // cannot_connect_now: server starting up
		t.Fatalf("startup error must be retried, got %q", h)
	}
	if h := permanentFailure(errors.New("connection refused")); h != "" {
		t.Fatalf("network error must be retried, got %q", h)
	}
}
