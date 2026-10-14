package jwt_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mini-base/mini-base/internal/platform/jwt"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestSignVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	s, err := jwt.NewHS256(secret)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.Sign(map[string]any{"sub": "u1", "exp": 123})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := s.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil || got["sub"] != "u1" {
		t.Fatalf("payload = %s", payload)
	}
}

// This token was produced by an independent HS256 implementation (Python's
// hmac module) and must verify, proving interoperability.
func TestVerifyKnownVector(t *testing.T) {
	t.Parallel()

	const token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ." +
		"l5R_LGI0U0AUZYlfzUidod1M4vlgu0JtPP8XpJB9Jx0"
	s, _ := jwt.NewHS256([]byte("your-256-bit-secret-your-256-bit"))
	payload, err := s.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"name":"John Doe"`) {
		t.Fatalf("payload = %s", payload)
	}

	signed, _ := s.Sign(map[string]string{"sub": "x"})
	if !strings.HasPrefix(signed, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.") {
		t.Fatalf("header encoding differs from the standard one: %s", signed)
	}
}

func TestVerifyRejects(t *testing.T) {
	t.Parallel()

	s, _ := jwt.NewHS256(secret)
	other, _ := jwt.NewHS256([]byte("ffffffffffffffffffffffffffffffff"))
	good, _ := s.Sign(map[string]any{"sub": "u1"})
	parts := strings.Split(good, ".")

	enc := base64.RawURLEncoding.EncodeToString
	noneHeader := enc([]byte(`{"alg":"none","typ":"JWT"}`))
	tamperedPayload := enc([]byte(`{"sub":"admin"}`))

	tests := map[string]struct {
		token string
		want  error
	}{
		"wrong secret":      {mustSign(t, other), jwt.ErrSignature},
		"alg none":          {noneHeader + "." + parts[1] + ".", jwt.ErrAlgorithm},
		"tampered payload":  {parts[0] + "." + tamperedPayload + "." + parts[2], jwt.ErrSignature},
		"two segments":      {parts[0] + "." + parts[1], jwt.ErrMalformed},
		"garbage signature": {parts[0] + "." + parts[1] + ".!!!", jwt.ErrMalformed},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := s.Verify(tt.token); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestShortSecretRejected(t *testing.T) {
	t.Parallel()

	if _, err := jwt.NewHS256([]byte("short")); !errors.Is(err, jwt.ErrShortSecret) {
		t.Fatalf("err = %v", err)
	}
}

func mustSign(t *testing.T, s *jwt.HS256) string {
	t.Helper()
	tok, err := s.Sign(map[string]any{"sub": "u1"})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
