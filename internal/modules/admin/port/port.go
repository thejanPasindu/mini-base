// Package port declares what the admin services need from the outside world.
package port

import (
	"context"
	"time"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
)

// AccountStore persists dashboard operators.
type AccountStore interface {
	// Create returns domain.ErrAccountExists for a duplicate email.
	Create(ctx context.Context, email, passwordHash string) (domain.Account, error)
	// FindByEmail returns domain.ErrAccountNotFound if absent.
	FindByEmail(ctx context.Context, email string) (domain.Account, string, error)
	// FindByID returns domain.ErrAccountNotFound if absent.
	FindByID(ctx context.Context, id string) (domain.Account, string, error)
	List(ctx context.Context) ([]domain.Account, error)
	Count(ctx context.Context) (int, error)
	// Delete returns domain.ErrAccountNotFound if absent.
	Delete(ctx context.Context, email string) error
	// DeleteByID deletes an account unless it is the last one
	// (domain.ErrLastAccount), atomically. It returns the deleted account,
	// or domain.ErrAccountNotFound.
	DeleteByID(ctx context.Context, id string) (domain.Account, error)
	SetPassword(ctx context.Context, id, passwordHash string) error
	RecordSignIn(ctx context.Context, id string, at time.Time) error
}

// SessionStore persists dashboard sessions by token hash.
type SessionStore interface {
	Create(ctx context.Context, tokenHash []byte, accountID string, expiresAt time.Time, c domain.ClientInfo) error
	// Lookup returns the account owning an unexpired session and marks it
	// as seen; domain.ErrNotSignedIn otherwise.
	Lookup(ctx context.Context, tokenHash []byte, now time.Time) (domain.Account, error)
	Delete(ctx context.Context, tokenHash []byte) error
	DeleteForAccount(ctx context.Context, accountID string) error
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

// PasswordHasher hashes and checks passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (bool, error)
}

// DatabaseStats summarises the database for the overview page.
type DatabaseStats struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	SizeBytes int64  `json:"size_bytes"`
}

// Catalog reads and changes schema metadata.
type Catalog interface {
	ListTables(ctx context.Context, hidden []string) ([]domain.TableSummary, error)
	// Schemas lists schemas other than hidden ones and Postgres's own.
	Schemas(ctx context.Context, hidden []string) ([]string, error)
	// Table returns domain.ErrTableNotFound if absent or hidden.
	Table(ctx context.Context, schema, name string, hidden []string) (*domain.Table, error)
	SetRLS(ctx context.Context, t *domain.Table, enabled bool) error
	// ExecDDL runs statements in one transaction with
	// standard_conforming_strings on.
	ExecDDL(ctx context.Context, statements []string) error
	Stats(ctx context.Context) (DatabaseStats, error)
}

// DataStore runs table-editor statements as the connection user.
type DataStore interface {
	// QueryText runs a statement that yields one text value (JSON).
	// It returns ok=false when the statement produced no row.
	QueryText(ctx context.Context, st domain.Statement) (value string, ok bool, err error)
	// Count runs a statement yielding one bigint.
	Count(ctx context.Context, st domain.Statement) (int64, error)
	// Export streams the rows of st (text columns, nil for NULL) to emit.
	Export(ctx context.Context, st domain.Statement, emit func(row []*string) error) (int64, error)
	// Import runs sql once per batch ($1 = batch) in one transaction and
	// returns the rows inserted. On failure, failedBatch is the index of
	// the batch that failed.
	Import(ctx context.Context, sql string, batches []string) (rows int64, failedBatch int, err error)
}

// AuditStore persists the audit log.
type AuditStore interface {
	Append(ctx context.Context, e domain.AuditEntry) error
	List(ctx context.Context, q domain.AuditQuery) ([]domain.AuditEntry, error)
	// SQLHistory returns an account's latest SQL editor runs.
	SQLHistory(ctx context.Context, accountID string, limit int) ([]domain.HistoryEntry, error)
	DeleteBefore(ctx context.Context, t time.Time) (int64, error)
}

// SnippetStore persists saved queries per admin. Methods return
// domain.ErrSnippetNotFound for an unknown id (or another admin's) and
// domain.ErrSnippetExists for a duplicate name.
type SnippetStore interface {
	List(ctx context.Context, accountID string) ([]domain.Snippet, error)
	Create(ctx context.Context, accountID, name, query string) (domain.Snippet, error)
	Update(ctx context.Context, accountID, id, name, query string) (domain.Snippet, error)
	Delete(ctx context.Context, accountID, id string) error
}

// SQLRunner executes free-form SQL from the SQL editor on a dedicated
// connection that is discarded afterwards.
type SQLRunner interface {
	Run(ctx context.Context, sql string, maxRows int) (domain.SQLRun, error)
}

// EventPublisher is the subset of the event bus used here.
type EventPublisher interface {
	Publish(ctx context.Context, name string, payload any)
}
