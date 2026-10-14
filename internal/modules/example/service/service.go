// Package service contains the example module's use cases.
package service

import (
	"context"
	"fmt"

	"github.com/mini-base/mini-base/internal/modules/example/domain"
	"github.com/mini-base/mini-base/internal/modules/example/port"
)

// EventGreetingCreated is published after a greeting is stored.
const EventGreetingCreated = "example.greeting.created"

// List limits.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

// Service implements the greeting use cases.
type Service struct {
	repo   port.GreetingRepository
	events port.EventPublisher
}

// New returns a Service.
func New(repo port.GreetingRepository, events port.EventPublisher) *Service {
	return &Service{repo: repo, events: events}
}

// Create validates and stores a greeting, then announces it.
func (s *Service) Create(ctx context.Context, message string) (domain.Greeting, error) {
	g, err := domain.NewGreeting(message)
	if err != nil {
		return domain.Greeting{}, err
	}
	saved, err := s.repo.Create(ctx, g)
	if err != nil {
		return domain.Greeting{}, fmt.Errorf("create greeting: %w", err)
	}
	s.events.Publish(ctx, EventGreetingCreated, saved)
	return saved, nil
}

// List returns the newest greetings. limit is clamped to [1, MaxListLimit];
// zero means DefaultListLimit.
func (s *Service) List(ctx context.Context, limit int) ([]domain.Greeting, error) {
	switch {
	case limit == 0:
		limit = DefaultListLimit
	case limit < 1:
		limit = 1
	case limit > MaxListLimit:
		limit = MaxListLimit
	}
	out, err := s.repo.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list greetings: %w", err)
	}
	return out, nil
}
