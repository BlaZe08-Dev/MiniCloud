package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// Assignment parameters used by these tests: the ADR-0017 defaults for the accept window (5 s),
// the worker timeout (MINICLOUD_WORKER_TIMEOUT default, 10 s) and the batch limit (50).
const (
	testAcceptWindow  = 5 * time.Second
	testWorkerTimeout = 10 * time.Second
	testBatchLimit    = 50
	testFallbackAge   = 30 * time.Second
	testMaxLapses     = 3
)

// assignClock is an injectable clock for the in-memory repository.
type assignClock struct {
	mu  sync.Mutex
	now time.Time
}

func newAssignClock() *assignClock {
	return &assignClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
}

func (c *assignClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *assignClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newAssignRepo(t *testing.T) (*storage.InMemoryRepository, *assignClock) {
	t.Helper()
	clk := newAssignClock()
	return storage.NewInMemoryRepository(storage.WithClock(clk)), clk
}

func addAssignWorker(t *testing.T, repo *storage.InMemoryRepository, clk *assignClock, id string, slots int, tasks ...string) {
	t.Helper()
	err := repo.UpsertWorker(context.Background(), &domain.Worker{
		ID:              id,
		Version:         "v1.0.0",
		TotalSlots:      slots,
		Status:          domain.WorkerStatusHealthy,
		SupportedTasks:  tasks,
		LastHeartbeatAt: clk.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertWorker(%s): %v", id, err)
	}
}

// pollAssign records a poll and a heartbeat for a worker, so it is eligible for a reservation.
func pollAssign(t *testing.T, repo *storage.InMemoryRepository, clk *assignClock, id string) {
	t.Helper()
	ctx := context.Background()
	if err := repo.RecordHeartbeat(ctx, id, 0); err != nil {
		t.Fatalf("RecordHeartbeat(%s): %v", id, err)
	}
	if err := repo.RecordPoll(ctx, id); err != nil {
		t.Fatalf("RecordPoll(%s): %v", id, err)
	}
}

func submitAssign(t *testing.T, repo *storage.InMemoryRepository, id, taskType string) {
	t.Helper()
	err := repo.SubmitJob(context.Background(), &domain.Job{
		ID:             id,
		TaskType:       taskType,
		Payload:        []byte("{}"),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob(%s): %v", id, err)
	}
}

func assignParams() storage.AssignParams {
	return storage.AssignParams{AcceptWindow: testAcceptWindow, WorkerTimeout: testWorkerTimeout, BatchLimit: testBatchLimit, MaxLapses: testMaxLapses}
}

func runPass(t *testing.T, repo *storage.InMemoryRepository) storage.AssignResult {
	t.Helper()
	res, err := repo.AssignQueuedJobs(context.Background(), assignParams())
	if err != nil {
		t.Fatalf("AssignQueuedJobs: %v", err)
	}
	return res
}

func claimOpts(ageArm bool) storage.ClaimOptions {
	return storage.ClaimOptions{MaxLapses: testMaxLapses, FallbackAge: testFallbackAge, AgeArmOpen: ageArm}
}

func mustJob(t *testing.T, repo *storage.InMemoryRepository, id string) *domain.Job {
	t.Helper()
	job, err := repo.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s): %v", id, err)
	}
	return job
}

// setReservation writes a reservation directly, bypassing the pass. It is the only seam these
// tests use for states the pass would not produce on its own, such as a reservation that is
// older than the fallback age while the pass is silent.
func setReservation(t *testing.T, repo *storage.InMemoryRepository, jobID, workerID string, expires time.Time) {
	t.Helper()
	job := mustJob(t, repo, jobID)
	w := workerID
	job.AssignedWorkerID = &w
	at := expires.Add(-testAcceptWindow)
	job.AssignedAt = &at
	job.AssignmentExpiresAt = &expires
	if err := repo.UpdateJob(context.Background(), job); err != nil {
		t.Fatalf("UpdateJob(%s): %v", jobID, err)
	}
}

func countEventsOfType(t *testing.T, repo *storage.InMemoryRepository, jobID string, typ domain.JobEventType) int {
	t.Helper()
	events, err := repo.ListEventsByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ListEventsByJob(%s): %v", jobID, err)
	}
	n := 0
	for _, e := range events {
		if e.EventType == typ {
			n++
		}
	}
	return n
}

