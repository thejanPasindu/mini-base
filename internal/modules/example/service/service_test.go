package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/modules/example/domain"
	"github.com/mini-base/mini-base/internal/modules/example/service"
)

// fakeRepo is an in-memory GreetingRepository.
type fakeRepo struct {
	saved     []domain.Greeting
	lastLimit int
	err       error
}

func (f *fakeRepo) Create(_ context.Context, g domain.Greeting) (domain.Greeting, error) {
	if f.err != nil {
		return domain.Greeting{}, f.err
	}
	g.ID = int64(len(f.saved) + 1)
	g.CreatedAt = time.Unix(0, 0)
	f.saved = append(f.saved, g)
	return g, nil
}

func (f *fakeRepo) List(_ context.Context, limit int) ([]domain.Greeting, error) {
	f.lastLimit = limit
	return f.saved, f.err
}

type fakeEvents struct{ names []string }

func (f *fakeEvents) Publish(_ context.Context, name string, _ any) { f.names = append(f.names, name) }

func TestCreateStoresAndPublishes(t *testing.T) {
	t.Parallel()

	repo, events := &fakeRepo{}, &fakeEvents{}
	svc := service.New(repo, events)

	g, err := svc.Create(context.Background(), " hi ")
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != 1 || g.Message != "hi" {
		t.Fatalf("got %+v", g)
	}
	if len(events.names) != 1 || events.names[0] != service.EventGreetingCreated {
		t.Fatalf("events = %v", events.names)
	}
}

func TestCreateInvalidDoesNotTouchRepoOrPublish(t *testing.T) {
	t.Parallel()

	repo, events := &fakeRepo{}, &fakeEvents{}
	_, err := service.New(repo, events).Create(context.Background(), "")
	if !errors.Is(err, domain.ErrMessageEmpty) {
		t.Fatalf("err = %v", err)
	}
	if len(repo.saved) != 0 || len(events.names) != 0 {
		t.Fatal("side effects on invalid input")
	}
}

func TestCreateRepoFailureIsWrappedAndNotPublished(t *testing.T) {
	t.Parallel()

	boom := errors.New("db down")
	events := &fakeEvents{}
	_, err := service.New(&fakeRepo{err: boom}, events).Create(context.Background(), "hi")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if len(events.names) != 0 {
		t.Fatal("published despite failure")
	}
}

func TestListClampsLimit(t *testing.T) {
	t.Parallel()

	for in, want := range map[int]int{0: 20, -5: 1, 50: 50, 1000: 100} {
		repo := &fakeRepo{}
		if _, err := service.New(repo, &fakeEvents{}).List(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if repo.lastLimit != want {
			t.Errorf("limit %d → %d, want %d", in, repo.lastLimit, want)
		}
	}
}
