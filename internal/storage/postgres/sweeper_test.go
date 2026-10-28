package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

type sweepFixture struct {
	pool *pgxpool.Pool
	repo *postgres.Repository
	att  *domain.Attempt
}

// newSweepFixture builds one worker, one RUNNING job and its attempt, with the lease already passed.
func newSweepFixture(t *testing.T, lockTimeout time.Duration) sweepFixture {
	t.Helper()
	ctx := context.Background()
	pool, _ := setupTestPool(t)
	repo := postgres.NewRepository(pool, postgres.WithSweepLockTimeout(lockTimeout))
	if err := repo.UpsertWorker(ctx, &domain.Worker{ID: "w_lo", Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SubmitJob(ctx, &domain.Job{ID: "j_lo", TaskType: "echo", Payload: []byte(`{}`), State: domain.StateQueued, MaxAttempts: 3, TimeoutSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	att, err := repo.DispatchJob(ctx, "j_lo", "w_lo")
	if err != nil {
		t.Fatal(err)
	}
	expireLease(t, pool, att.ID)
	return sweepFixture{pool, repo, att}
}

func expireLease(t *testing.T, pool *pgxpool.Pool, attemptID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE attempts SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, attemptID); err != nil {
		t.Fatal(err)
	}
}

func zeroBackoff() domain.SweepParams {
	return domain.SweepParams{Backoff: func(int) time.Duration { return 0 }}
}

// lockRow takes a row lock in an open transaction, standing in for a concurrent writer.
func lockRow(t *testing.T, pool *pgxpool.Pool, table, id string) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var got string
	var q string
	switch table {
	case "jobs":
		q = `SELECT id FROM jobs WHERE id = $1 FOR UPDATE`
	case "workers":
		q = `SELECT id FROM workers WHERE id = $1 FOR UPDATE`
	case "attempts":
		q = `SELECT id FROM attempts WHERE id = $1 FOR UPDATE`
	default:
		t.Fatalf("unknown table %q", table)
	}
	if err := tx.QueryRow(ctx, q, id).Scan(&got); err != nil {
		t.Fatalf("locking %s row %s: %v", table, id, err)
	}
	return tx
}

// rowLockedByOthers reports whether a row is currently locked by another transaction: a NOWAIT
// lock attempt fails with lock_not_available.
func rowLockedByOthers(t *testing.T, pool *pgxpool.Pool, table, id string) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var q string
	switch table {
	case "jobs":
		q = `SELECT id FROM jobs WHERE id = $1 FOR UPDATE NOWAIT`
	case "workers":
		q = `SELECT id FROM workers WHERE id = $1 FOR UPDATE NOWAIT`
	case "attempts":
		q = `SELECT id FROM attempts WHERE id = $1 FOR UPDATE NOWAIT`
	default:
		t.Fatalf("unknown table %q", table)
	}
	var got string
	err = tx.QueryRow(ctx, q, id).Scan(&got)
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
		return true
	}
	t.Fatalf("NOWAIT probe on %s: %v", table, err)
	return false
}

// waitForLockWaiter blocks until a backend is waiting on a row lock in a statement that contains
// fragment, proving the sweeper has reached that lock.
func waitForLockWaiter(t *testing.T, pool *pgxpool.Pool, fragment string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%' || $1 || '%' AND pid <> pg_backend_pid()`, fragment).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the sweeper never blocked on a lock in a statement containing %q", fragment)
}

type sweepOutcome struct {
	res *domain.SweepResult
	err error
	dur time.Duration
}

func sweepAsync(f sweepFixture, p domain.SweepParams) <-chan sweepOutcome {
	ch := make(chan sweepOutcome, 1)
	go func() {
		start := time.Now()
		res, err := f.repo.SweepExpiredLeases(context.Background(), p)
		ch <- sweepOutcome{res, err, time.Since(start)}
	}()
	return ch
}

func awaitSweep(t *testing.T, ch <-chan sweepOutcome) sweepOutcome {
	t.Helper()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("sweep returned an error: %v", out.err)
		}
		return out
	case <-time.After(20 * time.Second):
		t.Fatal("sweep did not finish")
		return sweepOutcome{}
	}
}

func attemptStateOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var st string
	if err := pool.QueryRow(context.Background(), `SELECT state FROM attempts WHERE id = $1`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestPostgresRepository_Sweeper_CanonicalLockOrder proves the sweeper acquires row locks in the
// canonical order jobs -> workers -> attempts, by parking it on each blocking lock in turn and
// checking, from another connection, which locks it already holds. It also pins the two ways a
// candidate is skipped without being a failed tick: SKIP LOCKED on the job, and lock_timeout on
// the workers or attempts row.
func TestPostgresRepository_Sweeper_CanonicalLockOrder(t *testing.T) {
	t.Run("blocked_on_workers_holds_jobs_but_not_attempts", func(t *testing.T) {
		f := newSweepFixture(t, 10*time.Second)
		holder := lockRow(t, f.pool, "workers", "w_lo")
		ch := sweepAsync(f, zeroBackoff())
		waitForLockWaiter(t, f.pool, "FROM workers WHERE id")

		if !rowLockedByOthers(t, f.pool, "jobs", "j_lo") {
			t.Fatal("while waiting for the workers lock the sweeper does not hold the jobs lock: jobs must be locked before workers")
		}
		if rowLockedByOthers(t, f.pool, "attempts", f.att.ID) {
			t.Fatal("the sweeper holds the attempts lock while still waiting for workers: attempts must be locked after workers")
		}
		_ = holder.Rollback(context.Background())
		if out := awaitSweep(t, ch); len(out.res.Swept) != 1 {
			t.Fatalf("sweep after the workers lock was released = %+v", out.res)
		}
	})

	t.Run("blocked_on_attempts_holds_jobs_and_workers", func(t *testing.T) {
		f := newSweepFixture(t, 10*time.Second)
		holder := lockRow(t, f.pool, "attempts", f.att.ID)
		ch := sweepAsync(f, zeroBackoff())
		waitForLockWaiter(t, f.pool, "FROM attempts")

		if !rowLockedByOthers(t, f.pool, "jobs", "j_lo") {
			t.Fatal("while waiting for the attempts lock the sweeper does not hold the jobs lock")
		}
		if !rowLockedByOthers(t, f.pool, "workers", "w_lo") {
			t.Fatal("while waiting for the attempts lock the sweeper does not hold the workers lock: workers must be locked before attempts")
		}
		_ = holder.Rollback(context.Background())
		if out := awaitSweep(t, ch); len(out.res.Swept) != 1 {
			t.Fatalf("sweep after the attempts lock was released = %+v", out.res)
		}
	})

	t.Run("locked_job_is_skipped_without_waiting_and_is_not_an_error", func(t *testing.T) {
		f := newSweepFixture(t, 10*time.Second)
		holder := lockRow(t, f.pool, "jobs", "j_lo")
		out := awaitSweep(t, sweepAsync(f, zeroBackoff()))
		if out.res.SkippedLocked != 1 || len(out.res.Swept) != 0 || out.res.Candidates != 1 {
			t.Fatalf("sweep with the job row locked = %+v, want one SkippedLocked", out.res)
		}
		if out.dur > 5*time.Second {
			t.Fatalf("sweep waited %v on a locked job; SKIP LOCKED must not wait", out.dur)
		}
		if attemptStateOf(t, f.pool, f.att.ID) != "RUNNING" {
			t.Fatal("a skipped candidate was mutated")
		}
		_ = holder.Rollback(context.Background())
		if out := awaitSweep(t, sweepAsync(f, zeroBackoff())); len(out.res.Swept) != 1 {
			t.Fatalf("the skipped candidate was not picked up on the next tick: %+v", out.res)
		}
	})

	for _, table := range []string{"workers", "attempts"} {
		table := table
		t.Run("lock_timeout_on_"+table+"_skips_the_candidate_and_is_not_an_error", func(t *testing.T) {
			f := newSweepFixture(t, 300*time.Millisecond)
			id := "w_lo"
			if table == "attempts" {
				id = f.att.ID
			}
			holder := lockRow(t, f.pool, table, id)
			out := awaitSweep(t, sweepAsync(f, zeroBackoff()))
			if out.res.SkippedLocked != 1 || len(out.res.Swept) != 0 {
				t.Fatalf("sweep with the %s row locked = %+v, want one SkippedLocked", table, out.res)
			}
			if out.dur < 250*time.Millisecond {
				t.Fatalf("sweep returned after %v; it must wait for lock_timeout (300ms) before skipping", out.dur)
			}
			if attemptStateOf(t, f.pool, f.att.ID) != "RUNNING" {
				t.Fatal("a candidate skipped on lock timeout was mutated")
			}
			var jobState string
			if err := f.pool.QueryRow(context.Background(), `SELECT state FROM jobs WHERE id = 'j_lo'`).Scan(&jobState); err != nil || jobState != "RUNNING" {
				t.Fatalf("job state after a lock-timeout skip = %q (%v)", jobState, err)
			}
			_ = holder.Rollback(context.Background())
			if out := awaitSweep(t, sweepAsync(f, zeroBackoff())); len(out.res.Swept) != 1 {
				t.Fatalf("the skipped candidate was not picked up on the next tick: %+v", out.res)
			}
		})
	}
}

// TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck proves the in-lock re-check: when an
// attempt stops being an expired RUNNING attempt between the candidate scan and the attempts row
// lock (a renewal or a commit landed), the sweeper makes no change. The sweeper is parked on the
// attempts lock, held by a transaction that renews or completes the attempt, then released.
// (A real RenewLease needs the job lock the parked sweeper holds, so the renewal is applied
// with the same UPDATE RenewLease issues.)
func TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck(t *testing.T) {
	cases := []struct {
		name  string
		apply string
		check func(t *testing.T, f sweepFixture)
	}{
		{
			name:  "renewal_landed_after_the_scan",
			apply: `UPDATE attempts SET lease_expires_at = clock_timestamp() + interval '30 seconds' WHERE id = $1`,
			check: func(t *testing.T, f sweepFixture) {
				var remaining time.Duration
				if err := f.pool.QueryRow(context.Background(), `SELECT lease_expires_at - clock_timestamp() FROM attempts WHERE id = $1`, f.att.ID).Scan(&remaining); err != nil || remaining < 20*time.Second {
					t.Fatalf("the renewed lease was overwritten: %v left (%v)", remaining, err)
				}
				if got := attemptStateOf(t, f.pool, f.att.ID); got != "RUNNING" {
					t.Fatalf("renewed attempt is %s, want RUNNING", got)
				}
			},
		},
		{
			name:  "commit_landed_after_the_scan",
			apply: `UPDATE attempts SET state = 'COMPLETED', finished_at = clock_timestamp() WHERE id = $1`,
			check: func(t *testing.T, f sweepFixture) {
				if got := attemptStateOf(t, f.pool, f.att.ID); got != "COMPLETED" {
					t.Fatalf("committed attempt is %s, want COMPLETED (never LOST)", got)
				}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newSweepFixture(t, 10*time.Second)
			ctx := context.Background()
			var eventsBefore int
			if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = 'j_lo'`).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}

			// The scan will see the expired attempt; hold its row lock so the sweeper parks there.
			holder := lockRow(t, f.pool, "attempts", f.att.ID)
			ch := sweepAsync(f, zeroBackoff())
			waitForLockWaiter(t, f.pool, "FROM attempts")
			if _, err := holder.Exec(ctx, tc.apply, f.att.ID); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			out := awaitSweep(t, ch)
			if out.res.Candidates != 1 || len(out.res.Swept) != 0 || out.res.SkippedChanged != 1 {
				t.Fatalf("sweep = %+v, want the candidate found by the scan but skipped by the re-check", out.res)
			}
			tc.check(t, f)
			var jobState string
			var eventsAfter int
			if err := f.pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id = 'j_lo'`).Scan(&jobState); err != nil || jobState != "RUNNING" {
				t.Fatalf("job state = %q (%v), want untouched RUNNING", jobState, err)
			}
			if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = 'j_lo'`).Scan(&eventsAfter); err != nil || eventsAfter != eventsBefore {
				t.Fatalf("events %d -> %d (%v): a skipped candidate must record nothing", eventsBefore, eventsAfter, err)
			}
		})
	}
}

// TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt verifies, directly in SQL, the
// SEC-022 rule that a commit rejected because the job is already terminal closes a RUNNING attempt
// (CANCELLED for a CANCELLED job, LOST otherwise), frees the worker slot in worker_capacity, and
// records exactly one DIAGNOSTIC_LATE_RESULT row, even when many rejected commits race.
func TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt(t *testing.T) {
	cases := []struct {
		name        string
		jobState    domain.JobState
		wantAttempt string
	}{
		{"cancelled_job_closes_attempt_cancelled", domain.StateCancelled, "CANCELLED"},
		{"succeeded_job_closes_attempt_lost", domain.StateSucceeded, "LOST"},
		{"failed_job_closes_attempt_lost", domain.StateFailed, "LOST"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool, _ := setupTestPool(t)
			repo := postgres.NewRepository(pool)
			if err := repo.UpsertWorker(ctx, &domain.Worker{ID: "w_t", Version: "v", TotalSlots: 2, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}}); err != nil {
				t.Fatal(err)
			}
			if err := repo.SubmitJob(ctx, &domain.Job{ID: "j_t", TaskType: "echo", Payload: []byte(`{}`), State: domain.StateQueued, MaxAttempts: 3, TimeoutSeconds: 30}); err != nil {
				t.Fatal(err)
			}
			att, err := repo.DispatchJob(ctx, "j_t", "w_t")
			if err != nil {
				t.Fatal(err)
			}
			var slots int
			if err := pool.QueryRow(ctx, `SELECT available_slots FROM worker_capacity WHERE id = 'w_t'`).Scan(&slots); err != nil || slots != 1 {
				t.Fatalf("setup: available_slots = %d (%v), want 1", slots, err)
			}
			if _, err := pool.Exec(ctx, `UPDATE jobs SET state = $1 WHERE id = 'j_t'`, string(tc.jobState)); err != nil {
				t.Fatal(err)
			}

			const racers = 20
			errs := make([]error, racers)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < racers; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					_, errs[i] = repo.CommitAttempt(ctx, "j_t", att.ID, att.LeaseToken, "w_t", domain.AttemptOutcome{ExitCode: 0, Output: "late"})
				}(i)
			}
			close(start)
			wg.Wait()
			for i, err := range errs {
				if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
					t.Fatalf("racer %d: want ErrJobAlreadyTerminal, got %v", i, err)
				}
			}

			var state string
			var finished bool
			if err := pool.QueryRow(ctx, `SELECT state, finished_at IS NOT NULL FROM attempts WHERE id = $1`, att.ID).Scan(&state, &finished); err != nil {
				t.Fatal(err)
			}
			if state != tc.wantAttempt || !finished {
				t.Fatalf("attempt state=%s finished=%v, want %s with finished_at set", state, finished, tc.wantAttempt)
			}
			if err := pool.QueryRow(ctx, `SELECT available_slots FROM worker_capacity WHERE id = 'w_t'`).Scan(&slots); err != nil || slots != 2 {
				t.Fatalf("available_slots = %d (%v), want 2: the rejected commit must free the slot", slots, err)
			}
			var diag int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = 'j_t' AND event_type = 'DIAGNOSTIC_LATE_RESULT'`).Scan(&diag); err != nil || diag != 1 {
				t.Fatalf("%d DIAGNOSTIC_LATE_RESULT rows after %d racing rejected commits (%v), want exactly 1", diag, racers, err)
			}
			var results int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM results WHERE job_id = 'j_t'`).Scan(&results); err != nil || results != 0 {
				t.Fatalf("%d results rows written by rejected commits (%v)", results, err)
			}
		})
	}
}

// TestPostgresRepository_CommitAttempt_LostAttemptDiagnosticBoundedByIndex: racing late commits on
// a swept attempt record exactly one diagnostic row, and the database itself refuses a second one.
func TestPostgresRepository_CommitAttempt_LostAttemptDiagnosticBoundedByIndex(t *testing.T) {
	ctx := context.Background()
	f := newSweepFixture(t, time.Second)
	if res, err := f.repo.SweepExpiredLeases(ctx, domain.SweepParams{Backoff: func(int) time.Duration { return time.Hour }}); err != nil || len(res.Swept) != 1 {
		t.Fatalf("setup sweep = %+v, %v", res, err)
	}

	const racers = 20
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.repo.CommitAttempt(ctx, "j_lo", f.att.ID, f.att.LeaseToken, "w_lo", domain.AttemptOutcome{ExitCode: 0})
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("racer %d: want ErrLeaseExpired, got %v", i, err)
		}
	}
	var diag int
	if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = 'j_lo' AND event_type = 'DIAGNOSTIC_LATE_RESULT'`).Scan(&diag); err != nil || diag != 1 {
		t.Fatalf("%d diagnostic rows after %d racing late commits (%v), want exactly 1", diag, racers, err)
	}
	_, err := f.pool.Exec(ctx, `INSERT INTO job_events (job_id, attempt_id, event_type, details) VALUES ('j_lo', $1, 'DIAGNOSTIC_LATE_RESULT', jsonb_build_object('attempt_id', $1::text))`, f.att.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("a second diagnostic row for the same attempt must violate the unique index, got %v", err)
	}
}
