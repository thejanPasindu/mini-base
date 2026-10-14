package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/modules/auth/domain"
	"github.com/mini-base/mini-base/internal/modules/auth/port"
	"github.com/mini-base/mini-base/internal/modules/auth/service"
)

// ---- fakes ----------------------------------------------------------------

type memUsers struct {
	mu     sync.Mutex
	byID   map[string]domain.User
	hashes map[string]string // id -> hash
}

func newMemUsers() *memUsers {
	return &memUsers{byID: map[string]domain.User{}, hashes: map[string]string{}}
}

func (m *memUsers) Create(_ context.Context, email, hash string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.byID {
		if u.Email == email {
			return domain.User{}, domain.ErrEmailTaken
		}
	}
	u := domain.User{ID: fmt.Sprintf("user-%d", len(m.byID)+1), Email: email}
	m.byID[u.ID], m.hashes[u.ID] = u, hash
	return u, nil
}

func (m *memUsers) FindByEmail(_ context.Context, email string) (domain.User, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, u := range m.byID {
		if u.Email == email {
			return u, m.hashes[id], nil
		}
	}
	return domain.User{}, "", domain.ErrUserNotFound
}

func (m *memUsers) FindByID(_ context.Context, id string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	return u, nil
}

func (m *memUsers) RecordSignIn(context.Context, string, time.Time) error { return nil }

type memToken struct {
	hash    []byte
	session string
	used    bool
}

type memSessions struct {
	mu       sync.Mutex
	sessions map[string]*domain.Session
	tokens   []*memToken
}

func newMemSessions() *memSessions { return &memSessions{sessions: map[string]*domain.Session{}} }

func (m *memSessions) Create(_ context.Context, userID string, hash []byte, exp time.Time, _ domain.ClientInfo) (domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &domain.Session{ID: fmt.Sprintf("sess-%d", len(m.sessions)+1), UserID: userID, ExpiresAt: exp}
	m.sessions[s.ID] = s
	m.tokens = append(m.tokens, &memToken{hash: hash, session: s.ID})
	return *s, nil
}

func (m *memSessions) Rotate(_ context.Context, oldHash, newHash []byte, now time.Time) (domain.Session, port.RotateOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if !bytes.Equal(t.hash, oldHash) {
			continue
		}
		s := m.sessions[t.session]
		switch {
		case t.used:
			return *s, port.Reused, nil
		case !s.Active(now):
			return *s, port.Inactive, nil
		}
		t.used = true
		m.tokens = append(m.tokens, &memToken{hash: newHash, session: s.ID})
		return *s, port.Rotated, nil
	}
	return domain.Session{}, port.NotFound, nil
}

func (m *memSessions) Revoke(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok && s.RevokedAt == nil {
		s.RevokedAt = &at
	}
	return nil
}

func (m *memSessions) DeleteStale(context.Context, time.Time) (int64, error) { return 0, nil }

type plainHasher struct{ verifyCalls int }

func (h *plainHasher) Hash(_ context.Context, pw string) (string, error) { return "hash:" + pw, nil }
func (h *plainHasher) Verify(_ context.Context, pw, enc string) (bool, error) {
	h.verifyCalls++
	return enc == "hash:"+pw, nil
}

type fakeIssuer struct{}

func (fakeIssuer) Issue(u domain.User, sid string, now time.Time) (string, time.Time, error) {
	return "access:" + u.ID + ":" + sid, now.Add(15 * time.Minute), nil
}

type events struct{ names []string }

func (e *events) Publish(_ context.Context, name string, _ any) { e.names = append(e.names, name) }

// ---- helpers ----------------------------------------------------------------

type fixture struct {
	svc      *service.Service
	sessions *memSessions
	hasher   *plainHasher
	events   *events
	now      time.Time
}

func newFixture(t *testing.T, opts ...func(*service.Options)) *fixture {
	t.Helper()
	o := service.Options{SessionTTL: 24 * time.Hour, PasswordMinLength: 8, AllowSignup: true}
	for _, fn := range opts {
		fn(&o)
	}
	f := &fixture{sessions: newMemSessions(), hasher: &plainHasher{}, events: &events{}, now: time.Unix(1_700_000_000, 0)}
	svc, err := service.New(newMemUsers(), f.sessions, f.hasher, fakeIssuer{}, f.events, o)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc.WithClock(func() time.Time { return f.now })
	return f
}

