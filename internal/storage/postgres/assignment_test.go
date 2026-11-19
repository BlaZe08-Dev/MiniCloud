package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/storagetest"
)

// shiftTimestampsBack moves every stored timestamp of the schema back by d. The database clock
// cannot be moved, so this is how the PostgreSQL tests let time pass: every comparison the
// repository makes is "stored time against clock_timestamp()", and moving all stored times back by
// d is the same as moving the clock forward by d.
func shiftTimestampsBack(t *testing.T, pool *pgxpool.Pool, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE jobs SET created_at = created_at - $1::interval, updated_at = updated_at - $1::interval,
		        not_before = not_before - $1::interval, cancel_requested_at = cancel_requested_at - $1::interval,
		        assigned_at = assigned_at - $1::interval, assignment_expires_at = assignment_expires_at - $1::interval`,
		`UPDATE workers SET registered_at = registered_at - $1::interval, last_heartbeat_at = last_heartbeat_at - $1::interval,
		        last_poll_at = last_poll_at - $1::interval`,
		`UPDATE attempts SET started_at = started_at - $1::interval, finished_at = finished_at - $1::interval,
		        lease_expires_at = lease_expires_at - $1::interval`,
		`UPDATE results SET committed_at = committed_at - $1::interval`,
		`UPDATE job_events SET created_at = created_at - $1::interval`,
		`UPDATE assignment_pass_state SET last_completed_at = last_completed_at - $1::interval`,
		`UPDATE assignment_cursor SET updated_at = updated_at - $1::interval`,
	} {
		if _, err := pool.Exec(ctx, q, d); err != nil {
			t.Fatalf("shift timestamps back by %v: %v\n%s", d, err, q)
		}
	}
}

func dbNow(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("reading database time failed: %v", err)
	}
	return now
}

// holdPassLock takes the pass advisory key on its own connection and transaction, as a second
// coordinator's pass would, and returns the function that releases it.
func holdPassLock(t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, postgres.AssignmentPassLockID); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		t.Fatalf("hold pass lock: %v", err)
	}
	return func() {
		_ = tx.Rollback(ctx)
		conn.Release()
	}
}

func newPostgresAssignmentHarnessWithRepo(t *testing.T, opts ...postgres.Option) (storagetest.AssignmentHarness, *pgxpool.Pool, *postgres.Repository) {
	pool, _ := setupTestPool(t)
	repo := postgres.NewRepository(pool, opts...)
	ctx := context.Background()
	h := storagetest.AssignmentHarness{
		Repo:    repo,
		Now:     func(t *testing.T) time.Time { return dbNow(t, pool) },
		Advance: func(t *testing.T, d time.Duration) { shiftTimestampsBack(t, pool, d) },
		SetReservation: func(t *testing.T, jobID, workerID string, expiresIn time.Duration) {
			t.Helper()
			if _, err := pool.Exec(ctx, `
				UPDATE jobs SET assigned_worker_id = $2,
				       assignment_expires_at = c.n + $3::interval,
				       assigned_at = c.n + $3::interval - $4::interval
				FROM (SELECT clock_timestamp() AS n) c
				WHERE jobs.id = $1`, jobID, workerID, expiresIn, storagetest.AssignTestAcceptWindow); err != nil {
				t.Fatalf("set reservation %s -> %s: %v", jobID, workerID, err)
			}
		},
		SetLapses: func(t *testing.T, jobID string, lapses int) {
			t.Helper()
			if _, err := pool.Exec(ctx, `UPDATE jobs SET assignment_lapses = $2 WHERE id = $1`, jobID, lapses); err != nil {
				t.Fatalf("set lapses of %s: %v", jobID, err)
			}
		},
		SetRetrying: func(t *testing.T, jobID string, notBeforeIn time.Duration) {
			t.Helper()
			if _, err := pool.Exec(ctx, `UPDATE jobs SET state = 'RETRYING', not_before = clock_timestamp() + $2::interval WHERE id = $1`, jobID, notBeforeIn); err != nil {
				t.Fatalf("set %s RETRYING: %v", jobID, err)
			}
		},
		HoldPassLock: func(t *testing.T) func() { return holdPassLock(t, pool) },
		LastPollAt: func(t *testing.T, workerID string) *time.Time {
			t.Helper()
			var at *time.Time
			if err := pool.QueryRow(context.Background(), `SELECT last_poll_at FROM workers WHERE id = $1;`, workerID).Scan(&at); err != nil {
				t.Fatalf("read last_poll_at(%s): %v", workerID, err)
			}
			return at
		},
	}
	return h, pool, repo
}

func newPostgresAssignmentHarness(t *testing.T) storagetest.AssignmentHarness {
	h, _, _ := newPostgresAssignmentHarnessWithRepo(t)
	return h
}

// The shared slice 5a cases (ADR-0017 implementation note 6), run against PostgreSQL.

func TestConformance_Assign_CreatesNoAttempt(t *testing.T) {
	storagetest.RunAssignCreatesNoAttemptConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_ClaimAssigned_OnlyAssignedWithinWindow(t *testing.T) {
	storagetest.RunClaimAssignedOnlyAssignedWithinWindowConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_AssignmentLapse_ConsumesNoAttempt(t *testing.T) {
	storagetest.RunAssignmentLapseConsumesNoAttemptConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_AssignmentLapse_FallbackAfterLimit(t *testing.T) {
	storagetest.RunAssignmentLapseFallbackAfterLimitConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_CancelAssigned_NeverClaimed(t *testing.T) {
	storagetest.RunCancelAssignedNeverClaimedConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_OneReservationPerWorker(t *testing.T) {
	storagetest.RunAssignOneReservationPerWorkerConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_SkipsWorkerWithRunningAttempt(t *testing.T) {
	storagetest.RunAssignSkipsWorkerWithRunningAttemptConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_SkipsStaleWorkers(t *testing.T) {
	storagetest.RunAssignSkipsStaleWorkersConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_FifoPull_IgnoresAssignmentColumns(t *testing.T) {
	storagetest.RunFifoPullIgnoresAssignmentColumnsConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_AssignEvent_CapsCandidates(t *testing.T) {
	storagetest.RunAssignEventCapsCandidatesConformance(t, newPostgresAssignmentHarness)
}

// INV-1, PostgreSQL variant (ADR-0017 Section 6.3 name; implementation note 5).
func TestPostgresRepository_ClaimAssigned_ConcurrentPollsOneWinner(t *testing.T) {
	storagetest.RunClaimAssignedConcurrentPollsOneWinnerConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Claim_RespectsWorkerCapacity(t *testing.T) {
	storagetest.RunClaimRespectsWorkerCapacityConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_AssignPass_ExcludesJobAtLapseLimit(t *testing.T) {
	storagetest.RunAssignPassExcludesJobAtLapseLimitConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_ClaimLapseArm_ClosedBelowLimit(t *testing.T) {
	storagetest.RunClaimLapseArmClosedBelowLimitConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_AssignPass_UnhealthyRevokeCountsNoLapse(t *testing.T) {
	storagetest.RunAssignPassUnhealthyRevokeCountsNoLapseConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_ClaimOwnArm_ClosedAtExactExpiry(t *testing.T) {
	storagetest.RunClaimOwnArmClosedAtExactExpiryConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_ClaimAgeArm_UsesNotBefore(t *testing.T) {
	storagetest.RunClaimAgeArmUsesNotBeforeConformance(t, newPostgresAssignmentHarness)
}

// P4-08g H1: INV-16, the age arm needs a pass silent for FallbackAge.
func TestConformance_ClaimAgeArm_NeedsSilentPass(t *testing.T) {
	storagetest.RunClaimAgeArmNeedsSilentPassConformance(t, newPostgresAssignmentHarness)
}

// P4-08g: INV-3 on the fallback arm, and the revoke batch limit.
func TestConformance_ClaimFallback_LiveReservationOfOther(t *testing.T) {
	storagetest.RunClaimFallbackLiveReservationOfOtherConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_AssignRevoke_BatchLimit(t *testing.T) {
	storagetest.RunAssignRevokeBatchLimitConformance(t, newPostgresAssignmentHarness)
}

// 20 rounds, as in memory: each round is a fresh schema.
func TestConformance_AssignEvent_CandidatesInIDOrder(t *testing.T) {
	storagetest.RunAssignEventCandidatesInIDOrderConformance(t, newPostgresAssignmentHarness, 20)
}

func TestConformance_AssignPass_SkipsWhenPassLockHeld(t *testing.T) {
	storagetest.RunAssignPassSkipsWhenPassLockHeldConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_AssignEvent_CursorBeforeAdvancesWithinPass(t *testing.T) {
	storagetest.RunAssignEventCursorBeforeAdvancesWithinPassConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_ClaimFallback_ExpiredUnrevokedReservation(t *testing.T) {
	storagetest.RunClaimFallbackExpiredUnrevokedReservationConformance(t, newPostgresAssignmentHarness)
}

// INV-9 at the repository (slice 5c): grace revokes without counting the lapse, decided on the
// database time read with the job row lock.
func TestConformance_AssignPass_GraceRevokesWithoutCounting(t *testing.T) {
	storagetest.RunAssignPassGraceRevokesWithoutCountingConformance(t, newPostgresAssignmentHarness)
}

// Poll freshness throttle (ADR-0017 Section 2.2, implementation note 9), on the database clock.
func TestConformance_RecordPoll_ThrottlesFreshnessWrites(t *testing.T) {
	storagetest.RunRecordPollThrottlesFreshnessWritesConformance(t, newPostgresAssignmentHarness)
}

// ADR-0017 Sections 3.2 and 5.1: the policy and fallback reason per claim arm (slice 5c, part 2).
func TestConformance_ClaimPolicy_PerArm(t *testing.T) {
	storagetest.RunClaimPolicyPerArmConformance(t, newPostgresAssignmentHarness)
}

// ADR-0017 Section 3.2: a job that meets both fallback arms reports the lapse arm (current
// behaviour, not mandated by the ADR; review F-R2).
func TestConformance_ClaimPolicy_BothArms(t *testing.T) {
	storagetest.RunClaimPolicyBothArmsConformance(t, newPostgresAssignmentHarness)
}

// ADR-0017 Section 5.1: the four assignment fields of the job view (slice 5c, part 2).
func TestConformance_JobView_AssignmentFields(t *testing.T) {
	storagetest.RunJobViewAssignmentFieldsConformance(t, newPostgresAssignmentHarness)
}

// --- PostgreSQL-only invariants (ADR-0017 Section 6.3, slice 5b) ---

func assignmentCleared(t *testing.T, pool *pgxpool.Pool, jobID string) bool {
	t.Helper()
	var cleared bool
	if err := pool.QueryRow(context.Background(), `
		SELECT assigned_worker_id IS NULL AND assigned_at IS NULL AND assignment_expires_at IS NULL
		FROM jobs WHERE id = $1`, jobID).Scan(&cleared); err != nil {
		t.Fatalf("read assignment of %s: %v", jobID, err)
	}
	return cleared
}

func reservedTo(t *testing.T, pool *pgxpool.Pool, jobID string) string {
	t.Helper()
	var w *string
	if err := pool.QueryRow(context.Background(), `SELECT assigned_worker_id FROM jobs WHERE id = $1`, jobID).Scan(&w); err != nil {
		t.Fatalf("read assigned_worker_id of %s: %v", jobID, err)
	}
	if w == nil {
		return ""
	}
	return *w
}

// reserveViaPass submits jobID, makes workerID the only eligible worker and runs a pass, so the
// job carries a real reservation made by the pass.
func reserveViaPass(t *testing.T, h storagetest.AssignmentHarness, pool *pgxpool.Pool, jobID, workerID string) {
	t.Helper()
	storagetest.AssignPoll(t, h, workerID)
	storagetest.AssignRunPass(t, h)
	if got := reservedTo(t, pool, jobID); got != workerID {
		t.Fatalf("control: %s reserved to %q, want %q", jobID, got, workerID)
	}
}

// TestPostgresRepository_CancelAssigned_ClearsAssignment: INV-2b, every state-changing path clears
// the assignment columns, and chk_jobs_assignment_shape is the backstop for a path that forgets.
// One case per path that leaves QUEUED or RETRYING with a reservation (cancel of QUEUED, cancel of
// RETRYING, the round-robin claim, the fifo DispatchNextJob, DispatchJob by ID). Commit and sweep
// start from RUNNING, where the CHECK already forbids a reservation, so their cases assert that
// the columns stay NULL through them.
func TestPostgresRepository_CancelAssigned_ClearsAssignment(t *testing.T) {
	ctx := context.Background()

	t.Run("cancel_queued", func(t *testing.T) {
		h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
		storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
		storagetest.AssignSubmit(t, h, "job_1", "echo")
		reserveViaPass(t, h, pool, "job_1", "w_1")
		job, err := repo.CancelJob(ctx, "job_1")
		if err != nil {
			t.Fatalf("CancelJob of a reserved QUEUED job: %v", err)
		}
		if job.State != domain.StateCancelled || !assignmentCleared(t, pool, "job_1") {
			t.Fatalf("after cancel: state=%s cleared=%v", job.State, assignmentCleared(t, pool, "job_1"))
		}
	})

	t.Run("cancel_retrying", func(t *testing.T) {
		h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
		storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
		storagetest.AssignSubmit(t, h, "job_1", "echo")
		h.SetRetrying(t, "job_1", -time.Second)
		reserveViaPass(t, h, pool, "job_1", "w_1")
		job, err := repo.CancelJob(ctx, "job_1")
		if err != nil {
			t.Fatalf("CancelJob of a reserved RETRYING job: %v", err)
		}
		if job.State != domain.StateCancelled || !assignmentCleared(t, pool, "job_1") {
			t.Fatalf("after cancel: state=%s cleared=%v", job.State, assignmentCleared(t, pool, "job_1"))
		}
	})

	t.Run("round_robin_claim", func(t *testing.T) {
		h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
		storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
		storagetest.AssignSubmit(t, h, "job_1", "echo")
		reserveViaPass(t, h, pool, "job_1", "w_1")
		claim, err := repo.ClaimForPoll(ctx, "w_1", storagetest.AssignTestClaimOpts(false))
		if err != nil || claim.Policy != domain.DispatchPolicyRoundRobin {
			t.Fatalf("claim: %+v err=%v", claim, err)
		}
		if !assignmentCleared(t, pool, "job_1") {
			t.Fatalf("round-robin claim left the reservation on a RUNNING job")
		}
	})

	t.Run("fifo_dispatch_next_job", func(t *testing.T) {
		h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
		storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
		storagetest.AssignAddWorker(t, h, "w_2", 1, "echo")
		storagetest.AssignSubmit(t, h, "job_1", "echo")
		reserveViaPass(t, h, pool, "job_1", "w_1")
		h.SetLapses(t, "job_1", 2)
		if _, _, err := repo.DispatchNextJob(ctx, "w_2"); err != nil {
			t.Fatalf("DispatchNextJob of a job reserved to another worker: %v", err)
		}
		if !assignmentCleared(t, pool, "job_1") || storagetest.AssignJob(t, h, "job_1").AssignmentLapses != 0 {
			t.Fatalf("fifo claim left the reservation or the lapse count")
		}
	})

	t.Run("dispatch_job_by_id", func(t *testing.T) {
		h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
		storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
		storagetest.AssignAddWorker(t, h, "w_2", 1, "echo")
		storagetest.AssignSubmit(t, h, "job_1", "echo")
		reserveViaPass(t, h, pool, "job_1", "w_1")
		if _, err := repo.DispatchJob(ctx, "job_1", "w_2"); err != nil {
			t.Fatalf("DispatchJob of a reserved job: %v", err)
		}
		if !assignmentCleared(t, pool, "job_1") {
			t.Fatalf("DispatchJob left the reservation on a RUNNING job")
		}
	})

	t.Run("commit_and_sweep_keep_the_columns_null", func(t *testing.T) {
		h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
		storagetest.AssignAddWorker(t, h, "w_1", 2, "echo")
		storagetest.AssignSubmit(t, h, "job_c", "echo")
		storagetest.AssignSubmit(t, h, "job_s", "echo")
		_, attC, err := repo.DispatchNextJob(ctx, "w_1")
		if err != nil {
			t.Fatalf("dispatch job_c: %v", err)
		}
		_, attS, err := repo.DispatchNextJob(ctx, "w_1")
		if err != nil {
			t.Fatalf("dispatch job_s: %v", err)
		}
		if _, err := repo.CommitAttempt(ctx, "job_c", attC.ID, attC.LeaseToken, "w_1", domain.AttemptOutcome{ExitCode: 0, Output: "ok"}, nil); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE attempts SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, attS.ID); err != nil {
			t.Fatal(err)
		}
		res, err := repo.SweepExpiredLeases(ctx, domain.SweepParams{Backoff: func(int) time.Duration { return time.Second }})
		if err != nil || len(res.Swept) != 1 {
			t.Fatalf("sweep: %+v err=%v", res, err)
		}
		for _, id := range []string{"job_c", "job_s"} {
			if !assignmentCleared(t, pool, id) {
				t.Fatalf("%s carries a reservation after commit or sweep", id)
			}
		}
	})

	t.Run("backstop_check_rejects_running_with_a_reservation", func(t *testing.T) {
		h, pool, _ := newPostgresAssignmentHarnessWithRepo(t)
		storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
		storagetest.AssignSubmit(t, h, "job_1", "echo")
		reserveViaPass(t, h, pool, "job_1", "w_1")
		_, err := pool.Exec(ctx, `UPDATE jobs SET state = 'RUNNING' WHERE id = 'job_1'`)
		if pgCode(err) != "23514" {
			t.Fatalf("a transition to RUNNING that keeps the reservation: want check_violation 23514, got %v", err)
		}
	})
}

// TestPostgresRepository_AssignmentCursor_SurvivesRestart: INV-5, the rotation survives a
// coordinator restart, per task type. Two types, three workers that support both. The first pass,
// on one repository instance, moves the echo cursor to w_a and the sleep cursor to w_b. The jobs
// are cancelled, a new repository instance is built over the same database (a restart keeps
// nothing in process memory), and the next pass continues each type's own rotation: echo to w_b,
// sleep to w_c. Cursors reset on restart would give w_a and w_b again.
func TestPostgresRepository_AssignmentCursor_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
	for _, id := range []string{"w_a", "w_b", "w_c"} {
		storagetest.AssignAddWorker(t, h, id, 1, "echo", "sleep")
		storagetest.AssignPoll(t, h, id)
	}
	storagetest.AssignSubmit(t, h, "job_e1", "echo")
	storagetest.AssignSubmit(t, h, "job_s1", "sleep")
	storagetest.AssignRunPass(t, h)
	if e, s := reservedTo(t, pool, "job_e1"), reservedTo(t, pool, "job_s1"); e != "w_a" || s != "w_b" {
		t.Fatalf("control: first pass reserved echo->%q sleep->%q, want w_a and w_b", e, s)
	}
	for _, id := range []string{"job_e1", "job_s1"} {
		if _, err := repo.CancelJob(ctx, id); err != nil {
			t.Fatalf("CancelJob(%s): %v", id, err)
		}
	}

	restarted := postgres.NewRepository(pool)
	h.Repo = restarted
	storagetest.AssignSubmit(t, h, "job_e2", "echo")
	storagetest.AssignSubmit(t, h, "job_s2", "sleep")
	storagetest.AssignRunPass(t, h)
	if e, s := reservedTo(t, pool, "job_e2"), reservedTo(t, pool, "job_s2"); e != "w_b" || s != "w_c" {
		t.Fatalf("after restart: echo->%q sleep->%q, want w_b and w_c (each type continues its own rotation)", e, s)
	}
}

// TestPostgresRepository_AssignmentCursor_UpdatePathPersistsAcrossPasses: INV-5 on the cursor
// update path. The restart test above writes each type's cursor row once (the INSERT); this test
// makes the third pass of one type go through the upsert's UPDATE. Three passes over w_a, w_b, w_c
// with a restart before the third: the picks are w_a, w_b, w_c, and the stored row follows each
// pick (P4-08c review F-R4, mutant A01).
func TestPostgresRepository_AssignmentCursor_UpdatePathPersistsAcrossPasses(t *testing.T) {
	ctx := context.Background()
	h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
	for _, id := range []string{"w_a", "w_b", "w_c"} {
		storagetest.AssignAddWorker(t, h, id, 1, "echo")
		storagetest.AssignPoll(t, h, id)
	}
	cursorRow := func() string {
		var w *string
		if err := pool.QueryRow(ctx, `SELECT last_worker_id FROM assignment_cursor WHERE task_type = 'echo'`).Scan(&w); err != nil || w == nil {
			t.Fatalf("cursor row: %v", err)
		}
		return *w
	}
	for i, want := range []string{"w_a", "w_b", "w_c"} {
		if i == 2 {
			h.Repo = postgres.NewRepository(pool) // restart before the third pass
		}
		id := []string{"job_1", "job_2", "job_3"}[i]
		storagetest.AssignSubmit(t, h, id, "echo")
		storagetest.AssignRunPass(t, h)
		if got := reservedTo(t, pool, id); got != want {
			t.Fatalf("pass %d reserved %s to %q, want %q", i+1, id, got, want)
		}
		if c := cursorRow(); c != want {
			t.Fatalf("pass %d left the stored cursor at %q, want %q", i+1, c, want)
		}
		if _, err := repo.CancelJob(ctx, id); err != nil {
			t.Fatalf("CancelJob(%s): %v", id, err)
		}
	}
}

// TestPostgresRepository_Assign_SkipsWorkerLockedByClaim: INV-10b, the eligibility re-check runs
// under the worker row lock. A second connection holds w_1's row lock, as an in-flight claim does.
// The pass, with a 300 ms lock_timeout, must not reserve w_1, must not block past the timeout,
// must report no error (no 40P01), and moves the decision on to w_2. After the lock is released, a
// pass reserves w_1 (control).
func TestPostgresRepository_Assign_SkipsWorkerLockedByClaim(t *testing.T) {
	ctx := context.Background()
	h, pool, repo := newPostgresAssignmentHarnessWithRepo(t, postgres.WithSweepLockTimeout(300*time.Millisecond))
	storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
	storagetest.AssignAddWorker(t, h, "w_2", 1, "echo")
	storagetest.AssignPoll(t, h, "w_1")
	storagetest.AssignPoll(t, h, "w_2")
	storagetest.AssignSubmit(t, h, "job_1", "echo")
	storagetest.AssignSubmit(t, h, "job_2", "echo")

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var released bool
	defer func() {
		if !released {
			_ = holder.Rollback(ctx)
		}
	}()
	if _, err := holder.Exec(ctx, `SELECT id FROM workers WHERE id = 'w_1' FOR UPDATE`); err != nil {
		t.Fatalf("hold w_1: %v", err)
	}

	passCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	res, err := repo.AssignQueuedJobs(passCtx, storagetest.AssignTestParams())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("pass with w_1 locked: err=%v (pg code %q)", err, pgCode(err))
	}
	if elapsed > 3*time.Second {
		t.Fatalf("pass took %v with a 300ms lock_timeout: it blocked on the worker row", elapsed)
	}
	if got := reservedTo(t, pool, "job_1"); got != "w_2" {
		t.Fatalf("job_1 reserved to %q while w_1's row was locked; want w_2", got)
	}
	if got := reservedTo(t, pool, "job_2"); got != "" {
		t.Fatalf("job_2 reserved to %q; w_1 is skipped for the rest of the pass and w_2 holds one", got)
	}
	if res.Reserved != 1 {
		t.Fatalf("reserved %d, want 1", res.Reserved)
	}

	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	released = true
	storagetest.AssignRunPass(t, h)
	if got := reservedTo(t, pool, "job_2"); got != "w_1" {
		t.Fatalf("control: after release job_2 reserved to %q, want w_1", got)
	}
}

// TestPostgresRepository_AssignmentPass_NoDeadlockWithClaim: INV-13, the pass keeps the lock order
// jobs -> workers and never waits on a job, so it cannot deadlock with a claim. A second
// connection plays a claim of job_b by w_1: it locks job_b first, and then, while the pass runs,
// asks for w_1's row. The pass reserves job_a (echo) to w_1, holding w_1's row, and then reads the
// sleep candidates; job_b is locked, so the pass must skip it, not wait for it. The test asserts
// that neither side gets 40P01 and both finish. The lock_timeout is 3 s, above PostgreSQL's
// default deadlock_timeout of 1 s, so a cycle would be reported as 40P01 before the timeout.
func TestPostgresRepository_AssignmentPass_NoDeadlockWithClaim(t *testing.T) {
	ctx := context.Background()
	h, pool, repo := newPostgresAssignmentHarnessWithRepo(t, postgres.WithSweepLockTimeout(3*time.Second))
	storagetest.AssignAddWorker(t, h, "w_1", 1, "echo", "sleep")
	storagetest.AssignPoll(t, h, "w_1")
	storagetest.AssignSubmit(t, h, "job_a", "echo")
	storagetest.AssignSubmit(t, h, "job_b", "sleep")

	claim, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Rollback(ctx)
	if _, err := claim.Exec(ctx, `SELECT set_config('lock_timeout', '5s', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := claim.Exec(ctx, `SELECT id FROM jobs WHERE id = 'job_b' FOR UPDATE`); err != nil {
		t.Fatalf("claim locks job_b: %v", err)
	}

	type out struct {
		res storage.AssignResult
		err error
	}
	done := make(chan out, 1)
	passCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	go func() {
		res, err := repo.AssignQueuedJobs(passCtx, storagetest.AssignTestParams())
		done <- out{res, err}
	}()
	time.Sleep(300 * time.Millisecond)
	_, claimErr := claim.Exec(ctx, `SELECT id FROM workers WHERE id = 'w_1' FOR UPDATE`)
	_ = claim.Rollback(ctx)
	got := <-done

	if pgCode(claimErr) == "40P01" || pgCode(got.err) == "40P01" {
		t.Fatalf("deadlock between the pass and a claim: claim err=%v, pass err=%v", claimErr, got.err)
	}
	if claimErr != nil {
		t.Fatalf("claim's worker lock failed: %v (pg code %q)", claimErr, pgCode(claimErr))
	}
	if got.err != nil {
		t.Fatalf("pass failed: %v (pg code %q)", got.err, pgCode(got.err))
	}
	if r := reservedTo(t, pool, "job_a"); r != "w_1" {
		t.Fatalf("control: job_a reserved to %q, want w_1", r)
	}
	if r := reservedTo(t, pool, "job_b"); r != "" {
		t.Fatalf("job_b, locked by the claim, was reserved to %q", r)
	}
}

// TestPostgresRepository_AssignmentPass_ConcurrentMixedOperations: INV-13 under concurrent mixed
// operations. Passes, round-robin claims, fifo claims, commits, cancels, forced lease expiry and
// sweeps run at once over two task types and four workers that support both. No operation may
// fail with 40P01 or with an unexpected error, and afterwards no job has more than one active
// attempt and no worker holds more than one live reservation.
func TestPostgresRepository_AssignmentPass_ConcurrentMixedOperations(t *testing.T) {
	ctx := context.Background()
	h, pool, repo := newPostgresAssignmentHarnessWithRepo(t, postgres.WithSweepLockTimeout(3*time.Second))
	workers := []string{"w_a", "w_b", "w_c", "w_d"}
	for _, id := range workers {
		storagetest.AssignAddWorker(t, h, id, 1, "echo", "sleep")
		storagetest.AssignPoll(t, h, id)
	}
	const jobs = 60
	var jobIDs []string
	for i := 0; i < jobs; i++ {
		ty := "echo"
		if i%2 == 1 {
			ty = "sleep"
		}
		id := fmt.Sprintf("job_%02d", i)
		storagetest.AssignSubmit(t, h, id, ty)
		jobIDs = append(jobIDs, id)
	}
	// Some jobs reach the lapse arm, so fallback claims run as well.
	for i := 0; i < jobs; i += 5 {
		h.SetLapses(t, jobIDs[i], storagetest.AssignTestMaxLapses)
	}

	var (
		mu       sync.Mutex
		failures []string
	)
	fail := func(op string, err error) {
		mu.Lock()
		defer mu.Unlock()
		failures = append(failures, fmt.Sprintf("%s: %v (pg code %q)", op, err, pgCode(err)))
	}
	allowed := func(err error) bool {
		for _, e := range []error{domain.ErrNoQueuedJobs, domain.ErrWorkerNoCapacity, domain.ErrJobAlreadyTerminal,
			domain.ErrStaleAttempt, domain.ErrLeaseExpired, domain.ErrJobNotQueued} {
			if errors.Is(err, e) {
				return true
			}
		}
		return false
	}
	backoff := func(int) time.Duration { return 10 * time.Millisecond }

	var wg sync.WaitGroup
	stop := make(chan struct{})
	// The assignment pass, back to back.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := repo.AssignQueuedJobs(ctx, storagetest.AssignTestParams()); err != nil {
				fail("pass", err)
			}
		}
	}()
	// Sweeper and forced lease expiry.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := pool.Exec(ctx, `
				UPDATE attempts SET lease_expires_at = clock_timestamp() - interval '1 second'
				WHERE id IN (SELECT id FROM attempts WHERE state = 'RUNNING' ORDER BY random() LIMIT 1)`); err != nil {
				fail("expire", err)
			}
			if _, err := repo.SweepExpiredLeases(ctx, domain.SweepParams{Backoff: backoff}); err != nil {
				fail("sweep", err)
			}
		}
	}()
	// Canceller.
	wg.Add(1)
	go func() {
		defer wg.Done()
		rnd := rand.New(rand.NewSource(7))
		for i := 0; i < 15; i++ {
			if _, err := repo.CancelJob(ctx, jobIDs[rnd.Intn(jobs)]); err != nil && !allowed(err) {
				fail("cancel", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	// Workers: poll, claim (round-robin; w_d uses fifo), commit.
	var workersWG sync.WaitGroup
	for wi, id := range workers {
		workersWG.Add(1)
		go func(wi int, id string) {
			defer workersWG.Done()
			rnd := rand.New(rand.NewSource(int64(wi + 1)))
			for i := 0; i < 40; i++ {
				if err := repo.RecordHeartbeat(ctx, id, 0); err != nil {
					fail("heartbeat "+id, err)
				}
				if err := repo.RecordPoll(ctx, id); err != nil {
					fail("poll "+id, err)
				}
				var jobID string
				var att *domain.Attempt
				if id == "w_d" {
					j, a, err := repo.DispatchNextJob(ctx, id)
					if err != nil {
						if !allowed(err) {
							fail("fifo "+id, err)
						}
						continue
					}
					jobID, att = j.ID, a
				} else {
					c, err := repo.ClaimForPoll(ctx, id, storagetest.AssignTestClaimOpts(rnd.Intn(2) == 0))
					if err != nil {
						if !allowed(err) {
							fail("claim "+id, err)
						}
						continue
					}
					jobID, att = c.Job.ID, c.Attempt
				}
				outcome := domain.AttemptOutcome{ExitCode: 0, Output: "ok"}
				if rnd.Intn(3) == 0 {
					outcome = domain.AttemptOutcome{ExitCode: 1, Error: "boom", ErrorClassification: domain.ClassTransient}
				}
				if _, err := repo.CommitAttempt(ctx, jobID, att.ID, att.LeaseToken, id, outcome, backoff); err != nil && !allowed(err) {
					fail("commit "+id, err)
				}
			}
		}(wi, id)
	}
	workersWG.Wait()
	close(stop)
	wg.Wait()

	for _, f := range failures {
		t.Errorf("concurrent operation failed: %s", f)
	}
	var doubleActive, doubleReserved int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (SELECT job_id FROM attempts WHERE state IN ('PENDING', 'RUNNING') GROUP BY job_id HAVING COUNT(*) > 1) x`).Scan(&doubleActive); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (SELECT assigned_worker_id FROM jobs WHERE assignment_expires_at > clock_timestamp() GROUP BY assigned_worker_id HAVING COUNT(*) > 1) x`).Scan(&doubleReserved); err != nil {
		t.Fatal(err)
	}
	if doubleActive != 0 || doubleReserved != 0 {
		t.Fatalf("after the run: %d jobs with two active attempts, %d workers with two live reservations", doubleActive, doubleReserved)
	}
	var claimed int
	if err := pool.QueryRow(ctx, `SELECT COUNT(DISTINCT job_id) FROM attempts`).Scan(&claimed); err != nil || claimed == 0 {
		t.Fatalf("control: no job was claimed during the run (err=%v)", err)
	}
}

// assignmentSnapshot renders every row a pass can write: the heartbeat, the cursors, the job
// assignment columns and the event count.
func assignmentSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var hb, cursors, jobs string
	var events int
	err := pool.QueryRow(context.Background(), `
		SELECT (SELECT last_completed_at::text FROM assignment_pass_state),
		       (SELECT COALESCE(string_agg(task_type || ':' || COALESCE(last_worker_id, ''), ',' ORDER BY task_type), '') FROM assignment_cursor),
		       (SELECT COALESCE(string_agg(id || ':' || state || ':' || COALESCE(assigned_worker_id, '') || ':' ||
		               COALESCE(assignment_expires_at::text, '') || ':' || assignment_lapses, ',' ORDER BY id), '') FROM jobs),
		       (SELECT COUNT(*) FROM job_events)`).Scan(&hb, &cursors, &jobs, &events)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return fmt.Sprintf("heartbeat=%s cursors=[%s] jobs=[%s] events=%d", hb, cursors, jobs, events)
}

// TestPostgresRepository_AssignmentPass_SecondPassSkips: INV-22, a pass that finds the advisory
// key held returns Skipped at once and writes nothing. The schema holds work for every step of a
// pass: an expired reservation to lapse, an unassigned job and an eligible worker to reserve, and
// a cursor and a heartbeat to move. A second connection holds the key; the pass runs under a 1 s
// context deadline. After the release a pass does write (control).
func TestPostgresRepository_AssignmentPass_SecondPassSkips(t *testing.T) {
	ctx := context.Background()
	h, pool, repo := newPostgresAssignmentHarnessWithRepo(t)
	storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
	storagetest.AssignAddWorker(t, h, "w_2", 1, "echo")
	storagetest.AssignSubmit(t, h, "job_1", "echo")
	reserveViaPass(t, h, pool, "job_1", "w_1")
	storagetest.AssignSubmit(t, h, "job_2", "echo")
	storagetest.AssignSubmit(t, h, "job_3", "echo")
	h.SetReservation(t, "job_3", "w_1", -time.Second)
	storagetest.AssignPoll(t, h, "w_2")
	h.Advance(t, time.Second) // the heartbeat a pass would write is then visibly newer

	before := assignmentSnapshot(t, pool)
	release := holdPassLock(t, pool)
	passCtx, cancel := context.WithTimeout(ctx, time.Second)
	res, err := repo.AssignQueuedJobs(passCtx, storagetest.AssignTestParams())
	cancel()
	if err != nil || !res.Skipped {
		release()
		t.Fatalf("pass with the advisory key held: res=%+v err=%v, want Skipped within 1s", res, err)
	}
	if after := assignmentSnapshot(t, pool); after != before {
		release()
		t.Fatalf("skipped pass wrote rows:\nbefore %s\nafter  %s", before, after)
	}
	release()

	if res := storagetest.AssignRunPass(t, h); res.Skipped || res.Lapsed != 1 || res.Reserved != 1 {
		t.Fatalf("control: pass after release: %+v", res)
	}
	if after := assignmentSnapshot(t, pool); after == before {
		t.Fatalf("control: the pass after release wrote nothing")
	}
}

// TestPostgresRepository_AssignmentPass_HeartbeatIsCompletionTime: the pass heartbeat is the
// completion time of the pass (ADR-0017 Section 3.2, implementation note 8 as amended in fix round
// 1): the last statement writes clock_timestamp(), not now(), which is the transaction start. A
// second connection holds the only eligible worker's row, so the pass waits out its 300 ms
// lock_timeout inside its transaction before it writes the heartbeat. The heartbeat must therefore
// be at least 250 ms after the database time read before the pass began, and not earlier than the
// last event the pass wrote. With now(), it would be the transaction start, a few milliseconds
// after that time.
func TestPostgresRepository_AssignmentPass_HeartbeatIsCompletionTime(t *testing.T) {
	ctx := context.Background()
	h, pool, repo := newPostgresAssignmentHarnessWithRepo(t, postgres.WithSweepLockTimeout(300*time.Millisecond))
	storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
	storagetest.AssignPoll(t, h, "w_1")
	storagetest.AssignSubmit(t, h, "job_1", "echo")
	// An expired reservation gives the pass an event to write before it waits on the lock.
	storagetest.AssignSubmit(t, h, "job_0", "echo")
	h.SetReservation(t, "job_0", "w_1", -time.Second)

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `SELECT id FROM workers WHERE id = 'w_1' FOR UPDATE`); err != nil {
		t.Fatalf("hold w_1: %v", err)
	}

	before := dbNow(t, pool)
	res, err := repo.AssignQueuedJobs(ctx, storagetest.AssignTestParams())
	if err != nil || res.Skipped || res.Lapsed != 1 {
		t.Fatalf("pass: %+v err=%v; want one lapse and no skip", res, err)
	}
	var heartbeat, lastEvent time.Time
	if err := pool.QueryRow(ctx, `SELECT last_completed_at FROM assignment_pass_state`).Scan(&heartbeat); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT MAX(created_at) FROM job_events WHERE event_type = 'JOB_ASSIGNMENT_LAPSED'`).Scan(&lastEvent); err != nil {
		t.Fatal(err)
	}
	if gap := heartbeat.Sub(before); gap < 250*time.Millisecond {
		t.Fatalf("heartbeat is %v after the pre-pass time; the pass waited about 300ms first, so the heartbeat is not its completion time", gap)
	}
	if heartbeat.Before(lastEvent) {
		t.Fatalf("heartbeat %v is earlier than the lapse event %v the same pass wrote", heartbeat, lastEvent)
	}
}

