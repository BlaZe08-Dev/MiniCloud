package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

func setupTestSchema(t *testing.T) (*pgx.Conn, string) {
	t.Helper()

	testURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("skipping postgres test: MINICLOUD_TEST_DATABASE_URL is unset")
	}

	cfg, err := pgx.ParseConfig(testURL)
	if err != nil {
		t.Fatalf("failed to parse MINICLOUD_TEST_DATABASE_URL: %v", err)
	}

	// Hard safety guard: refuse execution unless database name ends in "_test"
	if !strings.HasSuffix(cfg.Database, "_test") {
		t.Fatalf("SAFETY VIOLATION: refusing to run test against database %q; name must end in '_test'", cfg.Database)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}

	// Generate unique random schema name for test isolation
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		conn.Close(ctx)
		t.Fatalf("failed to generate random schema name: %v", err)
	}
	schemaName := fmt.Sprintf("test_%s", hex.EncodeToString(b))

	// Create and switch to isolated schema
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s;", schemaName)); err != nil {
		conn.Close(ctx)
		t.Fatalf("failed to create schema %s: %v", schemaName, err)
	}

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s;", schemaName)); err != nil {
		_, _ = conn.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE;", schemaName))
		conn.Close(ctx)
		t.Fatalf("failed to set search_path to %s: %v", schemaName, err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.Exec(cleanupCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE;", schemaName))
		_ = conn.Close(cleanupCtx)
	})

	return conn, schemaName
}

func TestMigrator_FreshMigrate(t *testing.T) {
	conn, schemaName := setupTestSchema(t)
	ctx := context.Background()

	m := postgres.NewMigrator()
	applied, err := m.Migrate(ctx, conn)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	if applied < 1 {
		t.Fatalf("expected at least 1 migration applied on fresh database, got %d", applied)
	}

	// Verify schema_migrations table exists in the test schema
	var count int
	err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations;").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query schema_migrations: %v", err)
	}
	if count != applied {
		t.Fatalf("expected %d rows in schema_migrations, got %d", applied, count)
	}

	// Verify core tables exist in the test schema
	tables := []string{"jobs", "workers", "attempts", "results", "job_events"}
	for _, tbl := range tables {
		var exists bool
		query := `
			SELECT EXISTS (
				SELECT FROM information_schema.tables 
				WHERE table_schema = $1 AND table_name = $2
			);
		`
		if err := conn.QueryRow(ctx, query, schemaName, tbl).Scan(&exists); err != nil {
			t.Fatalf("failed to check existence of table %s: %v", tbl, err)
		}
		if !exists {
			t.Fatalf("expected table %s to exist in schema %s", tbl, schemaName)
		}
	}

	// Verify worker_capacity view exists
	var viewExists bool
	viewQuery := `
		SELECT EXISTS (
			SELECT FROM information_schema.views 
			WHERE table_schema = $1 AND table_name = 'worker_capacity'
		);
	`
	if err := conn.QueryRow(ctx, viewQuery, schemaName).Scan(&viewExists); err != nil {
		t.Fatalf("failed to check existence of worker_capacity view: %v", err)
	}
	if !viewExists {
		t.Fatalf("expected worker_capacity view to exist in schema %s", schemaName)
	}
}

func TestMigrator_IdempotentReRun(t *testing.T) {
	conn, _ := setupTestSchema(t)
	ctx := context.Background()

	m := postgres.NewMigrator()
	applied1, err := m.Migrate(ctx, conn)
	if err != nil {
		t.Fatalf("first Migrate failed: %v", err)
	}
	if applied1 < 1 {
		t.Fatalf("expected migrations applied on first run, got %d", applied1)
	}

	// Second run must be a no-op
	applied2, err := m.Migrate(ctx, conn)
	if err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	if applied2 != 0 {
		t.Fatalf("expected 0 migrations on re-run, got %d", applied2)
	}
}

