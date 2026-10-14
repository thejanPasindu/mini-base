// Package port declares the interfaces the example service depends on.
// Adapters implement them; tests use hand-written fakes.
package port

import (
	"context"

	"github.com/mini-base/mini-base/internal/modules/example/domain"
)

// GreetingRepository persists greetings.
type GreetingRepository interface {
	Create(ctx context.Context, g domain.Greeting) (domain.Greeting, error)
	List(ctx context.Context, limit int) ([]domain.Greeting, error)
}

// EventPublisher is the subset of the kernel event bus the service needs.
type EventPublisher interface {
	Publish(ctx context.Context, name string, payload any)
}
