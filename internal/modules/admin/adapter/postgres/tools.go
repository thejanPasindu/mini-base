package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validUUID lets ids from URLs be rejected as "not found" instead of
// reaching Postgres as malformed uuids.
func validUUID(id string) bool { return uuidRE.MatchString(id) }

// AuditLog implements port.AuditStore.
type AuditLog struct{ db *pgxpool.Pool }

// NewAuditLog returns an AuditLog.
func NewAuditLog(db *pgxpool.Pool) *AuditLog { return &AuditLog{db: db} }

// Append implements port.AuditStore.
func (s *AuditLog) Append(ctx context.Context, e domain.AuditEntry) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO admin.audit_log (at, account_id, actor, ip, action, target, detail)
		 VALUES ($1, $2::uuid, $3, nullif($4, ''), $5, $6, $7)`,
		e.At, e.AccountID, e.Actor, e.IP, e.Action, e.Target, e.Detail)
	if err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	return nil
}

// List implements port.AuditStore. Action filters by exact match, or by
// group when it ends with a dot ("schema.").
func (s *AuditLog) List(ctx context.Context, q domain.AuditQuery) ([]domain.AuditEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, at, account_id::text, actor, coalesce(ip, ''), action, target, detail
		FROM admin.audit_log
		WHERE ($1 = '' OR action = $1 OR (right($1, 1) = '.' AND starts_with(action, $1)))
		  AND ($2 = '' OR actor = $2)
		  AND ($3 = 0 OR id < $3)
		ORDER BY id DESC
		LIMIT $4`, q.Action, q.Actor, q.Before, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("list audit log: %w", err)
	}
	defer rows.Close()
	out := []domain.AuditEntry{}
	for rows.Next() {
		var e domain.AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.AccountID, &e.Actor, &e.IP, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SQLHistory implements port.AuditStore.
func (s *AuditLog) SQLHistory(ctx context.Context, accountID string, limit int) ([]domain.HistoryEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, at, detail->>'query', coalesce((detail->>'ok')::boolean, false), coalesce((detail->>'duration_ms')::bigint, 0)
		FROM admin.audit_log
		WHERE account_id = $1::uuid AND action = 'sql.run'
		ORDER BY id DESC
		LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("sql history: %w", err)
	}
	defer rows.Close()
	out := []domain.HistoryEntry{}
	for rows.Next() {
		var h domain.HistoryEntry
		var q *string
		if err := rows.Scan(&h.ID, &h.At, &q, &h.OK, &h.DurationMS); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		if q != nil {
			h.Query = *q
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// DeleteBefore implements port.AuditStore.
func (s *AuditLog) DeleteBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin.audit_log WHERE at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("purge audit log: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Snippets implements port.SnippetStore.
type Snippets struct{ db *pgxpool.Pool }

// NewSnippets returns a Snippets store.
func NewSnippets(db *pgxpool.Pool) *Snippets { return &Snippets{db: db} }

const snippetCols = `id::text, name, query, created_at, updated_at`

func scanSnippet(row pgx.Row) (domain.Snippet, error) {
	var sn domain.Snippet
	err := row.Scan(&sn.ID, &sn.Name, &sn.Query, &sn.CreatedAt, &sn.UpdatedAt)
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Snippet{}, domain.ErrSnippetNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return domain.Snippet{}, domain.ErrSnippetExists
	case err != nil:
		return domain.Snippet{}, fmt.Errorf("snippet: %w", err)
	}
	return sn, nil
}

// List implements port.SnippetStore.
func (s *Snippets) List(ctx context.Context, accountID string) ([]domain.Snippet, error) {
	rows, err := s.db.Query(ctx, `SELECT `+snippetCols+` FROM admin.snippets WHERE account_id = $1::uuid ORDER BY lower(name)`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list snippets: %w", err)
	}
	defer rows.Close()
	out := []domain.Snippet{}
	for rows.Next() {
		sn, err := scanSnippet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

// Create implements port.SnippetStore.
func (s *Snippets) Create(ctx context.Context, accountID, name, query string) (domain.Snippet, error) {
	return scanSnippet(s.db.QueryRow(ctx,
		`INSERT INTO admin.snippets (account_id, name, query) VALUES ($1::uuid, $2, $3) RETURNING `+snippetCols,
		accountID, name, query))
}

// Update implements port.SnippetStore.
func (s *Snippets) Update(ctx context.Context, accountID, id, name, query string) (domain.Snippet, error) {
	if !validUUID(id) {
		return domain.Snippet{}, domain.ErrSnippetNotFound
	}
	return scanSnippet(s.db.QueryRow(ctx,
		`UPDATE admin.snippets SET name = $3, query = $4, updated_at = now()
		 WHERE id = $2::uuid AND account_id = $1::uuid RETURNING `+snippetCols,
		accountID, id, name, query))
}

// Delete implements port.SnippetStore.
func (s *Snippets) Delete(ctx context.Context, accountID, id string) error {
	if !validUUID(id) {
		return domain.ErrSnippetNotFound
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM admin.snippets WHERE id = $2::uuid AND account_id = $1::uuid`, accountID, id)
	if err != nil {
		return fmt.Errorf("delete snippet: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrSnippetNotFound
	}
	return nil
}