func TestMigrator_ChecksumTamperingDetected(t *testing.T) {
	conn, _ := setupTestSchema(t)
	ctx := context.Background()

	m := postgres.NewMigrator()
	_, err := m.Migrate(ctx, conn)
	if err != nil {
		t.Fatalf("initial Migrate failed: %v", err)
	}

	// Tamper with the recorded checksum
	tamperSQL := "UPDATE schema_migrations SET sha256_checksum = 'tampered_bogus_checksum' WHERE version = 1;"
	if _, err := conn.Exec(ctx, tamperSQL); err != nil {
		t.Fatalf("failed to tamper with schema_migrations: %v", err)
	}

	// Subsequent migration run must detect tampering and fail loudly
	_, err = m.Migrate(ctx, conn)
	if err == nil {
		t.Fatalf("expected error due to checksum tampering, got nil")
	}
	if !errors.Is(err, postgres.ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got: %v", err)
	}
}

func TestMigrator_ConcurrentMigrators_ExactlyOneApplies(t *testing.T) {
	testURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("skipping postgres test: MINICLOUD_TEST_DATABASE_URL is unset")
	}

	cfg, err := pgx.ParseConfig(testURL)
	if err != nil {
		t.Fatalf("failed to parse MINICLOUD_TEST_DATABASE_URL: %v", err)
	}
	if !strings.HasSuffix(cfg.Database, "_test") {
		t.Fatalf("SAFETY VIOLATION: refusing to run test against database %q; name must end in '_test'", cfg.Database)
	}

	ctx := context.Background()
	adminConn, err := pgx.Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("failed to connect admin conn: %v", err)
	}
	defer adminConn.Close(ctx)

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	schemaName := fmt.Sprintf("test_conc_%s", hex.EncodeToString(b))

	if _, err := adminConn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s;", schemaName)); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	defer func() {
		_, _ = adminConn.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE;", schemaName))
	}()

	// Open two separate connections configured for the same schema
	conn1, err := pgx.Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("failed to connect conn1: %v", err)
	}
	defer conn1.Close(ctx)
	_, _ = conn1.Exec(ctx, fmt.Sprintf("SET search_path TO %s;", schemaName))

	conn2, err := pgx.Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("failed to connect conn2: %v", err)
	}
	defer conn2.Close(ctx)
	_, _ = conn2.Exec(ctx, fmt.Sprintf("SET search_path TO %s;", schemaName))

	startBarrier := make(chan struct{})
	var wg sync.WaitGroup
	var applied1, applied2 int
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-startBarrier
		m := postgres.NewMigrator()
		applied1, err1 = m.Migrate(ctx, conn1)
	}()

	go func() {
		defer wg.Done()
		<-startBarrier
		m := postgres.NewMigrator()
		applied2, err2 = m.Migrate(ctx, conn2)
	}()

	close(startBarrier)
	wg.Wait()

	if err1 != nil {
		t.Fatalf("migrator 1 error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("migrator 2 error: %v", err2)
	}

	// Exactly all embedded migrations should have been applied across both runners
	totalApplied := applied1 + applied2
	mChecker := postgres.NewMigrator()
	expectedMigrations, err := mChecker.LatestEmbeddedVersion()
	if err != nil {
		t.Fatalf("failed to determine latest embedded migration version: %v", err)
	}
	if totalApplied != expectedMigrations {
		t.Fatalf("expected exactly %d migrations applied across both concurrent runners, got %d (runner1=%d, runner2=%d)",
			expectedMigrations, totalApplied, applied1, applied2)
	}
}

