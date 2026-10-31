package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// All tests in this file build their schema with setupTestSchema, which refuses any database whose
// name does not end in "_test" (SEC-023: no migrator test may run against another database).

const migration000004Name = "000004_leases_retries_cancellation.sql"

// migrationsUpTo returns a migrator limited to the first n embedded migrations.
func migrationsUpTo(t *testing.T, n int) (*postgres.Migrator, []postgres.Migration) {
	t.Helper()
	all, err := postgres.NewMigrator().LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations failed: %v", err)
	}
	if len(all) < n {
		t.Fatalf("expected at least %d embedded migrations, got %d", n, len(all))
	}
	fsys := fstest.MapFS{}
	for _, m := range all[:n] {
		fsys["migrations/"+m.Name] = &fstest.MapFile{Data: []byte(m.Content)}
	}
	return postgres.NewMigrator(postgres.WithFS(fsys), postgres.WithLockID(0x12345678)), all
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func mustExec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec failed: %v\nsql: %s", err, sql)
	}
}

func TestMigrator_Migration000004_FreshSchema(t *testing.T) {
	conn, schema := setupTestSchema(t)
	ctx := context.Background()

	m := postgres.NewMigrator()
	all, err := m.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations failed: %v", err)
	}
	// 000005 and later follow 000004; the list itself is pinned by
	// TestMigrator_Migration000005_FreshSchema.
	if len(all) < 4 || all[3].Name != migration000004Name {
		t.Fatalf("expected the fourth embedded migration to be %s, got %d migrations (last %q)", migration000004Name, len(all), all[len(all)-1].Name)
	}
	if _, err := m.Migrate(ctx, conn); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	type col struct{ table, name, typ, nullable, hasDefault string }
	wantCols := []col{
		{"attempts", "lease_expires_at", "timestamp with time zone", "NO", "no"},
		{"attempts", "error_class", "character varying", "YES", "no"},
		{"attempts", "last_error", "text", "YES", "no"},
		{"jobs", "not_before", "timestamp with time zone", "YES", "no"},
		{"jobs", "cancel_requested_at", "timestamp with time zone", "YES", "no"},
	}
	for _, c := range wantCols {
		var typ, nullable string
		var def *string
		err := conn.QueryRow(ctx, `
			SELECT data_type, is_nullable, column_default FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
			schema, c.table, c.name).Scan(&typ, &nullable, &def)
		if err != nil {
			t.Fatalf("column %s.%s: %v", c.table, c.name, err)
		}
		if typ != c.typ || nullable != c.nullable {
			t.Errorf("column %s.%s: type=%q nullable=%q, want type=%q nullable=%q", c.table, c.name, typ, nullable, c.typ, c.nullable)
		}
		if def != nil {
			t.Errorf("column %s.%s keeps default %q; the temporary lease default must be dropped", c.table, c.name, *def)
		}
	}

	for _, idx := range []string{"idx_attempts_lease_expiry", "idx_jobs_retrying_dispatch", "idx_job_events_unique_diagnostic_late_result"} {
		var def string
		err := conn.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND indexname = $2`, schema, idx).Scan(&def)
		if err != nil {
			t.Fatalf("index %s missing: %v", idx, err)
		}
		if idx == "idx_job_events_unique_diagnostic_late_result" && !strings.Contains(def, "UNIQUE") {
			t.Errorf("index %s must be UNIQUE: %s", idx, def)
		}
		if !strings.Contains(def, "WHERE") {
			t.Errorf("index %s must be partial: %s", idx, def)
		}
	}

	for _, con := range []string{"chk_attempts_error_class", "chk_attempts_last_error_size"} {
		var validated bool
		err := conn.QueryRow(ctx, `
			SELECT c.convalidated FROM pg_constraint c
			JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE n.nspname = $1 AND c.conname = $2`, schema, con).Scan(&validated)
		if err != nil {
			t.Fatalf("constraint %s missing: %v", con, err)
		}
		if !validated {
			t.Errorf("constraint %s is NOT VALID; migration must VALIDATE it", con)
		}
	}

	mustExec(t, conn, `INSERT INTO workers (id, version, total_slots, status, supported_tasks) VALUES ('w1', 'v', 1, 'HEALTHY', ARRAY['echo'])`)
	mustExec(t, conn, `INSERT INTO jobs (id, task_type, payload, state) VALUES ('j1', 'echo', '\x00', 'RUNNING')`)
	hash := domain.HashLeaseToken("t")
	insertAttempt := func(id string, no int, extra string, args ...any) error {
		a := append([]any{id, no, hash}, args...)
		_, err := conn.Exec(ctx, `INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state, lease_expires_at`+extra, a...)
		return err
	}

	t.Run("lease_expires_at_is_required", func(t *testing.T) {
		_, err := conn.Exec(ctx, `INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state) VALUES ('a_nolease', 'j1', 1, 'w1', $1, 'FAILED')`, hash)
		if pgCode(err) != "23502" {
			t.Fatalf("insert without lease_expires_at: want not_null_violation 23502, got %v", err)
		}
	})

	t.Run("error_class_accepts_exactly_the_four_values", func(t *testing.T) {
		for i, v := range []string{"PERMANENT", "TRANSIENT", "UNKNOWN", "LEASE_EXPIRED"} {
			id := "a_ok_" + v
			if err := insertAttempt(id, 10+i, `, error_class) VALUES ($1, 'j1', $2, 'w1', $3, 'FAILED', CURRENT_TIMESTAMP, $4)`, v); err != nil {
				t.Errorf("error_class %q rejected: %v", v, err)
			}
		}
		if err := insertAttempt("a_null_class", 20, `) VALUES ($1, 'j1', $2, 'w1', $3, 'FAILED', CURRENT_TIMESTAMP)`); err != nil {
			t.Errorf("NULL error_class rejected: %v", err)
		}
		for i, v := range []string{"BOGUS", "permanent", "", "PERMANENT ", "SUCCESS"} {
			err := insertAttempt("a_bad_"+string(rune('a'+i)), 30+i, `, error_class) VALUES ($1, 'j1', $2, 'w1', $3, 'FAILED', CURRENT_TIMESTAMP, $4)`, v)
			if pgCode(err) != "23514" {
				t.Errorf("error_class %q: want check_violation 23514, got %v", v, err)
			}
		}
	})

	t.Run("last_error_is_bounded_to_10KiB", func(t *testing.T) {
		if err := insertAttempt("a_le_ok", 40, `, last_error) VALUES ($1, 'j1', $2, 'w1', $3, 'FAILED', CURRENT_TIMESTAMP, $4)`, strings.Repeat("x", 10240)); err != nil {
			t.Errorf("10240-byte last_error rejected: %v", err)
		}
		err := insertAttempt("a_le_big", 41, `, last_error) VALUES ($1, 'j1', $2, 'w1', $3, 'FAILED', CURRENT_TIMESTAMP, $4)`, strings.Repeat("x", 10241))
		if pgCode(err) != "23514" {
			t.Errorf("10241-byte last_error: want check_violation 23514, got %v", err)
		}
	})

	t.Run("diagnostic_late_result_is_unique_per_job_and_attempt", func(t *testing.T) {
		ins := func(jobID, eventType, detailsJSON string) error {
			_, err := conn.Exec(ctx, `INSERT INTO job_events (job_id, event_type, details) VALUES ($1, $2, $3::jsonb)`, jobID, eventType, detailsJSON)
			return err
		}
		mustExec(t, conn, `INSERT INTO jobs (id, task_type, payload, state) VALUES ('j2', 'echo', '\x00', 'SUCCEEDED')`)

		if err := ins("j1", "DIAGNOSTIC_LATE_RESULT", `{"attempt_id":"A"}`); err != nil {
			t.Fatalf("first diagnostic rejected: %v", err)
		}
		err := ins("j1", "DIAGNOSTIC_LATE_RESULT", `{"attempt_id":"A","rejected":true}`)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "idx_job_events_unique_diagnostic_late_result" {
			t.Fatalf("second diagnostic for the same (job, attempt): want 23505 on idx_job_events_unique_diagnostic_late_result, got %v", err)
		}
		if err := ins("j1", "DIAGNOSTIC_LATE_RESULT", `{"attempt_id":"B"}`); err != nil {
			t.Errorf("diagnostic for another attempt rejected: %v", err)
		}
		if err := ins("j2", "DIAGNOSTIC_LATE_RESULT", `{"attempt_id":"A"}`); err != nil {
			t.Errorf("diagnostic for the same attempt id on another job rejected: %v", err)
		}
		// The index is partial: other event types are never limited.
		for i := 0; i < 3; i++ {
			if err := ins("j1", "LEASE_EXPIRED", `{"attempt_id":"A"}`); err != nil {
				t.Errorf("LEASE_EXPIRED duplicate %d rejected: %v", i, err)
			}
		}
		// A diagnostic with no attempt_id is not arbitrated (NULL keys are distinct); the
		// repository always writes the key, so this only documents the index semantics.
		for i := 0; i < 2; i++ {
			if err := ins("j1", "DIAGNOSTIC_LATE_RESULT", `{}`); err != nil {
				t.Errorf("diagnostic without attempt_id rejected: %v", err)
			}
		}
	})
}