// INV-2: an assignment reserves only. It creates no attempt and changes no attempt number.
func TestConformance_Assign_CreatesNoAttempt(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_a", 1, "echo")
	submitAssign(t, repo, "job_a", "echo")
	pollAssign(t, repo, clk, "w_a")

	before, err := repo.ListAttemptsByJob(ctx, "job_a")
	if err != nil {
		t.Fatalf("ListAttemptsByJob: %v", err)
	}
	res := runPass(t, repo)
	after, err := repo.ListAttemptsByJob(ctx, "job_a")
	if err != nil {
		t.Fatalf("ListAttemptsByJob: %v", err)
	}
	if res.Reserved != 1 {
		t.Fatalf("expected one reservation, got %d", res.Reserved)
	}
	if len(before) != 0 || len(after) != 0 {
		t.Fatalf("assignment created attempts: before=%d after=%d", len(before), len(after))
	}
	job := mustJob(t, repo, "job_a")
	if job.AssignedWorkerID == nil || *job.AssignedWorkerID != "w_a" {
		t.Fatalf("job not reserved to w_a: %+v", job.AssignedWorkerID)
	}
	if job.State != domain.StateQueued {
		t.Fatalf("assignment changed job state to %s", job.State)
	}
}

// INV-3: only the assigned worker, inside the window, gets its own reservation. A poll never
// claims a live reservation of another worker, and own reservation is preferred to fallback.
func TestConformance_ClaimAssigned_OnlyAssignedWithinWindow(t *testing.T) {
	t.Run("wrong worker gets no live reservation", func(t *testing.T) {
		repo, clk := newAssignRepo(t)
		addAssignWorker(t, repo, clk, "w_1", 1, "echo")
		addAssignWorker(t, repo, clk, "w_2", 1, "echo")
		submitAssign(t, repo, "job_1", "echo")
		setReservation(t, repo, "job_1", "w_1", clk.Now().Add(testAcceptWindow))
		if _, err := repo.ClaimForPoll(context.Background(), "w_2", claimOpts(false)); !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("w_2 claimed w_1's live reservation: err=%v", err)
		}
	})

	t.Run("expired reservation with arms unmet gives 204 to its own worker", func(t *testing.T) {
		repo, clk := newAssignRepo(t)
		addAssignWorker(t, repo, clk, "w_1", 1, "echo")
		submitAssign(t, repo, "job_1", "echo")
		setReservation(t, repo, "job_1", "w_1", clk.Now().Add(-time.Millisecond))
		if _, err := repo.ClaimForPoll(context.Background(), "w_1", claimOpts(false)); !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("expired own reservation was claimed with arms unmet: err=%v", err)
		}
	})

	t.Run("own reservation beats an older job on the fallback age arm", func(t *testing.T) {
		repo, clk := newAssignRepo(t)
		addAssignWorker(t, repo, clk, "w_1", 1, "echo")
		submitAssign(t, repo, "job_old", "echo")
		clk.Advance(testFallbackAge + time.Second)
		submitAssign(t, repo, "job_own", "echo")
		setReservation(t, repo, "job_own", "w_1", clk.Now().Add(testAcceptWindow))
		claim, err := repo.ClaimForPoll(context.Background(), "w_1", claimOpts(true))
		if err != nil {
			t.Fatalf("ClaimForPoll: %v", err)
		}
		if claim.Job.ID != "job_own" || claim.Policy != domain.DispatchPolicyRoundRobin {
			t.Fatalf("claimed %s under %s; want job_own under round_robin", claim.Job.ID, claim.Policy)
		}
	})
}

