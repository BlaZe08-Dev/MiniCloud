package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// All tests in this file build their schema with setupTestSchema, which refuses any database whose
// name does not end in "_test" (SEC-023).

const migration000005Name = "000005_assignment_round_robin.sql"

// TestMigrator_Migration000005_FreshSchema applies every embedded migration to an empty schema and
// checks the ADR-0017 Section 5.3 objects: the jobs assignment columns, the three CHECKs (validated),
// workers.last_poll_at, assignment_cursor, the one-row assignment_pass_state and the three partial
// indexes. It also pins the migration list: 000005 is the fifth and last embedded migration.
func TestMigrator_Migration000005_FreshSchema(t *testing.T) {
	conn, schema := setupTestSchema(t)
	ctx := context.Background()

	m := postgres.NewMigrator()
	all, err := m.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations failed: %v", err)
	}
	if len(all) != 5 || all[4].Name != migration000005Name {
		t.Fatalf("expected the fifth and last embedded migration to be %s, got %d migrations (last %q)", migration000005Name, len(all), all[len(all)-1].Name)
	}
	if n, err := m.Migrate(ctx, conn); err != nil || n != 5 {
		t.Fatalf("Migrate: applied=%d err=%v", n, err)
	}

	type col struct{ table, name, typ, nullable string }
	for _, c := range []col{
		{"jobs", "assigned_worker_id", "text", "YES"},
		{"jobs", "assigned_at", "timestamp with time zone", "YES"},
		{"jobs", "assignment_expires_at", "timestamp with time zone", "YES"},
		{"jobs", "assignment_lapses", "integer", "NO"},
		{"jobs", "assignment_fallback_reason", "character varying", "YES"},
		{"workers", "last_poll_at", "timestamp with time zone", "YES"},
		{"assignment_cursor", "task_type", "character varying", "NO"},
		{"assignment_cursor", "last_worker_id", "text", "YES"},
		{"assignment_pass_state", "last_completed_at", "timestamp with time zone", "NO"},
	} {
		var typ, nullable string
		err := conn.QueryRow(ctx, `
			SELECT data_type, is_nullable FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
			schema, c.table, c.name).Scan(&typ, &nullable)
		if err != nil {
			t.Fatalf("column %s.%s: %v", c.table, c.name, err)
		}
		if typ != c.typ || nullable != c.nullable {
			t.Errorf("column %s.%s: type=%q nullable=%q, want %q %q", c.table, c.name, typ, nullable, c.typ, c.nullable)
		}
	}

	for _, con := range []string{"chk_jobs_assignment_shape", "chk_jobs_assignment_lapses", "chk_jobs_fallback_reason"} {
		var validated bool
		err := conn.QueryRow(ctx, `
			SELECT c.convalidated FROM pg_constraint c
			JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE n.nspname = $1 AND c.conname = $2`, schema, con).Scan(&validated)
		if err != nil {
			t.Fatalf("constraint %s missing: %v", con, err)
		}
		if !validated {
			t.Errorf("constraint %s is NOT VALID; the migration must VALIDATE it", con)
		}
	}

	for _, idx := range []string{"idx_jobs_dispatch_by_type", "idx_jobs_assigned_worker", "idx_jobs_assignment_expiry"} {
		var def string
		if err := conn.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND indexname = $2`, schema, idx).Scan(&def); err != nil {
			t.Fatalf("index %s missing: %v", idx, err)
		}
		if !strings.Contains(def, "WHERE") {
			t.Errorf("index %s must be partial: %s", idx, def)
		}
	}

	var rows int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM assignment_pass_state`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("assignment_pass_state rows = %d (err=%v), want exactly 1", rows, err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO assignment_pass_state (singleton) VALUES (FALSE)`); pgCode(err) != "23514" {
		t.Errorf("a second pass-state row (singleton=false): want check_violation 23514, got %v", err)
	}

	mustExec(t, conn, `INSERT INTO workers (id, version, total_slots, status, supported_tasks) VALUES ('w1', 'v', 1, 'HEALTHY', ARRAY['echo'])`)
	insJob := func(id, state string) {
		mustExec(t, conn, `INSERT INTO jobs (id, task_type, payload, state) VALUES ($1, 'echo', '\x00', $2)`, id, state)
	}

	t.Run("shape_check_allows_a_full_reservation_on_queued_and_retrying", func(t *testing.T) {
		for _, st := range []string{"QUEUED", "RETRYING"} {
			id := "j_ok_" + st
			insJob(id, st)
			if _, err := conn.Exec(ctx, `UPDATE jobs SET assigned_worker_id = 'w1', assigned_at = clock_timestamp(), assignment_expires_at = clock_timestamp() + interval '5 seconds' WHERE id = $1`, id); err != nil {
				t.Errorf("reservation on %s rejected: %v", st, err)
			}
		}
	})

	t.Run("shape_check_rejects_a_reservation_on_other_states", func(t *testing.T) {
		for _, st := range []string{"RUNNING", "CANCEL_REQUESTED", "CANCELLED", "SUCCEEDED", "FAILED"} {
			id := "j_bad_" + st
			insJob(id, st)
			_, err := conn.Exec(ctx, `UPDATE jobs SET assigned_worker_id = 'w1', assigned_at = clock_timestamp(), assignment_expires_at = clock_timestamp() + interval '5 seconds' WHERE id = $1`, id)
			if pgCode(err) != "23514" {
				t.Errorf("reservation on %s: want check_violation 23514, got %v", st, err)
			}
		}
	})

	t.Run("shape_check_rejects_partial_and_inverted_reservations", func(t *testing.T) {
		insJob("j_partial", "QUEUED")
		for name, set := range map[string]string{
			"worker_only":  `assigned_worker_id = 'w1'`,
			"no_expiry":    `assigned_worker_id = 'w1', assigned_at = clock_timestamp()`,
			"no_worker":    `assigned_at = clock_timestamp(), assignment_expires_at = clock_timestamp()`,
			"expiry_first": `assigned_worker_id = 'w1', assigned_at = clock_timestamp(), assignment_expires_at = clock_timestamp() - interval '1 second'`,
		} {
			if _, err := conn.Exec(ctx, `UPDATE jobs SET `+set+` WHERE id = 'j_partial'`); pgCode(err) != "23514" {
				t.Errorf("%s: want check_violation 23514, got %v", name, err)
			}
		}
	})

	t.Run("a_transition_to_running_that_keeps_the_reservation_is_rejected", func(t *testing.T) {
		if _, err := conn.Exec(ctx, `UPDATE jobs SET state = 'RUNNING' WHERE id = 'j_ok_QUEUED'`); pgCode(err) != "23514" {
			t.Fatalf("RUNNING with a reservation kept: want check_violation 23514, got %v", err)
		}
	})

	t.Run("lapses_and_fallback_reason_are_bounded", func(t *testing.T) {
		insJob("j_bounds", "QUEUED")
		if _, err := conn.Exec(ctx, `UPDATE jobs SET assignment_lapses = -1 WHERE id = 'j_bounds'`); pgCode(err) != "23514" {
			t.Errorf("negative lapses: want 23514, got %v", err)
		}
		for _, v := range []string{"lapses", "age"} {
			if _, err := conn.Exec(ctx, `UPDATE jobs SET assignment_fallback_reason = $1 WHERE id = 'j_bounds'`, v); err != nil {
				t.Errorf("fallback reason %q rejected: %v", v, err)
			}
		}
		for _, v := range []string{"", "AGE", "other"} {
			if _, err := conn.Exec(ctx, `UPDATE jobs SET assignment_fallback_reason = $1 WHERE id = 'j_bounds'`, v); pgCode(err) != "23514" {
				t.Errorf("fallback reason %q: want 23514, got %v", v, err)
			}
		}
	})
}

