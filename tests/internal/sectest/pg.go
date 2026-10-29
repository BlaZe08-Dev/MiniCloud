// Package sectest holds the shared harness of the P4-4e security suites: per-test PostgreSQL schema
// isolation, a TCP fault proxy, a runner for real binaries, an in-process coordinator rig with a
// memory or PostgreSQL backend, and invariant checks that read the stored state.
package sectest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvTestDatabaseURL is the only variable that enables database-backed tests.
const EnvTestDatabaseURL = "MINICLOUD_TEST_DATABASE_URL"

// DatabaseURL returns the test database URL, or "" when database tests are disabled.
func DatabaseURL() string { return os.Getenv(EnvTestDatabaseURL) }

// guardDatabaseName refuses any database that is not a per-agent or shared test database name
// ending in _test, and refuses the shared minicloud_test outright (P4-4e rule).
func guardDatabaseName(t testing.TB, name string) {
	t.Helper()
	if !strings.HasSuffix(name, "_test") {
		t.Fatalf("SAFETY VIOLATION: refusing to run against database %q; name must end in '_test'", name)
	}
	if name == "minicloud_test" {
		t.Fatalf("SAFETY VIOLATION: refusing to run against the shared database %q; use minicloud_<agent>_test", name)
	}
}

// PGSchema is one isolated schema with all migrations applied in the test database.
type PGSchema struct {
	Name      string
	BaseURL   string // the test database URL, host and port as configured
	SchemaURL string // BaseURL plus search_path=<schema>
	Pool      *pgxpool.Pool
}

// NewPGSchema creates a random schema in the test database, migrates it, opens a pool on it and
// drops the schema on cleanup. It skips when MINICLOUD_TEST_DATABASE_URL is unset.
func NewPGSchema(t testing.TB) *PGSchema {
	t.Helper()
	base := DatabaseURL()
	if base == "" {
		t.Skip("skipping database test: " + EnvTestDatabaseURL + " is unset")
	}
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvTestDatabaseURL, err)
	}
	guardDatabaseName(t, cfg.ConnConfig.Database)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer admin.Close(ctx)

	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random schema name: %v", err)
	}
	name := "test_sec_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	drop := func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dcancel()
		if c, err := pgx.Connect(dctx, base); err == nil {
			_, _ = c.Exec(dctx, "DROP SCHEMA IF EXISTS "+name+" CASCADE")
			_ = c.Close(dctx)
		}
	}
	if _, err := admin.Exec(ctx, "SET search_path TO "+name); err != nil {
		drop()
		t.Fatalf("set search_path: %v", err)
	}
	if _, err := postgres.NewMigrator().Migrate(ctx, admin); err != nil {
		drop()
		t.Fatalf("migrate: %v", err)
	}

	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	s := &PGSchema{Name: name, BaseURL: base, SchemaURL: base + sep + "search_path=" + name}
	s.Pool, err = s.OpenPool(ctx, s.SchemaURL, 25)
	if err != nil {
		drop()
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() {
		s.Pool.Close()
		drop()
	})
	return s
}

// OpenPool opens a pool on url (which must carry the schema search_path).
func (s *PGSchema) OpenPool(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	return pgxpool.NewWithConfig(ctx, cfg)
}

// URLVia returns SchemaURL with the host and port replaced by addr (a fault proxy address).
func (s *PGSchema) URLVia(addr string) string {
	u, err := url.Parse(s.SchemaURL)
	if err != nil {
		panic(err)
	}
	u.Host = addr
	return u.String()
}

// UpstreamAddr is the host:port of the real test database, for the fault proxy to forward to.
func (s *PGSchema) UpstreamAddr() string {
	u, err := url.Parse(s.BaseURL)
	if err != nil {
		panic(err)
	}
	h := u.Host
	if _, _, err := net.SplitHostPort(h); err != nil {
		h += ":5432"
	}
	return h
}

// Exec runs a statement against the schema as the test (a test-side action, never production code).
func (s *PGSchema) Exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// Count runs a single-integer query against the schema.
func (s *PGSchema) Count(t testing.TB, sql string, args ...any) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var n int
	if err := s.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

// Redacted returns a URL with the credentials replaced, safe to print.
func Redacted(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable>"
	}
	if u.User != nil {
		u.User = url.UserPassword("***", "***")
	}
	return fmt.Sprint(u)
}