var ctx = context.Background()

// ---- tests ------------------------------------------------------------------

func TestSignUpAndSignIn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	res, err := f.svc.SignUp(ctx, " Alice@Example.com ", "correct horse", domain.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Email != "alice@example.com" || res.AccessToken == "" || res.RefreshToken == "" {
		t.Fatalf("result = %+v", res)
	}
	if !res.SessionExpiresAt.Equal(f.now.Add(24 * time.Hour)) {
		t.Fatalf("session expiry = %v", res.SessionExpiresAt)
	}
	if strings.Join(f.events.names, ",") != "auth.user.created,auth.session.created" {
		t.Fatalf("events = %v", f.events.names)
	}

	if _, err := f.svc.SignUp(ctx, "alice@example.com", "another one", domain.ClientInfo{}); !errors.Is(err, domain.ErrEmailTaken) {
		t.Fatalf("duplicate sign-up err = %v", err)
	}

	if _, err := f.svc.SignInWithPassword(ctx, "ALICE@example.com", "correct horse", domain.ClientInfo{}); err != nil {
		t.Fatalf("sign-in: %v", err)
	}
}

func TestSignUpValidation(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	if _, err := f.svc.SignUp(ctx, "bad", "correct horse", domain.ClientInfo{}); !errors.Is(err, domain.ErrInvalidEmail) {
		t.Fatalf("email: %v", err)
	}
	if _, err := f.svc.SignUp(ctx, "a@example.com", "short", domain.ClientInfo{}); !errors.Is(err, domain.ErrWeakPassword) {
		t.Fatalf("password: %v", err)
	}

	closed := newFixture(t, func(o *service.Options) { o.AllowSignup = false })
	if _, err := closed.svc.SignUp(ctx, "a@example.com", "correct horse", domain.ClientInfo{}); !errors.Is(err, domain.ErrSignupDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}

func TestSignInFailuresAreIndistinguishable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.svc.SignUp(ctx, "bob@example.com", "correct horse", domain.ClientInfo{}); err != nil {
		t.Fatal(err)
	}

	before := f.hasher.verifyCalls
	_, errUnknown := f.svc.SignInWithPassword(ctx, "nobody@example.com", "correct horse", domain.ClientInfo{})
	_, errWrong := f.svc.SignInWithPassword(ctx, "bob@example.com", "wrong password", domain.ClientInfo{})

	if !errors.Is(errUnknown, domain.ErrInvalidCredentials) || !errors.Is(errWrong, domain.ErrInvalidCredentials) {
		t.Fatalf("errors = %v / %v", errUnknown, errWrong)
	}
	if f.hasher.verifyCalls-before != 2 {
		t.Fatal("unknown email must still run a password verification (timing)")
	}
}

func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first, err := f.svc.SignUp(ctx, "carol@example.com", "correct horse", domain.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}

	second, err := f.svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}

	// An attacker replays the old token: the session must be revoked…
	if _, err := f.svc.Refresh(ctx, first.RefreshToken); !errors.Is(err, domain.ErrRefreshTokenReused) {
		t.Fatalf("reuse err = %v", err)
	}
	// …so even the legitimate latest token stops working.
	if _, err := f.svc.Refresh(ctx, second.RefreshToken); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("after revocation err = %v", err)
	}
	if f.events.names[len(f.events.names)-1] != service.EventSessionRevoked {
		t.Fatalf("events = %v", f.events.names)
	}
}

func TestRefreshUnknownAndExpired(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	if _, err := f.svc.Refresh(ctx, "not-a-token"); !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := f.svc.Refresh(ctx, ""); !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("empty: %v", err)
	}

	res, _ := f.svc.SignUp(ctx, "dave@example.com", "correct horse", domain.ClientInfo{})
	f.now = f.now.Add(25 * time.Hour)
	if _, err := f.svc.Refresh(ctx, res.RefreshToken); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("expired: %v", err)
	}
}

func TestSignOutRevokesSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	res, _ := f.svc.SignUp(ctx, "erin@example.com", "correct horse", domain.ClientInfo{})

	sessionID := strings.Split(res.AccessToken, ":")[2]
	if err := f.svc.SignOut(ctx, res.User.ID, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(ctx, res.RefreshToken); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("refresh after sign-out: %v", err)
	}
}