// TestPostgresRepository_DeleteWorkerWithReservation_Rejected: open question 5 of the P4-08c
// handoff. jobs.assigned_worker_id references workers ON DELETE SET NULL, which sets only that
// column, so chk_jobs_assignment_shape must reject the cascade and with it the DELETE. Afterwards
// the reservation is intact. Control: a worker without a reservation can be deleted.
func TestPostgresRepository_DeleteWorkerWithReservation_Rejected(t *testing.T) {
	ctx := context.Background()
	h, pool, _ := newPostgresAssignmentHarnessWithRepo(t)
	storagetest.AssignAddWorker(t, h, "w_1", 1, "echo")
	storagetest.AssignAddWorker(t, h, "w_free", 1, "echo")
	storagetest.AssignSubmit(t, h, "job_1", "echo")
	reserveViaPass(t, h, pool, "job_1", "w_1")
	var expiresBefore time.Time
	if err := pool.QueryRow(ctx, `SELECT assignment_expires_at FROM jobs WHERE id = 'job_1'`).Scan(&expiresBefore); err != nil {
		t.Fatal(err)
	}

	_, err := pool.Exec(ctx, `DELETE FROM workers WHERE id = 'w_1'`)
	if err == nil {
		t.Fatalf("DEFECT: deleting a worker that holds a reservation succeeded")
	}
	if code := pgCode(err); code != "23514" && code != "23503" {
		t.Fatalf("delete of a reserved worker: want a constraint rejection (23514 or 23503), got %v (code %q)", err, code)
	}
	t.Logf("delete rejected: %v (code %s)", err, pgCode(err))

	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workers WHERE id = 'w_1')`).Scan(&exists); err != nil || !exists {
		t.Fatalf("worker w_1 gone after a rejected delete (err=%v)", err)
	}
	var expiresAfter time.Time
	var assignedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT assignment_expires_at, assigned_at FROM jobs WHERE id = 'job_1'`).Scan(&expiresAfter, &assignedAt); err != nil {
		t.Fatal(err)
	}
	if got := reservedTo(t, pool, "job_1"); got != "w_1" || assignedAt == nil || !expiresAfter.Equal(expiresBefore) {
		t.Fatalf("reservation changed by the rejected delete: worker=%q assigned_at=%v expires %v -> %v", got, assignedAt, expiresBefore, expiresAfter)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM workers WHERE id = 'w_free'`); err != nil {
		t.Fatalf("control: deleting a worker without a reservation: %v", err)
	}
}

// ADR-0020 (V2-0b): the round-robin pass and the fallback claim use the worker's real slots.

func TestConformance_Assign_ReservationsUpToDeclaredSlots(t *testing.T) {
	storagetest.RunAssignReservationsUpToDeclaredSlotsConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_ActiveAttemptsCountAgainstSlots(t *testing.T) {
	storagetest.RunAssignActiveAttemptsCountAgainstSlotsConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_ReservationCapPerWorker(t *testing.T) {
	storagetest.RunAssignReservationCapPerWorkerConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_ExpiredReservationDoesNotHoldSlot(t *testing.T) {
	storagetest.RunAssignExpiredReservationDoesNotHoldSlotConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_UndeclaredFourSlotWorkerKeepsOneSlot(t *testing.T) {
	storagetest.RunAssignUndeclaredFourSlotWorkerKeepsOneSlotConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_RotationWithMultiSlotWorkers(t *testing.T) {
	storagetest.RunAssignRotationWithMultiSlotWorkersConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Claim_FallbackRespectsReservedSlots(t *testing.T) {
	storagetest.RunClaimFallbackRespectsReservedSlotsConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Assign_ReRegisterLowerSlots_NoNewReservations(t *testing.T) {
	storagetest.RunAssignReRegisterLowerSlotsNoNewReservationsConformance(t, newPostgresAssignmentHarness)
}

func TestConformance_Worker_ExecutesConcurrentlyRoundTrip(t *testing.T) {
	storagetest.RunWorkerExecutesConcurrentlyRoundTripConformance(t, newPostgresAssignmentHarness(t).Repo)
}

// TestPostgresRepository_Assign_RecheckUsesSlotRule: ADR-0020 Section 2.7, the re-check under the
// worker row lock applies the real-slot rule, not only the snapshot. w_1 does not declare
// concurrency and has total_slots 2, so it has one assignable slot. A second connection (an
// in-flight claim) holds w_1's row and inserts a RUNNING attempt for it, uncommitted. The pass's
// snapshot therefore sees w_1 eligible and waits for the row. The connection commits while the pass
// waits; the re-check must then see A = 1 of 1 assignable slot and reserve nothing. A re-check
// that kept only total_slots - A > 0 would reserve job_1 to w_1 (1 of 2 slots used).
func TestPostgresRepository_Assign_RecheckUsesSlotRule(t *testing.T) {
	ctx := context.Background()
	h, pool, repo := newPostgresAssignmentHarnessWithRepo(t, postgres.WithSweepLockTimeout(5*time.Second))
	storagetest.AssignAddWorker(t, h, "w_1", 2, "echo")
	storagetest.AssignPoll(t, h, "w_1")
	storagetest.AssignSubmit(t, h, "job_1", "echo")
	if _, err := pool.Exec(ctx, `INSERT INTO jobs (id, task_type, payload, state) VALUES ('job_run', 'echo', '\x00', 'RUNNING')`); err != nil {
		t.Fatalf("insert job_run: %v", err)
	}

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT id FROM workers WHERE id = 'w_1' FOR UPDATE`); err != nil {
		t.Fatalf("hold w_1: %v", err)
	}
	if _, err := holder.Exec(ctx, `
		INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state, lease_expires_at)
		VALUES ('att_run_1', 'job_run', 1, 'w_1', repeat('a', 64), 'RUNNING', clock_timestamp() + INTERVAL '30 seconds')`); err != nil {
		t.Fatalf("insert the in-flight attempt: %v", err)
	}
	var holderPID int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}

	type passOut struct {
		res storage.AssignResult
		err error
	}
	done := make(chan passOut, 1)
	go func() {
		res, err := repo.AssignQueuedJobs(ctx, storagetest.AssignTestParams())
		done <- passOut{res, err}
	}()

	// Wait until the pass is blocked on w_1's row (it took its snapshot first), then commit.
	deadline := time.Now().Add(4 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND $1 = ANY(pg_blocking_pids(pid))`, holderPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case out := <-done:
			t.Fatalf("pass finished without waiting on w_1's row: res=%+v err=%v (the snapshot did not see w_1 as eligible)", out.res, out.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the pass never waited on w_1's row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit the in-flight attempt: %v", err)
	}

	out := <-done
	if out.err != nil {
		t.Fatalf("pass: %v", out.err)
	}
	if got := reservedTo(t, pool, "job_1"); got != "" {
		t.Fatalf("job_1 reserved to %q after the re-check; w_1 has 1 RUNNING attempt of 1 assignable slot", got)
	}
	if out.res.Reserved != 0 {
		t.Fatalf("pass reserved %d, want 0", out.res.Reserved)
	}
}
