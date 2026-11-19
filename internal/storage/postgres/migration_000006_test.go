package postgres_test

import (
	"context"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// All tests in this file build their schema with setupTestSchema, which refuses any database whose
// name does not end in "_test" (SEC-023).

const migration000006Name = "000006_worker_executes_concurrently.sql"

// TestMigrator_Migration000006_FreshSchema applies every embedded migration to an empty schema and
// checks the ADR-0020 Section 2.6 column: workers.executes_concurrently, boolean, NOT NULL, default
// false. It pins the migration list: 000006 is the sixth and last embedded migration.
func TestMigrator_Migration000006_FreshSchema(t *testing.T) {
	conn, schema := setupTestSchema(t)
	ctx := context.Background()

	m := postgres.NewMigrator()
	all, err := m.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations failed: %v", err)
	}
	if len(all) != 6 || all[5].Name != migration000006Name {
		t.Fatalf("expected the sixth and last embedded migration to be %s, got %d migrations (last %q)", migration000006Name, len(all), all[len(all)-1].Name)
	}
	if n, err := m.Migrate(ctx, conn); err != nil || n != 6 {
		t.Fatalf("Migrate: applied=%d err=%v", n, err)
	}

	var typ, nullable, def string
	if err := conn.QueryRow(ctx, `
		SELECT data_type, is_nullable, COALESCE(column_default, '') FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'workers' AND column_name = 'executes_concurrently'`,
		schema).Scan(&typ, &nullable, &def); err != nil {
		t.Fatalf("column workers.executes_concurrently: %v", err)
	}
	if typ != "boolean" || nullable != "NO" || def != "false" {
		t.Fatalf("workers.executes_concurrently: type=%q nullable=%q default=%q, want boolean NO false", typ, nullable, def)
	}

	// A row inserted without the column reads false (the undeclared, one-slot worker).
	mustExec(t, conn, `INSERT INTO workers (id, version, total_slots, status, supported_tasks) VALUES ('w_new', 'v0', 4, 'HEALTHY', ARRAY['echo'])`)
	var got bool
	if err := conn.QueryRow(ctx, `SELECT executes_concurrently FROM workers WHERE id = 'w_new'`).Scan(&got); err != nil || got {
		t.Fatalf("executes_concurrently of a row inserted without it = %v (err=%v), want false", got, err)
	}
}

// TestMigrator_Migration000006_UpgradeFrom000005_WithExistingRows applies 000006 to a database at
// 000005 that already holds workers, and proves that every existing worker reads
// executes_concurrently = false (it keeps one assignable slot until it re-registers) and that no
// row count changes.
func TestMigrator_Migration000006_UpgradeFrom000005_WithExistingRows(t *testing.T) {
	conn, _ := setupTestSchema(t)
	ctx := context.Background()

	m5, all := migrationsUpTo(t, 5)
	if n, err := m5.Migrate(ctx, conn); err != nil || n != 5 {
		t.Fatalf("migrating to 000005: applied=%d err=%v", n, err)
	}
	mustExec(t, conn, `INSERT INTO workers (id, version, total_slots, status, supported_tasks) VALUES ('w_a', 'v0.1.0', 4, 'HEALTHY', ARRAY['echo']), ('w_b', 'v0.1.0', 1, 'UNHEALTHY', ARRAY['sleep'])`)
	mustExec(t, conn, `INSERT INTO jobs (id, task_type, payload, state) VALUES ('j_q', 'echo', '\x00', 'QUEUED')`)

	if len(all) < 6 || all[5].Name != migration000006Name {
		t.Fatalf("expected %s as the sixth migration", migration000006Name)
	}
	m6, _ := migrationsUpTo(t, 6)
	if n, err := m6.Migrate(ctx, conn); err != nil || n != 1 {
		t.Fatalf("upgrading 000005 -> 000006: applied=%d err=%v", n, err)
	}

	var workers, jobs, declared int
	if err := conn.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM workers), (SELECT COUNT(*) FROM jobs), (SELECT COUNT(*) FROM workers WHERE executes_concurrently)`).Scan(&workers, &jobs, &declared); err != nil {
		t.Fatal(err)
	}
	if workers != 2 || jobs != 1 {
		t.Fatalf("row counts changed: workers=%d jobs=%d, want 2 and 1", workers, jobs)
	}
	if declared != 0 {
		t.Fatalf("%d existing workers read executes_concurrently = true after the upgrade, want 0", declared)
	}
}