func TestSchemaInvariants_SQLViolations(t *testing.T) {
	conn, schemaName := setupTestSchema(t)
	ctx := context.Background()

	m := postgres.NewMigrator()
	if _, err := m.Migrate(ctx, conn); err != nil {
		t.Fatalf("initial migration failed: %v", err)
	}

	t.Run("SecondActiveAttemptForJob_RejectedByPartialIndex", func(t *testing.T) {
		// Insert job and worker
		_, err := conn.Exec(ctx, `
			INSERT INTO jobs (id, task_type, payload, state)
			VALUES ('job_active_inv', 'echo', '\x68656c6c6f', 'RUNNING');
		`)
		if err != nil {
			t.Fatalf("failed to insert job: %v", err)
		}

		_, err = conn.Exec(ctx, `
			INSERT INTO workers (id, version, total_slots, status, supported_tasks)
			VALUES ('worker_active_inv', 'v0.1.0', 2, 'HEALTHY', ARRAY['echo']);
		`)
		if err != nil {
			t.Fatalf("failed to insert worker: %v", err)
		}

		// First active attempt succeeds (RUNNING)
		_, err = conn.Exec(ctx, `
			INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state)
			VALUES ('att_act_1', 'job_active_inv', 1, 'worker_active_inv', repeat('a', 64), 'RUNNING');
		`)
		if err != nil {
			t.Fatalf("expected first active attempt to succeed, got: %v", err)
		}

		// Second active attempt for same job with state RUNNING must fail due to idx_attempts_active_per_job
		_, err = conn.Exec(ctx, `
			INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state)
			VALUES ('att_act_2_running', 'job_active_inv', 2, 'worker_active_inv', repeat('b', 64), 'RUNNING');
		`)
		if err == nil {
			t.Fatalf("expected second RUNNING active attempt on same job to fail with unique violation, got nil")
		}
		if !strings.Contains(err.Error(), "idx_attempts_active_per_job") && !strings.Contains(err.Error(), "23505") {
			t.Fatalf("expected unique index violation on idx_attempts_active_per_job for RUNNING attempt, got: %v", err)
		}

		// Second active attempt for same job with state PENDING must ALSO fail due to idx_attempts_active_per_job
		_, err = conn.Exec(ctx, `
			INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state)
			VALUES ('att_act_2_pending', 'job_active_inv', 3, 'worker_active_inv', repeat('c', 64), 'PENDING');
		`)
		if err == nil {
			t.Fatalf("expected second PENDING active attempt on same job to fail with unique violation, got nil")
		}
		if !strings.Contains(err.Error(), "idx_attempts_active_per_job") && !strings.Contains(err.Error(), "23505") {
			t.Fatalf("expected unique index violation on idx_attempts_active_per_job for PENDING attempt, got: %v", err)
		}

		// Verify worker_capacity view counts non-terminal attempts (RUNNING or PENDING)
		var availSlots int
		err = conn.QueryRow(ctx, fmt.Sprintf("SELECT available_slots FROM %s.worker_capacity WHERE id = 'worker_active_inv';", schemaName)).Scan(&availSlots)
		if err != nil {
			t.Fatalf("failed to query worker_capacity: %v", err)
		}
		if availSlots != 1 {
			t.Fatalf("expected available_slots = 1 for worker with 2 total slots and 1 active attempt, got %d", availSlots)
		}
	})

	t.Run("DuplicateJobAttemptNo_RejectedByUniqueConstraint", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			INSERT INTO jobs (id, task_type, payload, state)
			VALUES ('job_dup_att', 'echo', '\x68656c6c6f', 'QUEUED');
		`)
		if err != nil {
			t.Fatalf("failed to insert job: %v", err)
		}

		// Insert attempt 1
		_, err = conn.Exec(ctx, `
			INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state)
			VALUES ('att_dup_1', 'job_dup_att', 1, 'worker_active_inv', repeat('a', 64), 'COMPLETED');
		`)
		if err != nil {
			t.Fatalf("failed to insert attempt 1: %v", err)
		}

		// Insert duplicate attempt_no 1 for same job
		_, err = conn.Exec(ctx, `
			INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state)
			VALUES ('att_dup_2', 'job_dup_att', 1, 'worker_active_inv', repeat('b', 64), 'FAILED');
		`)
		if err == nil {
			t.Fatalf("expected duplicate attempt_no to fail, got nil")
		}
		if !strings.Contains(err.Error(), "uq_attempts_job_attempt_no") && !strings.Contains(err.Error(), "23505") {
			t.Fatalf("expected unique constraint violation on uq_attempts_job_attempt_no, got: %v", err)
		}
	})

	t.Run("SecondResultForJob_RejectedByPrimaryKey", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			INSERT INTO jobs (id, task_type, payload, state)
			VALUES ('job_res_inv', 'echo', '\x68656c6c6f', 'RUNNING');
		`)
		if err != nil {
			t.Fatalf("failed to insert job: %v", err)
		}

		_, err = conn.Exec(ctx, `
			INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state)
			VALUES ('att_res_1', 'job_res_inv', 1, 'worker_active_inv', repeat('a', 64), 'COMPLETED');
		`)
		if err != nil {
			t.Fatalf("failed to insert attempt: %v", err)
		}

		// First result succeeds
		_, err = conn.Exec(ctx, `
			INSERT INTO results (job_id, attempt_id, exit_code, output, error)
			VALUES ('job_res_inv', 'att_res_1', 0, 'ok', '');
		`)
		if err != nil {
			t.Fatalf("failed to insert first result: %v", err)
		}

		// Second result for same job must fail (PK violation)
		_, err = conn.Exec(ctx, `
			INSERT INTO results (job_id, attempt_id, exit_code, output, error)
			VALUES ('job_res_inv', 'att_res_1', 0, 'duplicate', '');
		`)
		if err == nil {
			t.Fatalf("expected duplicate result to fail, got nil")
		}
		if !strings.Contains(err.Error(), "results_pkey") && !strings.Contains(err.Error(), "23505") {
			t.Fatalf("expected PK violation on results, got: %v", err)
		}
	})

	t.Run("OversizedPayload_RejectedByCheckConstraint", func(t *testing.T) {
		// 65537 bytes (> 64 KiB)
		oversized := make([]byte, 65537)
		_, err := conn.Exec(ctx, `
			INSERT INTO jobs (id, task_type, payload, state)
			VALUES ('job_oversized', 'echo', $1, 'QUEUED');
		`, oversized)
		if err == nil {
			t.Fatalf("expected oversized payload to fail, got nil")
		}
		if !strings.Contains(err.Error(), "chk_jobs_payload_size") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected check violation chk_jobs_payload_size, got: %v", err)
		}
	})

	t.Run("OversizedOutput_RejectedByCheckConstraint", func(t *testing.T) {
		oversizedText := strings.Repeat("x", 65537)
		_, err := conn.Exec(ctx, `
			INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state, output)
			VALUES ('att_oversized', 'job_res_inv', 2, 'worker_active_inv', repeat('c', 64), 'COMPLETED', $1);
		`, oversizedText)
		if err == nil {
			t.Fatalf("expected oversized output to fail, got nil")
		}
		if !strings.Contains(err.Error(), "chk_attempts_output_size") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected check violation chk_attempts_output_size, got: %v", err)
		}
	})

	t.Run("InvalidStateValues_RejectedByCheckConstraints", func(t *testing.T) {
		// Invalid job state
		_, err := conn.Exec(ctx, `
			INSERT INTO jobs (id, task_type, payload, state)
			VALUES ('job_inv_state', 'echo', '\x68656c6c6f', 'NOT_A_VALID_STATE');
		`)
		if err == nil {
			t.Fatalf("expected invalid job state to fail, got nil")
		}
		if !strings.Contains(err.Error(), "chk_jobs_state") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected check violation chk_jobs_state, got: %v", err)
		}

		// Invalid attempt state
		_, err = conn.Exec(ctx, `
			INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state)
			VALUES ('att_inv_state', 'job_res_inv', 3, 'worker_active_inv', repeat('d', 64), 'NOT_A_STATE');
		`)
		if err == nil {
			t.Fatalf("expected invalid attempt state to fail, got nil")
		}
		if !strings.Contains(err.Error(), "chk_attempts_state") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected check violation chk_attempts_state, got: %v", err)
		}

		// Invalid worker status
		_, err = conn.Exec(ctx, `
			INSERT INTO workers (id, version, total_slots, status, supported_tasks)
			VALUES ('worker_inv_status', 'v0.1.0', 1, 'INVALID_STATUS', ARRAY['echo']);
		`)
		if err == nil {
			t.Fatalf("expected invalid worker status to fail, got nil")
		}
		if !strings.Contains(err.Error(), "chk_workers_status") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected check violation chk_workers_status, got: %v", err)
		}
	})

	t.Run("WorkerSlotsRange_RejectedByCheckConstraints", func(t *testing.T) {
		// Zero slots
		_, err := conn.Exec(ctx, `
			INSERT INTO workers (id, version, total_slots, status, supported_tasks)
			VALUES ('w_slots_zero', 'v0.1.0', 0, 'HEALTHY', ARRAY['echo']);
		`)
		if err == nil {
			t.Fatalf("expected zero slots to fail check constraint, got nil")
		}
		if !strings.Contains(err.Error(), "chk_workers_total_slots_range") && !strings.Contains(err.Error(), "chk_workers_slots") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected check constraint violation on total_slots <= 0, got: %v", err)
		}

		// 1025 slots (> 1024)
		_, err = conn.Exec(ctx, `
			INSERT INTO workers (id, version, total_slots, status, supported_tasks)
			VALUES ('w_slots_excess', 'v0.1.0', 1025, 'HEALTHY', ARRAY['echo']);
		`)
		if err == nil {
			t.Fatalf("expected >1024 slots to fail check constraint, got nil")
		}
		if !strings.Contains(err.Error(), "chk_workers_total_slots_range") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected check constraint violation on total_slots > 1024, got: %v", err)
		}
	})

	t.Run("WorkerVersionLength_RejectedByCheckConstraint", func(t *testing.T) {
		// 65 chars (> 64)
		longVersion := strings.Repeat("v", 65)
		_, err := conn.Exec(ctx, `
			INSERT INTO workers (id, version, total_slots, status, supported_tasks)
			VALUES ('w_ver_long', $1, 1, 'HEALTHY', ARRAY['echo']);
		`, longVersion)
		if err == nil {
			t.Fatalf("expected >64 chars version to fail, got nil")
		}
		if !strings.Contains(err.Error(), "chk_workers_version_length") && !strings.Contains(err.Error(), "22001") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected version length violation, got: %v", err)
		}
	})

	t.Run("WorkerSupportedTasksCount_RejectedByCheckConstraint", func(t *testing.T) {
		// 17 tasks (> 16)
		excessTasks := make([]string, 17)
		for i := range excessTasks {
			excessTasks[i] = fmt.Sprintf("task_%d", i)
		}
		_, err := conn.Exec(ctx, `
			INSERT INTO workers (id, version, total_slots, status, supported_tasks)
			VALUES ('w_tasks_excess', 'v0.1.0', 1, 'HEALTHY', $1);
		`, excessTasks)
		if err == nil {
			t.Fatalf("expected >16 tasks to fail check constraint, got nil")
		}
		if !strings.Contains(err.Error(), "chk_workers_supported_tasks_count") && !strings.Contains(err.Error(), "23514") {
			t.Fatalf("expected check violation chk_workers_supported_tasks_count, got: %v", err)
		}
	})
}

