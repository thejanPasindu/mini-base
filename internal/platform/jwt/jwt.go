// Package jwt signs and verifies HS256 JSON Web Tokens with the standard
// library only. It deliberately supports a single algorithm: accepting the
// algorithm named in the token header is the classic JWT vulnerability.
//
// Claim semantics (exp, iss, aud…) are validated by the caller, which knows
// what it expects.
package jwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MinSecretLength is the minimum HMAC secret length in bytes.
const MinSecretLength = 32

// Errors.
var (
	ErrMalformed    = errors.New("jwt: malformed token")
	ErrAlgorithm    = errors.New("jwt: unsupported algorithm")
	ErrSignature    = errors.New("jwt: invalid signature")
	ErrShortSecret  = fmt.Errorf("jwt: secret must be at least %d bytes", MinSecretLength)
	encodedHS256Hdr = b64(mustJSON(header{Alg: "HS256", Typ: "JWT"}))
)

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ,omitempty"`
}

// HS256 signs and verifies tokens with one shared secret.
type HS256 struct {
	secret []byte
}

// NewHS256 returns a signer. The secret must be at least MinSecretLength bytes.
func NewHS256(secret []byte) (*HS256, error) {
	if len(secret) < MinSecretLength {
		return nil, ErrShortSecret
	}
	return &HS256{secret: append([]byte(nil), secret...)}, nil
}

// Sign encodes claims (any JSON-serialisable value) into a token.
func (s *HS256) Sign(claims any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("jwt: encode claims: %w", err)
	}
	unsigned := encodedHS256Hdr + "." + b64(payload)
	return unsigned + "." + b64(s.mac(unsigned)), nil
}

// Verify checks the token's structure, algorithm and signature and returns
// the raw JSON payload for the caller to decode and validate.
func (s *HS256) Verify(token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}

	rawHeader, err := unb64(parts[0])
	if err != nil {
		return nil, ErrMalformed
	}
	var h header
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return nil, ErrMalformed
	}
	if h.Alg != "HS256" {
		return nil, ErrAlgorithm
	}

	sig, err := unb64(parts[2])
	if err != nil {
		return nil, ErrMalformed
	}
	if !hmac.Equal(sig, s.mac(parts[0]+"."+parts[1])) {
		return nil, ErrSignature
	}

	payload, err := unb64(parts[1])
	if err != nil || !json.Valid(payload) {
		return nil, ErrMalformed
	}
	return payload, nil
}

func (s *HS256) mac(data string) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
