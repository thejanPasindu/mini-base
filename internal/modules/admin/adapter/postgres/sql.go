package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mini-base/mini-base/internal/modules/admin/domain"
)

// SQLRunner implements port.SQLRunner.
type SQLRunner struct {
	db      *pgxpool.Pool
	timeout time.Duration
}

// NewSQLRunner returns a runner with a per-script statement timeout.
func NewSQLRunner(db *pgxpool.Pool, timeout time.Duration) *SQLRunner {
	return &SQLRunner{db: db, timeout: timeout}
}

// Run executes a script with the simple query protocol, so it may contain
// several statements, and collects every statement's result.
//
// The connection is taken out of the pool and closed afterwards: a script
// may SET ROLE, change search_path or leave a transaction open, and none of
// that may leak into connections that serve API requests.
func (r *SQLRunner) Run(ctx context.Context, sql string, maxRows int) (domain.SQLRun, error) {
	pooled, err := r.db.Acquire(ctx)
	if err != nil {
		return domain.SQLRun{}, fmt.Errorf("acquire connection: %w", err)
	}
	conn := pooled.Hijack()
	defer conn.Close(context.WithoutCancel(ctx))
	pg := conn.PgConn()

	timeout := fmt.Sprintf("SET statement_timeout = %d", r.timeout.Milliseconds())
	if err := pg.Exec(ctx, timeout).Close(); err != nil {
		return domain.SQLRun{}, fmt.Errorf("set timeout: %w", err)
	}

	start := time.Now()
	run := domain.SQLRun{Results: []domain.SQLResult{}}
	mrr := pg.Exec(ctx, sql)
	for mrr.NextResult() {
		rr := mrr.ResultReader()
		res := domain.SQLResult{Columns: []domain.SQLColumn{}, Rows: [][]*string{}}
		for _, fd := range rr.FieldDescriptions() {
			res.Columns = append(res.Columns, domain.SQLColumn{Name: fd.Name, Type: typeName(fd.DataTypeOID)})
		}
		for rr.NextRow() {
			if len(res.Rows) >= maxRows {
				res.Truncated = true
				continue // keep draining the result
			}
			vals := rr.Values()
			row := make([]*string, len(vals))
			for i, v := range vals {
				if v != nil {
					s := string(v) // copy: the buffer is reused
					row[i] = &s
				}
			}
			res.Rows = append(res.Rows, row)
		}
		tag, err := rr.Close()
		if err != nil {
			run.Error = sqlError(err)
			break
		}
		res.Command = tag.String()
		run.Results = append(run.Results, res)
	}
	if err := mrr.Close(); err != nil && run.Error == nil {
		run.Error = sqlError(err)
	}
	run.DurationMS = time.Since(start).Milliseconds()

	// Errors from the script are results for the operator, not failures of
	// the request — unless the connection itself broke.
	if run.Error != nil && run.Error.Code == "" && ctx.Err() != nil {
		return domain.SQLRun{}, ctx.Err()
	}
	return run, nil
}

func sqlError(err error) *domain.SQLError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return &domain.SQLError{
			Message: pgErr.Message, Detail: pgErr.Detail, Hint: pgErr.Hint,
			Code: pgErr.Code, Position: pgErr.Position,
		}
	}
	return &domain.SQLError{Message: err.Error()}
}

// typeName names common type OIDs for display; others show as their OID.
func typeName(oid uint32) string {
	switch oid {
	case 16:
		return "bool"
	case 17:
		return "bytea"
	case 20:
		return "int8"
	case 21:
		return "int2"
	case 23:
		return "int4"
	case 25:
		return "text"
	case 114:
		return "json"
	case 700:
		return "float4"
	case 701:
		return "float8"
	case 1043:
		return "varchar"
	case 1082:
		return "date"
	case 1114:
		return "timestamp"
	case 1184:
		return "timestamptz"
	case 1700:
		return "numeric"
	case 2950:
		return "uuid"
	case 3802:
		return "jsonb"
	default:
		return fmt.Sprintf("oid:%d", oid)
	}
}
