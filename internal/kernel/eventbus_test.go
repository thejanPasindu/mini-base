package kernel_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mini-base/mini-base/internal/kernel"
)

func newBus(t *testing.T, buf int) *kernel.InProcBus {
	t.Helper()
	b := kernel.NewInProcBus(slog.New(slog.NewTextHandler(io.Discard, nil)), buf)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	return b
}

type collector struct {
	mu    sync.Mutex
	names []string
	got   chan struct{}
}

func newCollector() *collector { return &collector{got: make(chan struct{}, 100)} }

func (c *collector) handle(_ context.Context, e kernel.Event) {
	c.mu.Lock()
	c.names = append(c.names, e.Name)
	c.mu.Unlock()
	c.got <- struct{}{}
}

func (c *collector) wait(t *testing.T, n int) []string {
	t.Helper()
	for range n {
		select {
		case <-c.got:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %d events", n)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.names...)
}

func TestBusPatternMatching(t *testing.T) {
	t.Parallel()

	b := newBus(t, 10)
	exact, prefix, all := newCollector(), newCollector(), newCollector()
	b.Subscribe("auth.user.created", exact.handle)
	b.Subscribe("auth.*", prefix.handle)
	b.Subscribe("*", all.handle)

	ctx := context.Background()
	b.Publish(ctx, "auth.user.created", nil)
	b.Publish(ctx, "auth.session.created", nil)
	b.Publish(ctx, "storage.object.created", nil)
	b.Publish(ctx, "authx.thing", nil) // "auth.*" must not match "authx"

	if got := exact.wait(t, 1); len(got) != 1 {
		t.Fatalf("exact got %v", got)
	}
	if got := prefix.wait(t, 2); len(got) != 2 {
		t.Fatalf("prefix got %v", got)
	}
	if got := all.wait(t, 4); len(got) != 4 {
		t.Fatalf("all got %v", got)
	}
}

func TestBusPreservesOrderPerSubscriber(t *testing.T) {
	t.Parallel()

	b := newBus(t, 100)
	c := newCollector()
	b.Subscribe("*", c.handle)
	want := []string{"a", "b", "c", "d"}
	for _, n := range want {
		b.Publish(context.Background(), n, nil)
	}
	got := c.wait(t, len(want))
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v", got)
		}
	}
}

func TestBusUnsubscribe(t *testing.T) {
	t.Parallel()

	b := newBus(t, 10)
	c := newCollector()
	unsubscribe := b.Subscribe("*", c.handle)
	unsubscribe()
	unsubscribe() // idempotent
	b.Publish(context.Background(), "x", nil)

	select {
	case <-c.got:
		t.Fatal("received event after unsubscribe")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBusDropsWhenSubscriberIsFullInsteadOfBlocking(t *testing.T) {
	t.Parallel()

	b := newBus(t, 1)
	release := make(chan struct{})
	b.Subscribe("*", func(context.Context, kernel.Event) { <-release })

	done := make(chan struct{})
	go func() {
		for range 50 {
			b.Publish(context.Background(), "x", nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	close(release)
}

func TestBusSurvivesHandlerPanic(t *testing.T) {
	t.Parallel()

	b := newBus(t, 10)
	c := newCollector()
	b.Subscribe("*", func(_ context.Context, e kernel.Event) {
		if e.Name == "bad" {
			panic("boom")
		}
		c.handle(context.Background(), e)
	})
	b.Publish(context.Background(), "bad", nil)
	b.Publish(context.Background(), "good", nil)
	if got := c.wait(t, 1); got[0] != "good" {
		t.Fatalf("got %v", got)
	}
}

func TestBusCloseDrainsAndIgnoresLatePublishes(t *testing.T) {
	t.Parallel()

	b := kernel.NewInProcBus(slog.New(slog.NewTextHandler(io.Discard, nil)), 10)
	var mu sync.Mutex
	count := 0
	b.Subscribe("*", func(context.Context, kernel.Event) {
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		count++
		mu.Unlock()
	})
	for range 5 {
		b.Publish(context.Background(), "x", nil)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 5 {
		t.Fatalf("drained %d of 5 events", count)
	}
	b.Publish(context.Background(), "late", nil) // must not panic
}
