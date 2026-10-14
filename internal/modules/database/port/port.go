// Package port declares what the database service needs from Postgres.
package port

import (
	"context"

	"github.com/mini-base/mini-base/internal/modules/database/domain"
	"github.com/mini-base/mini-base/pkg/ports"
)

// SchemaLoader reads the exposed relations from the catalog.
type SchemaLoader interface {
	Load(ctx context.Context, schemas []string) (*domain.Schema, error)
}

// Executor runs a statement as the principal: inside a transaction with the
// matching Postgres role and request.jwt.claims set, so RLS applies.
type Executor interface {
	// Query runs a statement with Returns=true and yields its JSON text.
	Query(ctx context.Context, p ports.Principal, st domain.Statement) ([]byte, error)
	// Exec runs a statement with Returns=false and yields rows affected.
	Exec(ctx context.Context, p ports.Principal, st domain.Statement) (int64, error)
}