// INV-7a: a lapse consumes no attempt. attempt_no and the attempt list are unchanged.
func TestConformance_AssignmentLapse_ConsumesNoAttempt(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	submitAssign(t, repo, "job_1", "echo")
	pollAssign(t, repo, clk, "w_1")
	runPass(t, repo)

	clk.Advance(testAcceptWindow + time.Millisecond)
	pollAssign(t, repo, clk, "w_1")
	res := runPass(t, repo)
	if res.Lapsed != 1 {
		t.Fatalf("expected one counted lapse, got %d", res.Lapsed)
	}
	job := mustJob(t, repo, "job_1")
	if job.AssignmentLapses != 1 {
		t.Fatalf("assignment_lapses = %d, want 1", job.AssignmentLapses)
	}
	attempts, err := repo.ListAttemptsByJob(ctx, "job_1")
	if err != nil {
		t.Fatalf("ListAttemptsByJob: %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("lapse created %d attempts", len(attempts))
	}
	if job.CurrentAttemptID != "" {
		t.Fatalf("lapse set current attempt %q", job.CurrentAttemptID)
	}
}

// INV-7b: after MINICLOUD_ASSIGNMENT_MAX_LAPSES counted lapses the job is claimable by any
// healthy polling worker, with reason lapses.
func TestConformance_AssignmentLapse_FallbackAfterLimit(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	addAssignWorker(t, repo, clk, "w_2", 1, "echo")
	submitAssign(t, repo, "job_1", "echo")

	pollAssign(t, repo, clk, "w_1")
	runPass(t, repo)
	for i := 1; i <= testMaxLapses; i++ {
		clk.Advance(testAcceptWindow + time.Millisecond)
		if i < testMaxLapses {
			// Keep w_1 eligible so the lapse pass reserves it again; the last lapse leaves the
			// job unreserved because w_1 is no longer fresh.
			pollAssign(t, repo, clk, "w_1")
		}
		runPass(t, repo)
	}
	job := mustJob(t, repo, "job_1")
	if job.AssignmentLapses != testMaxLapses {
		t.Fatalf("assignment_lapses = %d, want %d", job.AssignmentLapses, testMaxLapses)
	}
	if job.AssignedWorkerID != nil {
		t.Fatalf("job still reserved after the lapse limit")
	}

	pollAssign(t, repo, clk, "w_2")
	claim, err := repo.ClaimForPoll(ctx, "w_2", claimOpts(false))
	if err != nil {
		t.Fatalf("w_2 claim after %d lapses: %v", testMaxLapses, err)
	}
	if claim.Policy != domain.DispatchPolicyRoundRobinFallback || claim.FallbackReason != domain.FallbackReasonLapses {
		t.Fatalf("claim policy=%s reason=%s; want round_robin_fallback/lapses", claim.Policy, claim.FallbackReason)
	}
}

// INV-8: a cancelled job is never claimed, and cancel clears its reservation.
func TestConformance_CancelAssigned_NeverClaimed(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	submitAssign(t, repo, "job_1", "echo")
	pollAssign(t, repo, clk, "w_1")
	runPass(t, repo)

	if _, err := repo.CancelJob(ctx, "job_1"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	job := mustJob(t, repo, "job_1")
	if job.State != domain.StateCancelled || job.AssignedWorkerID != nil {
		t.Fatalf("after cancel: state=%s assigned=%v", job.State, job.AssignedWorkerID)
	}
	if _, err := repo.ClaimForPoll(ctx, "w_1", claimOpts(true)); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("cancelled job was claimed: err=%v", err)
	}
}

// INV-10: no worker holds more than one reservation.
func TestConformance_Assign_OneReservationPerWorker(t *testing.T) {
	repo, clk := newAssignRepo(t)
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	submitAssign(t, repo, "job_1", "echo")
	submitAssign(t, repo, "job_2", "echo")
	pollAssign(t, repo, clk, "w_1")

	if res := runPass(t, repo); res.Reserved != 1 {
		t.Fatalf("first pass reserved %d, want 1", res.Reserved)
	}
	if res := runPass(t, repo); res.Reserved != 0 {
		t.Fatalf("second pass reserved %d more to a worker that holds one", res.Reserved)
	}
	if mustJob(t, repo, "job_2").AssignedWorkerID != nil {
		t.Fatalf("job_2 reserved to a worker that already holds a reservation")
	}
}

// INV-10: no reservation is given to a worker with a PENDING or RUNNING attempt, even with a
// fresh poll and free capacity (one effective slot, ADR-0017 Section 2.4).
func TestConformance_Assign_SkipsWorkerWithRunningAttempt(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_1", 2, "echo")
	submitAssign(t, repo, "job_run", "echo")
	submitAssign(t, repo, "job_next", "echo")
	pollAssign(t, repo, clk, "w_1")
	if _, _, err := repo.DispatchNextJob(ctx, "w_1"); err != nil {
		t.Fatalf("DispatchNextJob: %v", err)
	}
	pollAssign(t, repo, clk, "w_1")

	if res := runPass(t, repo); res.Reserved != 0 {
		t.Fatalf("reserved %d jobs to a worker with a RUNNING attempt", res.Reserved)
	}
	if mustJob(t, repo, "job_next").AssignedWorkerID != nil {
		t.Fatalf("job_next reserved to a busy worker")
	}
}

// INV-11: only fresh, healthy, polling workers are eligible. Each stale input is checked alone,
// with a fresh control worker that must be reserved.
func TestConformance_Assign_SkipsStaleWorkers(t *testing.T) {
	t.Run("stale poll", func(t *testing.T) {
		repo, clk := newAssignRepo(t)
		addAssignWorker(t, repo, clk, "w_stale", 1, "echo")
		submitAssign(t, repo, "job_1", "echo")
		pollAssign(t, repo, clk, "w_stale")
		clk.Advance(testAcceptWindow + time.Second)
		if res := runPass(t, repo); res.Reserved != 0 {
			t.Fatalf("reserved to a worker whose poll is older than the window")
		}
	})
	t.Run("stale heartbeat", func(t *testing.T) {
		repo, clk := newAssignRepo(t)
		addAssignWorker(t, repo, clk, "w_stale", 1, "echo")
		submitAssign(t, repo, "job_1", "echo")
		clk.Advance(testWorkerTimeout + time.Second)
		if err := repo.RecordPoll(context.Background(), "w_stale"); err != nil {
			t.Fatalf("RecordPoll: %v", err)
		}
		if res := runPass(t, repo); res.Reserved != 0 {
			t.Fatalf("reserved to a worker whose heartbeat is older than the timeout")
		}
	})
	t.Run("unhealthy", func(t *testing.T) {
		repo, clk := newAssignRepo(t)
		addAssignWorker(t, repo, clk, "w_bad", 1, "echo")
		submitAssign(t, repo, "job_1", "echo")
		pollAssign(t, repo, clk, "w_bad")
		clk.Advance(time.Second)
		if _, err := repo.ReapStaleWorkers(context.Background(), 0); err != nil {
			t.Fatalf("ReapStaleWorkers: %v", err)
		}
		if res := runPass(t, repo); res.Reserved != 0 {
			t.Fatalf("reserved to an UNHEALTHY worker")
		}
	})
	t.Run("control: fresh worker is reserved", func(t *testing.T) {
		repo, clk := newAssignRepo(t)
		addAssignWorker(t, repo, clk, "w_ok", 1, "echo")
		submitAssign(t, repo, "job_1", "echo")
		pollAssign(t, repo, clk, "w_ok")
		if res := runPass(t, repo); res.Reserved != 1 {
			t.Fatalf("fresh worker not reserved: %d", res.Reserved)
		}
	})
}

// INV-12: fifo_pull ignores assignment columns. A reservation to another worker does not stop
// DispatchNextJob, and the claim clears the reservation.
func TestConformance_FifoPull_IgnoresAssignmentColumns(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	addAssignWorker(t, repo, clk, "w_2", 1, "echo")
	submitAssign(t, repo, "job_1", "echo")
	setReservation(t, repo, "job_1", "w_1", clk.Now().Add(testAcceptWindow))

	job, _, err := repo.DispatchNextJob(ctx, "w_2")
	if err != nil {
		t.Fatalf("fifo DispatchNextJob by w_2 failed on a job reserved to w_1: %v", err)
	}
	if job.ID != "job_1" || job.State != domain.StateRunning {
		t.Fatalf("fifo claimed %s in state %s", job.ID, job.State)
	}
	if mustJob(t, repo, "job_1").AssignedWorkerID != nil {
		t.Fatalf("fifo claim left the reservation set on a RUNNING job")
	}
}

// INV-18: a JOB_ASSIGNED payload carries at most 10 candidate IDs plus the count.
func TestConformance_AssignEvent_CapsCandidates(t *testing.T) {
	repo, clk := newAssignRepo(t)
	const workers = 12
	for i := 0; i < workers; i++ {
		id := "w_" + string(rune('a'+i))
		addAssignWorker(t, repo, clk, id, 1, "echo")
		pollAssign(t, repo, clk, id)
	}
	submitAssign(t, repo, "job_1", "echo")
	runPass(t, repo)

	events, err := repo.ListEventsByJob(context.Background(), "job_1")
	if err != nil {
		t.Fatalf("ListEventsByJob: %v", err)
	}
	var assigned *domain.JobEvent
	for _, e := range events {
		if e.EventType == domain.JobEventAssigned {
			assigned = e
		}
	}
	if assigned == nil {
		t.Fatalf("no JOB_ASSIGNED event")
	}
	candidates, ok := assigned.Details["candidates"].([]string)
	if !ok || len(candidates) != 10 {
		t.Fatalf("candidates = %v, want 10 IDs", assigned.Details["candidates"])
	}
	if count, ok := assigned.Details["candidate_count"].(int); !ok || count != workers {
		t.Fatalf("candidate_count = %v, want %d", assigned.Details["candidate_count"], workers)
	}
}

// INV-1 (in-memory variant; the ADR names only the PostgreSQL variant): N concurrent polls for
// one reserved job produce exactly one winner, and exactly one attempt.
func TestConformance_ClaimAssigned_ConcurrentPollsOneWinner(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	const polls = 16
	for i := 0; i < polls; i++ {
		addAssignWorker(t, repo, clk, "w_"+string(rune('a'+i)), 1, "echo")
	}
	submitAssign(t, repo, "job_1", "echo")
	// Age the job past the fallback age, so the age arm is open for the losing polls. Without the
	// state predicate, a loser would then claim the RUNNING job a second time.
	clk.Advance(testFallbackAge + time.Second)
	setReservation(t, repo, "job_1", "w_a", clk.Now().Add(testAcceptWindow))

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, losses := 0, 0
	for i := 0; i < polls; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := repo.ClaimForPoll(ctx, id, claimOpts(true))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, domain.ErrNoQueuedJobs):
				losses++
			default:
				t.Errorf("poll by %s: unexpected error %v", id, err)
			}
		}("w_" + string(rune('a'+i)))
	}
	wg.Wait()
	if wins != 1 || losses != polls-1 {
		t.Fatalf("wins=%d losses=%d, want 1 and %d", wins, losses, polls-1)
	}
	attempts, err := repo.ListAttemptsByJob(ctx, "job_1")
	if err != nil {
		t.Fatalf("ListAttemptsByJob: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
}

// ADR-0017 Sections 2.2 and 3.2: the claim applies the worker capacity check. A one-slot worker
// with a RUNNING attempt cannot claim a second job, even by a fallback arm (F-5a-2).
func TestConformance_Claim_RespectsWorkerCapacity(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	submitAssign(t, repo, "job_a", "echo")
	submitAssign(t, repo, "job_b", "echo")
	for _, id := range []string{"job_a", "job_b"} {
		j := mustJob(t, repo, id)
		j.AssignmentLapses = testMaxLapses
		if err := repo.UpdateJob(ctx, j); err != nil {
			t.Fatalf("UpdateJob(%s): %v", id, err)
		}
	}
	pollAssign(t, repo, clk, "w_1")
	if _, err := repo.ClaimForPoll(ctx, "w_1", claimOpts(false)); err != nil {
		t.Fatalf("control: first claim: %v", err)
	}
	if c, err := repo.ClaimForPoll(ctx, "w_1", claimOpts(false)); !errors.Is(err, domain.ErrWorkerNoCapacity) {
		t.Fatalf("one-slot worker with a RUNNING attempt claimed again: claim=%v err=%v", c, err)
	}
}

// ADR-0017 implementation note 1 (D9): a job at the lapse limit is excluded from the candidate
// read even when another job of its type keeps the type in the pass (F-5a-3).
func TestConformance_AssignPass_ExcludesJobAtLapseLimit(t *testing.T) {
	repo, clk := newAssignRepo(t)
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	addAssignWorker(t, repo, clk, "w_2", 1, "echo")
	pollAssign(t, repo, clk, "w_1")
	pollAssign(t, repo, clk, "w_2")
	submitAssign(t, repo, "job_limit", "echo")
	submitAssign(t, repo, "job_fresh", "echo")
	j := mustJob(t, repo, "job_limit")
	j.AssignmentLapses = testMaxLapses
	if err := repo.UpdateJob(context.Background(), j); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}
	runPass(t, repo)
	if got := mustJob(t, repo, "job_limit"); got.AssignedWorkerID != nil {
		t.Fatalf("job at the lapse limit was reserved to %s", *got.AssignedWorkerID)
	}
	if got := mustJob(t, repo, "job_fresh"); got.AssignedWorkerID == nil {
		t.Fatalf("control: job below the limit was not reserved")
	}
}