// TestMigrator_Migration000004_UpgradeFrom000003_WithExistingRows applies 000004 to a database at
// 000003 that already holds jobs, attempts in every state, results and events, and proves that
// the migration backfills lease_expires_at, changes no existing row, and leaves a legacy
// in-flight attempt usable (not instantly reapable, still committable).
func TestMigrator_Migration000004_UpgradeFrom000003_WithExistingRows(t *testing.T) {
	conn, schema := setupTestSchema(t)
	ctx := context.Background()

	m3, all := migrationsUpTo(t, 3)
	if n, err := m3.Migrate(ctx, conn); err != nil || n != 3 {
		t.Fatalf("migrating to 000003: applied=%d err=%v", n, err)
	}

	token := "legacy-running-token"
	hash := domain.HashLeaseToken(token)
	mustExec(t, conn, `INSERT INTO workers (id, version, total_slots, status, supported_tasks) VALUES ('w_legacy', 'v0', 4, 'HEALTHY', ARRAY['echo'])`)
	for _, j := range [][2]string{
		{"j_run", "RUNNING"}, {"j_done", "SUCCEEDED"}, {"j_fail", "FAILED"}, {"j_cancel", "CANCELLED"}, {"j_queued", "QUEUED"},
	} {
		mustExec(t, conn, `INSERT INTO jobs (id, task_type, payload, state, max_attempts) VALUES ($1, 'echo', '\x7b7d', $2, 3)`, j[0], j[1])
	}
	mustExec(t, conn, `UPDATE jobs SET current_attempt_id = 'att_run' WHERE id = 'j_run'`)
	for _, a := range []struct {
		id, job string
		no      int
		state   string
	}{
		{"att_run", "j_run", 1, "RUNNING"},
		{"att_done", "j_done", 1, "COMPLETED"},
		{"att_lost", "j_fail", 1, "LOST"},
		{"att_fail", "j_fail", 2, "FAILED"},
		{"att_cancel", "j_cancel", 1, "CANCELLED"},
	} {
		h := hash
		if a.id != "att_run" {
			h = domain.HashLeaseToken("other-" + a.id)
		}
		mustExec(t, conn, `INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state) VALUES ($1, $2, $3, 'w_legacy', $4, $5)`, a.id, a.job, a.no, h, a.state)
	}
	mustExec(t, conn, `INSERT INTO results (job_id, attempt_id, exit_code, output, error) VALUES ('j_done', 'att_done', 0, 'ok', '')`)
	mustExec(t, conn, `INSERT INTO job_events (job_id, attempt_id, event_type, details) VALUES ('j_done', 'att_done', 'DIAGNOSTIC_LATE_RESULT', '{"attempt_id":"att_done","rejected":true}')`)
	mustExec(t, conn, `INSERT INTO job_events (job_id, event_type) VALUES ('j_queued', 'JOB_SUBMITTED')`)

	type snapshot struct{ jobs, attempts, results, events int }
	count := func() snapshot {
		var s snapshot
		for q, dst := range map[string]*int{
			`SELECT COUNT(*) FROM jobs`: &s.jobs, `SELECT COUNT(*) FROM attempts`: &s.attempts,
			`SELECT COUNT(*) FROM results`: &s.results, `SELECT COUNT(*) FROM job_events`: &s.events,
		} {
			if err := conn.QueryRow(ctx, q).Scan(dst); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		return s
	}
	statesOf := func(query string) map[string]string {
		rows, err := conn.Query(ctx, query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var id, st string
			if err := rows.Scan(&id, &st); err != nil {
				t.Fatal(err)
			}
			out[id] = st
		}
		return out
	}
	const jobStates, attStates = `SELECT id, state FROM jobs`, `SELECT id, state FROM attempts`
	before, jobStatesBefore, attStatesBefore := count(), statesOf(jobStates), statesOf(attStates)

	// Apply exactly 000004 on top of 000003 (later migrations are covered by their own tests).
	m4, _ := migrationsUpTo(t, 4)
	if all[3].Name != migration000004Name {
		t.Fatalf("expected %s as the fourth migration, got %s", migration000004Name, all[3].Name)
	}
	if n, err := m4.Migrate(ctx, conn); err != nil || n != 1 {
		t.Fatalf("upgrading 000003 -> 000004 with existing rows: applied=%d err=%v", n, err)
	}

	if after := count(); after != before {
		t.Fatalf("row counts changed by the migration: before=%+v after=%+v", before, after)
	}
	for id, st := range jobStatesBefore {
		if got := statesOf(jobStates)[id]; got != st {
			t.Errorf("job %s state changed %s -> %s", id, st, got)
		}
	}
	for id, st := range attStatesBefore {
		if got := statesOf(attStates)[id]; got != st {
			t.Errorf("attempt %s state changed %s -> %s", id, st, got)
		}
	}

	var nulls int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM attempts WHERE lease_expires_at IS NULL OR error_class IS NOT NULL OR last_error IS NOT NULL`).Scan(&nulls); err != nil || nulls != 0 {
		t.Fatalf("backfill: %d attempts with NULL lease_expires_at or non-NULL error_class/last_error (err=%v)", nulls, err)
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE not_before IS NOT NULL OR cancel_requested_at IS NOT NULL`).Scan(&nulls); err != nil || nulls != 0 {
		t.Fatalf("%d existing jobs got a not_before or cancel_requested_at (err=%v)", nulls, err)
	}

	// The legacy RUNNING attempt gets a fresh ~30s lease from the migration: not instantly reapable.
	var remaining time.Duration
	if err := conn.QueryRow(ctx, `SELECT lease_expires_at - clock_timestamp() FROM attempts WHERE id = 'att_run'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining <= 20*time.Second || remaining > 31*time.Second {
		t.Fatalf("legacy RUNNING attempt lease has %v left; want roughly the 30s of the temporary default", remaining)
	}
	var candidates int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM attempts WHERE state = 'RUNNING' AND lease_expires_at < clock_timestamp()`).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("%d legacy RUNNING attempts are immediately expired after the upgrade (err=%v)", candidates, err)
	}

	// The temporary default is gone: new attempts must state their lease explicitly.
	_, err := conn.Exec(ctx, `INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state) VALUES ('att_new', 'j_queued', 1, 'w_legacy', $1, 'RUNNING')`, hash)
	if pgCode(err) != "23502" {
		t.Fatalf("insert without lease_expires_at after upgrade: want 23502, got %v", err)
	}

	// The migrated database serves the legacy in-flight attempt: its holder can still commit.
	testURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
	cfg, err := pgxpool.ParseConfig(testURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := postgres.NewRepository(pool)
	job, err := repo.CommitAttempt(ctx, "j_run", "att_run", token, "w_legacy", domain.AttemptOutcome{ExitCode: 0, Output: "legacy ok"}, nil)
	if err != nil {
		t.Fatalf("committing the legacy in-flight attempt after the upgrade: %v", err)
	}
	if job.State != domain.StateSucceeded {
		t.Fatalf("legacy job state after commit = %s, want SUCCEEDED", job.State)
	}
}

