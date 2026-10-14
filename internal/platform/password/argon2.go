// Package password hashes passwords with Argon2id, encoded in the standard
// PHC string format so parameters can be raised later without breaking old
// hashes: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are Argon2id cost parameters.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// DefaultParams follow the OWASP minimum recommendation (19 MiB, t=2, p=1),
// chosen to stay usable on small machines such as a Raspberry Pi.
var DefaultParams = Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32}

var errInvalidHash = errors.New("password: invalid encoded hash")

// Argon2id implements port.PasswordHasher. It bounds concurrent hashes so a
// burst of sign-ins cannot exhaust memory (each hash uses MemoryKiB).
type Argon2id struct {
	params Params
	slots  chan struct{}
}

// NewArgon2id returns a hasher allowing maxConcurrent simultaneous hashes.
func NewArgon2id(p Params, maxConcurrent int) *Argon2id {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Argon2id{params: p, slots: make(chan struct{}, maxConcurrent)}
}

func (a *Argon2id) acquire(ctx context.Context) error {
	select {
	case a.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("password: waiting for hash slot: %w", ctx.Err())
	}
}

func (a *Argon2id) release() { <-a.slots }

// Hash returns the PHC-encoded Argon2id hash of password.
func (a *Argon2id) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, a.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: salt: %w", err)
	}
	if err := a.acquire(ctx); err != nil {
		return "", err
	}
	defer a.release()

	p := a.params
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded, using the parameters
// stored in encoded.
func (a *Argon2id) Verify(ctx context.Context, password, encoded string) (bool, error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, err
	}
	if err := a.acquire(ctx); err != nil {
		return false, err
	}
	defer a.release()

	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want))) //nolint:gosec // len bounded by decode
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var b64 = base64.RawStdEncoding

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, errInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, errInvalidHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, errInvalidHash
	}
	if p.MemoryKiB == 0 || p.MemoryKiB > 1<<20 || p.Iterations == 0 || p.Iterations > 100 || p.Parallelism == 0 {
		return Params{}, nil, nil, errInvalidHash // refuse absurd costs from a tampered row
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, errInvalidHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 128 {
		return Params{}, nil, nil, errInvalidHash
	}
	return p, salt, key, nil
}