// ADR-0017 D9 lower boundary: a job at L-1 lapses is not claimable by the lapse arm (F-5a-3).
func TestConformance_ClaimLapseArm_ClosedBelowLimit(t *testing.T) {
	repo, clk := newAssignRepo(t)
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	submitAssign(t, repo, "job_1", "echo")
	j := mustJob(t, repo, "job_1")
	j.AssignmentLapses = testMaxLapses - 1
	if err := repo.UpdateJob(context.Background(), j); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}
	pollAssign(t, repo, clk, "w_1")
	if _, err := repo.ClaimForPoll(context.Background(), "w_1", claimOpts(false)); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("job at L-1 lapses was claimed by the lapse arm: err=%v", err)
	}
}

// ADR-0017 D8 and Section 3.4: revoking the reservation of a worker that is no longer HEALTHY does
// not count a lapse (F-5a-4).
func TestConformance_AssignPass_UnhealthyRevokeCountsNoLapse(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	pollAssign(t, repo, clk, "w_1")
	submitAssign(t, repo, "job_1", "echo")
	runPass(t, repo)
	if mustJob(t, repo, "job_1").AssignedWorkerID == nil {
		t.Fatalf("control: job was not reserved")
	}
	if err := repo.UpsertWorker(ctx, &domain.Worker{ID: "w_1", Version: "v1.0.0", TotalSlots: 1, Status: domain.WorkerStatusUnhealthy, SupportedTasks: []string{"echo"}, LastHeartbeatAt: clk.Now()}); err != nil {
		t.Fatalf("UpsertWorker: %v", err)
	}
	res := runPass(t, repo)
	got := mustJob(t, repo, "job_1")
	if res.Revoked != 1 || got.AssignedWorkerID != nil {
		t.Fatalf("control: revoke did not happen: revoked=%d assigned=%v", res.Revoked, got.AssignedWorkerID)
	}
	if got.AssignmentLapses != 0 {
		t.Fatalf("revoke for an UNHEALTHY worker counted a lapse: lapses=%d", got.AssignmentLapses)
	}
}

