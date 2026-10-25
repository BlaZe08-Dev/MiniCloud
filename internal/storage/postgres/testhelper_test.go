package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setupTestPool creates an isolated schema in minicloud_test, runs all migrations,
// and returns a pgxpool.Pool configured strictly with that schema in search_path.
func setupTestPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()

	testURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("skipping postgres test: MINICLOUD_TEST_DATABASE_URL is unset")
	}

	cfg, err := pgxpool.ParseConfig(testURL)
	if err != nil {
		t.Fatalf("failed to parse MINICLOUD_TEST_DATABASE_URL: %v", err)
	}

	// Hard safety guard: refuse execution unless database name ends in "_test"
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatalf("SAFETY VIOLATION: refusing to run test against database %q; name must end in '_test'", cfg.ConnConfig.Database)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	adminConn, err := pgx.Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("failed to connect admin conn to test database: %v", err)
	}
	defer adminConn.Close(ctx)

	// Generate unique random schema name for test isolation
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("failed to generate random schema name: %v", err)
	}
	schemaName := fmt.Sprintf("test_repo_%s", hex.EncodeToString(b))

	// Create isolated schema
	if _, err := adminConn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s;", schemaName)); err != nil {
		t.Fatalf("failed to create schema %s: %v", schemaName, err)
	}

	// Configure search_path on adminConn to run migrations
	if _, err := adminConn.Exec(ctx, fmt.Sprintf("SET search_path TO %s;", schemaName)); err != nil {
		_, _ = adminConn.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE;", schemaName))
		t.Fatalf("failed to set search_path: %v", err)
	}

	// Run all forward-only migrations
	m := postgres.NewMigrator()
	if _, err := m.Migrate(ctx, adminConn); err != nil {
		_, _ = adminConn.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE;", schemaName))
		t.Fatalf("failed to run migrations in test schema: %v", err)
	}

	// Configure pool search_path to the isolated test schema
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	cfg.MaxConns = 60

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		_, _ = adminConn.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE;", schemaName))
		t.Fatalf("failed to create pgxpool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupConn, err := pgx.Connect(cleanupCtx, testURL)
		if err == nil {
			_, _ = cleanupConn.Exec(cleanupCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE;", schemaName))
			_ = cleanupConn.Close(cleanupCtx)
		}
	})

	return pool, schemaName
}
