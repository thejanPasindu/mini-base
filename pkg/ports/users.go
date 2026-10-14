package ports

import (
	"context"
	"time"
)

// UserSummary is an application user as seen by operators.
type UserSummary struct {
	ID             string     `json:"id"`
	Email          string     `json:"email"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSignInAt   *time.Time `json:"last_sign_in_at"`
	ActiveSessions int        `json:"active_sessions"`
}

// UserQuery filters and pages ListUsers.
type UserQuery struct {
	Search string // case-insensitive substring of the email
	Limit  int
	Offset int
}

// UserDirectory lets operator tools (the admin dashboard) manage application
// users without touching the auth module's tables. The auth module provides
// it under UserDirectoryService.
type UserDirectory interface {
	ListUsers(ctx context.Context, q UserQuery) (users []UserSummary, total int, err error)
	CreateUser(ctx context.Context, email, password string) (UserSummary, error)
	DeleteUser(ctx context.Context, id string) error
	// RevokeSessions signs the user out everywhere and returns how many
	// sessions were revoked.
	RevokeSessions(ctx context.Context, id string) (int, error)
}

// UserDirectoryService is the kernel.Services name of the UserDirectory.
const UserDirectoryService = "auth.user_directory"

// EventSchemaChanged is published after an operator changes the database
// schema (DDL, RLS settings). Modules caching catalog data reload on it.
const EventSchemaChanged = "db.schema.changed"