// ADR-0017 Sections 1.3 and 3.2: the own arm needs assignment_expires_at > now, so a reservation
// is not claimable at the exact instant it expires (F-5a-5).
func TestConformance_ClaimOwnArm_ClosedAtExactExpiry(t *testing.T) {
	repo, clk := newAssignRepo(t)
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	submitAssign(t, repo, "job_1", "echo")
	setReservation(t, repo, "job_1", "w_1", clk.Now())
	if _, err := repo.ClaimForPoll(context.Background(), "w_1", claimOpts(false)); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("reservation claimed at its exact expiry: err=%v", err)
	}
}

// ADR-0017 Section 3.2: dispatchable_since is GREATEST(created_at, not_before). A RETRYING job
// whose not_before has only just passed is not aged, however old created_at is (F-5a-6).
func TestConformance_ClaimAgeArm_UsesNotBefore(t *testing.T) {
	repo, clk := newAssignRepo(t)
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	submitAssign(t, repo, "job_1", "echo")
	clk.Advance(testFallbackAge + time.Minute)
	j := mustJob(t, repo, "job_1")
	j.State = domain.StateRetrying
	nb := clk.Now()
	j.NotBefore = &nb
	if err := repo.UpdateJob(context.Background(), j); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}
	pollAssign(t, repo, clk, "w_1")
	if _, err := repo.ClaimForPoll(context.Background(), "w_1", claimOpts(true)); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("RETRYING job claimed by age although not_before just passed: err=%v", err)
	}
}

