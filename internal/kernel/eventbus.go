package kernel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Event is a notification published by a module, e.g. "auth.user.created".
// Names are "<module>.<entity>.<past-tense-verb>".
type Event struct {
	Name       string
	Payload    any
	OccurredAt time.Time
}

// Handler processes an event. It runs on the subscription's own goroutine and
// should not block for long; a slow handler only delays its own subscription.
type Handler func(ctx context.Context, e Event)

// EventBus is the port modules use for cross-module notifications.
type EventBus interface {
	// Publish never blocks. If a subscriber's buffer is full the event is
	// dropped for that subscriber and a warning is logged.
	Publish(ctx context.Context, name string, payload any)

	// Subscribe registers h for events matching pattern: an exact name,
	// a prefix wildcard such as "auth.*", or "*" for everything.
	Subscribe(pattern string, h Handler) (unsubscribe func())
}

// InProcBus is the in-memory EventBus. Delivery is at-most-once and
// in-order per subscriber. Durable delivery (e.g. webhooks) must persist events
// itself; a NATS/Redis implementation can replace this for multi-node setups.
type InProcBus struct {
	log     *slog.Logger
	bufSize int

	ctx    context.Context //nolint:containedctx // lifetime of handler invocations
	cancel context.CancelFunc

	mu     sync.RWMutex
	subs   map[uint64]*subscription
	nextID uint64
	closed bool
	wg     sync.WaitGroup
}

type subscription struct {
	pattern string
	ch      chan Event
	once    sync.Once
}

func (s *subscription) close() { s.once.Do(func() { close(s.ch) }) }

// NewInProcBus returns a bus whose subscribers buffer up to bufSize events.
func NewInProcBus(log *slog.Logger, bufSize int) *InProcBus {
	if bufSize <= 0 {
		bufSize = 256
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &InProcBus{
		log:     log,
		bufSize: bufSize,
		ctx:     ctx,
		cancel:  cancel,
		subs:    make(map[uint64]*subscription),
	}
}

// Publish implements EventBus.
func (b *InProcBus) Publish(ctx context.Context, name string, payload any) {
	e := Event{Name: name, Payload: payload, OccurredAt: time.Now().UTC()}

	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		b.log.DebugContext(ctx, "event published after bus closed", slog.String("event", name))
		return
	}
	for _, s := range b.subs {
		if !matchEvent(s.pattern, name) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			b.log.WarnContext(ctx, "event dropped: subscriber buffer full",
				slog.String("event", name), slog.String("pattern", s.pattern))
		}
	}
}

// Subscribe implements EventBus.
func (b *InProcBus) Subscribe(pattern string, h Handler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return func() {}
	}

	id := b.nextID
	b.nextID++
	s := &subscription{pattern: pattern, ch: make(chan Event, b.bufSize)}
	b.subs[id] = s

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for e := range s.ch {
			b.dispatch(h, s.pattern, e)
		}
	}()

	return func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
		s.close()
	}
}

func (b *InProcBus) dispatch(h Handler, pattern string, e Event) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("event handler panicked",
				slog.String("event", e.Name), slog.String("pattern", pattern), slog.Any("panic", r))
		}
	}()
	h(b.ctx, e)
}

// Close stops accepting events, lets subscribers drain their buffers and
// waits for them until ctx expires, after which handler contexts are cancelled.
func (b *InProcBus) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	for id, s := range b.subs {
		s.close()
		delete(b.subs, id)
	}
	b.mu.Unlock()

	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()

	select {
	case <-done:
		b.cancel()
		return nil
	case <-ctx.Done():
		b.cancel()
		return fmt.Errorf("event bus: subscribers did not drain: %w", ctx.Err())
	}
}

func matchEvent(pattern, name string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, ".*"):
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	default:
		return pattern == name
	}
}