// TestMigrator_PreExistingInvalidRow_ValidationFails verifies that when migration 000003
// runs against an existing database containing a row that violates the new CHECK constraints,
// the VALIDATE CONSTRAINT step fails cleanly with a check_violation error (SQLSTATE 23514),
// proving that pre-existing invalid data cannot be silently certified.
func TestMigrator_PreExistingInvalidRow_ValidationFails(t *testing.T) {
	testURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("skipping postgres test: MINICLOUD_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testURL)
	if err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}
	defer conn.Close(ctx)

	schemaName := fmt.Sprintf("test_migrator_pre_existing_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s;", schemaName)); err != nil {
		t.Fatalf("failed to create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanConn, err := pgx.Connect(cleanupCtx, testURL)
		if err == nil {
			_, _ = cleanConn.Exec(cleanupCtx, fmt.Sprintf("DROP SCHEMA %s CASCADE;", schemaName))
			_ = cleanConn.Close(cleanupCtx)
		}
	})

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s;", schemaName)); err != nil {
		t.Fatalf("failed to set search path: %v", err)
	}

	// 1. Read real embedded migrations 1 and 2
	realMigrator := postgres.NewMigrator()
	allMigrations, err := realMigrator.LoadMigrations()
	if err != nil {
		t.Fatalf("failed to load real migrations: %v", err)
	}
	if len(allMigrations) < 3 {
		t.Fatalf("expected at least 3 migrations, got %d", len(allMigrations))
	}

	// Create test fs with only migrations 1 and 2
	phase1FS := fstest.MapFS{
		"migrations/" + allMigrations[0].Name: &fstest.MapFile{Data: []byte(allMigrations[0].Content)},
		"migrations/" + allMigrations[1].Name: &fstest.MapFile{Data: []byte(allMigrations[1].Content)},
	}
	m1 := postgres.NewMigrator(postgres.WithFS(phase1FS), postgres.WithLockID(0x12345678))
	applied, err := m1.Migrate(ctx, conn)
	if err != nil {
		t.Fatalf("phase 1 migration failed: %v", err)
	}
	if applied != 2 {
		t.Fatalf("expected 2 migrations applied, got %d", applied)
	}

	// 2. Insert a pre-existing worker row with 5000 slots (valid under migration 1/2, invalid under migration 3)
	_, err = conn.Exec(ctx, `
		INSERT INTO workers (id, version, total_slots, status, supported_tasks)
		VALUES ('w_legacy_oversized', 'v0.1.0', 5000, 'HEALTHY', ARRAY['echo']);
	`)
	if err != nil {
		t.Fatalf("failed to insert legacy oversized worker: %v", err)
	}

	// 3. Now run migration runner that includes migration 000003
	phase2FS := fstest.MapFS{
		"migrations/" + allMigrations[0].Name: &fstest.MapFile{Data: []byte(allMigrations[0].Content)},
		"migrations/" + allMigrations[1].Name: &fstest.MapFile{Data: []byte(allMigrations[1].Content)},
		"migrations/" + allMigrations[2].Name: &fstest.MapFile{Data: []byte(allMigrations[2].Content)},
	}
	m2 := postgres.NewMigrator(postgres.WithFS(phase2FS), postgres.WithLockID(0x12345678))
	_, err = m2.Migrate(ctx, conn)
	if err == nil {
		t.Fatalf("expected migration 000003 to fail when validating constraint on oversized row, got nil")
	}
	if !strings.Contains(err.Error(), "chk_workers_total_slots_range") && !strings.Contains(err.Error(), "23514") {
		t.Fatalf("expected check constraint violation on VALIDATE CONSTRAINT, got: %v", err)
	}

	// 4. Fix the pre-existing row to conform to the new limit
	if _, err := conn.Exec(ctx, "UPDATE workers SET total_slots = 100 WHERE id = 'w_legacy_oversized';"); err != nil {
		t.Fatalf("failed to update worker row: %v", err)
	}

	// 5. Re-run migration: now it must succeed
	applied2, err := m2.Migrate(ctx, conn)
	if err != nil {
		t.Fatalf("expected migration to succeed after remediation, got error: %v", err)
	}
	if applied2 != 1 {
		t.Fatalf("expected exactly 1 migration applied after fix, got %d", applied2)
	}
}
