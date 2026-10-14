package password_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mini-base/mini-base/internal/platform/password"
)

// Small parameters keep tests fast; production uses DefaultParams.
var testParams = password.Params{MemoryKiB: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func TestHashAndVerify(t *testing.T) {
	t.Parallel()
	h := password.NewArgon2id(testParams, 2)
	ctx := context.Background()

	enc, err := h.Hash(ctx, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("encoding = %s", enc)
	}

	if ok, err := h.Verify(ctx, "correct horse", enc); err != nil || !ok {
		t.Fatalf("correct password: %v %v", ok, err)
	}
	if ok, _ := h.Verify(ctx, "wrong horse", enc); ok {
		t.Fatal("wrong password accepted")
	}

	again, _ := h.Hash(ctx, "correct horse")
	if again == enc {
		t.Fatal("hashes must be salted")
	}
}

// Hashes made with other parameters keep verifying after defaults change.
func TestVerifyUsesStoredParams(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	enc, _ := password.NewArgon2id(testParams, 1).Hash(ctx, "pw-123456")
	if ok, err := password.NewArgon2id(password.DefaultParams, 1).Verify(ctx, "pw-123456", enc); err != nil || !ok {
		t.Fatalf("verify with different defaults: %v %v", ok, err)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	t.Parallel()
	h := password.NewArgon2id(testParams, 1)
	for _, enc := range []string{
		"",
		"plain",
		"$bcrypt$v=19$m=1024,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA", // absurd memory
	} {
		if _, err := h.Verify(context.Background(), "x", enc); err == nil {
			t.Errorf("accepted %q", enc)
		}
	}
}
