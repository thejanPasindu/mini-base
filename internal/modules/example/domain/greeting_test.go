package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mini-base/mini-base/internal/modules/example/domain"
)

func TestNewGreeting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"trims", "  hello  ", "hello", nil},
		{"empty", "   ", "", domain.ErrMessageEmpty},
		{"max length in runes", strings.Repeat("é", 280), strings.Repeat("é", 280), nil},
		{"too long", strings.Repeat("a", 281), "", domain.ErrMessageTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g, err := domain.NewGreeting(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if g.Message != tt.want {
				t.Fatalf("message = %q, want %q", g.Message, tt.want)
			}
		})
	}
}