// TestMigrator_Migration000004_PreExistingDuplicateDiagnostic_FailsLoudly: the unique index cannot
// be built over duplicate DIAGNOSTIC_LATE_RESULT rows. The shipped code never writes duplicates,
// so the migration fails loudly (and rolls back completely) instead of deleting audit rows;
// after an operator removes the duplicate, the same migration applies.
func TestMigrator_Migration000004_PreExistingDuplicateDiagnostic_FailsLoudly(t *testing.T) {
	conn, _ := setupTestSchema(t)
	ctx := context.Background()

	m3, _ := migrationsUpTo(t, 3)
	if _, err := m3.Migrate(ctx, conn); err != nil {
		t.Fatalf("migrating to 000003: %v", err)
	}
	mustExec(t, conn, `INSERT INTO jobs (id, task_type, payload, state) VALUES ('j_dup', 'echo', '\x00', 'SUCCEEDED')`)
	for i := 0; i < 2; i++ {
		mustExec(t, conn, `INSERT INTO job_events (job_id, event_type, details) VALUES ('j_dup', 'DIAGNOSTIC_LATE_RESULT', '{"attempt_id":"att_x"}')`)
	}

	m4, _ := migrationsUpTo(t, 4)
	_, err := m4.Migrate(ctx, conn)
	if err == nil || !strings.Contains(err.Error(), "idx_job_events_unique_diagnostic_late_result") {
		t.Fatalf("expected the unique index build to fail on duplicates, got %v", err)
	}
	var version int
	if err := conn.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("schema version after failed migration = %d (err=%v), want 3", version, err)
	}
	var cols int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'attempts' AND column_name = 'lease_expires_at' AND table_schema = current_schema()`).Scan(&cols); err != nil || cols != 0 {
		t.Fatalf("failed migration left %d lease_expires_at columns behind (err=%v): not atomic", cols, err)
	}

	mustExec(t, conn, `DELETE FROM job_events WHERE id = (SELECT MAX(id) FROM job_events WHERE job_id = 'j_dup')`)
	if n, err := m4.Migrate(ctx, conn); err != nil || n != 1 {
		t.Fatalf("migration after removing the duplicate: applied=%d err=%v", n, err)
	}
}
