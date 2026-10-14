package admin

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	adminpg "github.com/mini-base/mini-base/internal/modules/admin/adapter/postgres"
	"github.com/mini-base/mini-base/internal/modules/admin/domain"
	"github.com/mini-base/mini-base/internal/modules/admin/service"
	"github.com/mini-base/mini-base/internal/platform/password"
)

// CLI manages admin accounts from the command line (`minibase admin ...`).
type CLI struct{ accounts *service.Accounts }

// NewCLI returns a CLI using db, which must already be migrated.
func NewCLI(db *pgxpool.Pool) (*CLI, error) {
	a, err := service.NewAccounts(adminpg.NewAccounts(db), adminpg.NewSessions(db),
		password.NewArgon2id(password.DefaultParams, 1), time.Hour)
	if err != nil {
		return nil, err
	}
	return &CLI{accounts: a}, nil
}

// Create adds an admin.
func (c *CLI) Create(ctx context.Context, email, pw string) (domain.Account, error) {
	return c.accounts.Create(ctx, email, pw)
}

// List returns all admins.
func (c *CLI) List(ctx context.Context) ([]domain.Account, error) { return c.accounts.List(ctx) }

// Delete removes an admin.
func (c *CLI) Delete(ctx context.Context, email string) error { return c.accounts.Delete(ctx, email) }

// ResetPassword sets a new password and ends the admin's sessions.
func (c *CLI) ResetPassword(ctx context.Context, email, pw string) error {
	return c.accounts.ResetPassword(ctx, email, pw)
}

// GeneratePassword returns a random password suitable for an admin.
func GeneratePassword() (string, error) { return service.GeneratePassword() }
