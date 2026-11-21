package postgres

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5"
)

// MapPgError exposes mapPgError to the external tests, so they can feed it real PostgreSQL errors
// (taken from real constraint violations) instead of fabricated ones.
var MapPgError = mapPgError

// StatementTracer records every statement the pool sends (pgx.QueryTracer), so a test can count the
// statements and transactions of one repository call. Test code only.
type StatementTracer struct {
	mu   sync.Mutex
	sqls []string
}

func (s *StatementTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	s.sqls = append(s.sqls, d.SQL)
	s.mu.Unlock()
	return ctx
}

func (s *StatementTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Reset forgets the recorded statements; SQL returns a copy of them.
func (s *StatementTracer) Reset() { s.mu.Lock(); s.sqls = nil; s.mu.Unlock() }
func (s *StatementTracer) SQL() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sqls...)
}