// ADR-0017 Section 2.1: the JOB_ASSIGNED payload lists the first 10 eligible worker IDs in ID
// order. Repeated over fresh repositories with workers inserted in reverse, so map iteration
// order would show (F-5a-7).
func TestConformance_AssignEvent_CandidatesInIDOrder(t *testing.T) {
	want := []string{"w_a", "w_b", "w_c", "w_d", "w_e", "w_f", "w_g", "w_h", "w_i", "w_j"}
	for round := 0; round < 20; round++ {
		repo, clk := newAssignRepo(t)
		for i := 11; i >= 0; i-- {
			id := "w_" + string(rune('a'+i))
			addAssignWorker(t, repo, clk, id, 1, "echo")
			pollAssign(t, repo, clk, id)
		}
		submitAssign(t, repo, "job_1", "echo")
		runPass(t, repo)
		events, err := repo.ListEventsByJob(context.Background(), "job_1")
		if err != nil {
			t.Fatalf("ListEventsByJob: %v", err)
		}
		seen := 0
		for _, e := range events {
			if e.EventType != domain.JobEventAssigned {
				continue
			}
			seen++
			got, _ := e.Details["candidates"].([]string)
			if len(got) != len(want) {
				t.Fatalf("round %d: candidates %v", round, got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("round %d: candidates %v, want %v", round, got, want)
				}
			}
		}
		if seen != 1 {
			t.Fatalf("round %d: %d JOB_ASSIGNED events, want 1", round, seen)
		}
	}
}