// TestMigrator_Migration000005_UpgradeFrom000004_WithExistingRows applies 000005 to a database at
// 000004 that already holds workers and jobs in several states, and proves that the existing rows
// become unassigned with zero lapses and no fallback reason, that existing workers have never
// polled, that no row count changes, and that the validated shape CHECK holds for every row.
func TestMigrator_Migration000005_UpgradeFrom000004_WithExistingRows(t *testing.T) {
	conn, _ := setupTestSchema(t)
	ctx := context.Background()

	m4, all := migrationsUpTo(t, 4)
	if n, err := m4.Migrate(ctx, conn); err != nil || n != 4 {
		t.Fatalf("migrating to 000004: applied=%d err=%v", n, err)
	}
	mustExec(t, conn, `INSERT INTO workers (id, version, total_slots, status, supported_tasks) VALUES ('w_old', 'v0', 1, 'HEALTHY', ARRAY['echo'])`)
	for _, j := range [][2]string{{"j_q", "QUEUED"}, {"j_r", "RETRYING"}, {"j_run", "RUNNING"}, {"j_done", "SUCCEEDED"}, {"j_c", "CANCELLED"}} {
		mustExec(t, conn, `INSERT INTO jobs (id, task_type, payload, state) VALUES ($1, 'echo', '\x00', $2)`, j[0], j[1])
	}
	var jobsBefore, workersBefore int
	if err := conn.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM jobs), (SELECT COUNT(*) FROM workers)`).Scan(&jobsBefore, &workersBefore); err != nil {
		t.Fatal(err)
	}

	if all[4].Name != migration000005Name {
		t.Fatalf("expected %s as the fifth migration, got %s", migration000005Name, all[4].Name)
	}
	m5, _ := migrationsUpTo(t, 5)
	if n, err := m5.Migrate(ctx, conn); err != nil || n != 1 {
		t.Fatalf("upgrading 000004 -> 000005: applied=%d err=%v", n, err)
	}

	var jobsAfter, workersAfter int
	if err := conn.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM jobs), (SELECT COUNT(*) FROM workers)`).Scan(&jobsAfter, &workersAfter); err != nil {
		t.Fatal(err)
	}
	if jobsAfter != jobsBefore || workersAfter != workersBefore {
		t.Fatalf("row counts changed: jobs %d -> %d, workers %d -> %d", jobsBefore, jobsAfter, workersBefore, workersAfter)
	}
	var bad int
	if err := conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM jobs
		WHERE assigned_worker_id IS NOT NULL OR assigned_at IS NOT NULL OR assignment_expires_at IS NOT NULL
		   OR assignment_lapses <> 0 OR assignment_fallback_reason IS NOT NULL`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("%d existing jobs are not unassigned with zero lapses (err=%v)", bad, err)
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM workers WHERE last_poll_at IS NOT NULL`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("%d existing workers have a last_poll_at (err=%v)", bad, err)
	}
	var cursors int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM assignment_cursor`).Scan(&cursors); err != nil || cursors != 0 {
		t.Fatalf("assignment_cursor starts with %d rows (err=%v), want 0", cursors, err)
	}
	var version int
	if err := conn.QueryRow(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("schema version = %d (err=%v), want 5", version, err)
	}
	// The migrated database accepts a reservation on the legacy queued job.
	mustExec(t, conn, `UPDATE jobs SET assigned_worker_id = 'w_old', assigned_at = clock_timestamp(), assignment_expires_at = clock_timestamp() + interval '5 seconds' WHERE id = 'j_q'`)
}
