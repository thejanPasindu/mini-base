// Package postgres implements the example module's repository with pgx.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/modules/example/domain"
)

// Repository implements port.GreetingRepository.
type Repository struct {
	db *pgxpool.Pool
}

// NewRepository returns a Repository using db.
func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// row mirrors example.greetings for pgx struct scanning.
type row struct {
	ID        int64     `db:"id"`
	Message   string    `db:"message"`
	CreatedAt time.Time `db:"created_at"`
}

func (r row) toDomain() domain.Greeting {
	return domain.Greeting{ID: r.ID, Message: r.Message, CreatedAt: r.CreatedAt}
}

// Create inserts g and returns it with its generated ID and timestamp.
func (r *Repository) Create(ctx context.Context, g domain.Greeting) (domain.Greeting, error) {
	rows, err := r.db.Query(ctx,
		`INSERT INTO example.greetings (message) VALUES ($1)
		 RETURNING id, message, created_at`, g.Message)
	if err != nil {
		return domain.Greeting{}, fmt.Errorf("insert greeting: %w", err)
	}
	saved, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[row])
	if err != nil {
		return domain.Greeting{}, fmt.Errorf("insert greeting: %w", err)
	}
	return saved.toDomain(), nil
}

// List returns up to limit greetings, newest first.
func (r *Repository) List(ctx context.Context, limit int) ([]domain.Greeting, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, message, created_at FROM example.greetings
		 ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list greetings: %w", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowToStructByName[row])
	if err != nil {
		return nil, fmt.Errorf("list greetings: %w", err)
	}
	out := make([]domain.Greeting, len(found))
	for i, f := range found {
		out[i] = f.toDomain()
	}
	return out, nil
}