// ADR-0017 INV-22 (memory twin): a pass that cannot take the pass lock skips. It returns Skipped,
// reserves nothing and leaves the pass heartbeat unchanged. The pass runs in a goroutine with a
// deadline, so a pass that blocks on the lock fails the test instead of hanging it (F-5a-9).
func TestConformance_AssignPass_SkipsWhenPassLockHeld(t *testing.T) {
	repo, clk := newAssignRepo(t)
	ctx := context.Background()
	addAssignWorker(t, repo, clk, "w_1", 1, "echo")
	pollAssign(t, repo, clk, "w_1")
	submitAssign(t, repo, "job_1", "echo")
	before, err := repo.AssignmentPassState(ctx)
	if err != nil {
		t.Fatalf("AssignmentPassState: %v", err)
	}

	release := repo.HoldPassLockForTest()
	var once sync.Once
	unlock := func() { once.Do(release) }
	t.Cleanup(unlock)

	type out struct {
		res storage.AssignResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := repo.AssignQueuedJobs(ctx, assignParams())
		done <- out{res, err}
	}()
	var got out
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("pass blocked on a held pass lock instead of skipping")
	}
	if got.err != nil || !got.res.Skipped {
		t.Fatalf("pass with the lock held: res=%+v err=%v, want Skipped", got.res, got.err)
	}
	if mustJob(t, repo, "job_1").AssignedWorkerID != nil {
		t.Fatalf("skipped pass reserved a job")
	}
	after, err := repo.AssignmentPassState(ctx)
	if err != nil {
		t.Fatalf("AssignmentPassState: %v", err)
	}
	if !after.Equal(before) {
		t.Fatalf("skipped pass moved the heartbeat from %v to %v", before, after)
	}

	unlock()
	if res := runPass(t, repo); res.Skipped || mustJob(t, repo, "job_1").AssignedWorkerID == nil {
		t.Fatalf("control: pass after release: res=%+v", res)
	}
}
