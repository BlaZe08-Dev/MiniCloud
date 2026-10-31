package storagetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// Shared ADR-0017 assignment conformance cases (slice 5b moved them here from the slice 5a
// in-memory tests, implementation note 6). The in-memory and the PostgreSQL repositories run the
// same cases through an AssignmentHarness.

// Assignment parameters used by these cases: the ADR-0017 defaults for the accept window (5 s),
// the worker timeout (MINICLOUD_WORKER_TIMEOUT default, 10 s), the batch limit (50), the fallback
// age (30 s) and the lapse limit (3).
const (
	AssignTestAcceptWindow  = 5 * time.Second
	AssignTestWorkerTimeout = 10 * time.Second
	AssignTestBatchLimit    = 50
	AssignTestFallbackAge   = 30 * time.Second
	AssignTestMaxLapses     = 3
)

// AssignmentHarness pairs a repository that implements storage.AssignmentRepository with the
// backend-specific hooks the assignment cases need. Time is the repository's own clock: the
// injected clock in memory, the database clock in PostgreSQL.
type AssignmentHarness struct {
	Repo storage.Repository
	// Now reads the repository clock.
	Now func(t *testing.T) time.Time
	// Advance moves the repository clock forward by d. In PostgreSQL, where the database clock
	// cannot be moved, every stored timestamp is moved back by d instead, which is the same thing
	// for every comparison the repository makes.
	Advance func(t *testing.T, d time.Duration)
	// SetReservation writes a reservation of jobID to workerID that expires at repository-now +
	// expiresIn, with assigned_at one accept window earlier, bypassing the pass. It is the seam for
	// states the pass would not produce on its own.
	SetReservation func(t *testing.T, jobID, workerID string, expiresIn time.Duration)
	// SetLapses sets a job's assignment_lapses.
	SetLapses func(t *testing.T, jobID string, lapses int)
	// SetRetrying puts a job in RETRYING with not_before = repository-now + notBeforeIn.
	SetRetrying func(t *testing.T, jobID string, notBeforeIn time.Duration)
	// HoldPassLock takes the pass lock (the in-memory pass mutex, or the PostgreSQL advisory key on
	// another connection) and returns the function that releases it.
	HoldPassLock func(t *testing.T) (release func())
}

func (h AssignmentHarness) assignRepo(t *testing.T) storage.AssignmentRepository {
	t.Helper()
	ar, ok := h.Repo.(storage.AssignmentRepository)
	if !ok {
		t.Fatalf("repository %T does not implement storage.AssignmentRepository", h.Repo)
	}
	return ar
}

// AssignTestParams are the pass inputs of these cases.
func AssignTestParams() storage.AssignParams {
	return storage.AssignParams{AcceptWindow: AssignTestAcceptWindow, WorkerTimeout: AssignTestWorkerTimeout, BatchLimit: AssignTestBatchLimit, MaxLapses: AssignTestMaxLapses}
}

// AssignTestClaimOpts are the claim inputs of these cases.
func AssignTestClaimOpts(ageArm bool) storage.ClaimOptions {
	return storage.ClaimOptions{MaxLapses: AssignTestMaxLapses, FallbackAge: AssignTestFallbackAge, AgeArmOpen: ageArm}
}

// AssignAddWorker registers a HEALTHY worker with a fresh heartbeat.
func AssignAddWorker(t *testing.T, h AssignmentHarness, id string, slots int, tasks ...string) {
	t.Helper()
	err := h.Repo.UpsertWorker(context.Background(), &domain.Worker{
		ID:              id,
		Version:         "v1.0.0",
		TotalSlots:      slots,
		Status:          domain.WorkerStatusHealthy,
		SupportedTasks:  tasks,
		LastHeartbeatAt: h.Now(t),
	})
	if err != nil {
		t.Fatalf("UpsertWorker(%s): %v", id, err)
	}
}

// AssignPoll records a heartbeat and a poll for a worker, so it is eligible for a reservation.
func AssignPoll(t *testing.T, h AssignmentHarness, id string) {
	t.Helper()
	ctx := context.Background()
	if err := h.Repo.RecordHeartbeat(ctx, id, 0); err != nil {
		t.Fatalf("RecordHeartbeat(%s): %v", id, err)
	}
	if err := h.assignRepo(t).RecordPoll(ctx, id); err != nil {
		t.Fatalf("RecordPoll(%s): %v", id, err)
	}
}

// AssignSubmit submits a QUEUED job.
func AssignSubmit(t *testing.T, h AssignmentHarness, id, taskType string) {
	t.Helper()
	err := h.Repo.SubmitJob(context.Background(), &domain.Job{
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

// AssignRunPass runs one pass with AssignTestParams.
func AssignRunPass(t *testing.T, h AssignmentHarness) storage.AssignResult {
	t.Helper()
	res, err := h.assignRepo(t).AssignQueuedJobs(context.Background(), AssignTestParams())
	if err != nil {
		t.Fatalf("AssignQueuedJobs: %v", err)
	}
	return res
}

// AssignJob reads a job.
func AssignJob(t *testing.T, h AssignmentHarness, id string) *domain.Job {
	t.Helper()
	job, err := h.Repo.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s): %v", id, err)
	}
	return job
}

// EventStrings normalises a JSON-array detail: []string in memory, []any after a PostgreSQL round
// trip.
func EventStrings(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// EventInt normalises a numeric detail: int in memory, float64 after a PostgreSQL round trip.
func EventInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case float64:
		if x == float64(int(x)) {
			return int(x), true
		}
	}
	return 0, false
}

func assignWorkerName(i int) string { return "w_" + string(rune('a'+i)) }

// RunAssignCreatesNoAttemptConformance: INV-2, an assignment reserves only. It creates no attempt
// and changes no attempt number.
func RunAssignCreatesNoAttemptConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_a", 1, "echo")
	AssignSubmit(t, h, "job_a", "echo")
	AssignPoll(t, h, "w_a")

	before, err := h.Repo.ListAttemptsByJob(ctx, "job_a")
	if err != nil {
		t.Fatalf("ListAttemptsByJob: %v", err)
	}
	res := AssignRunPass(t, h)
	after, err := h.Repo.ListAttemptsByJob(ctx, "job_a")
	if err != nil {
		t.Fatalf("ListAttemptsByJob: %v", err)
	}
	if res.Reserved != 1 {
		t.Fatalf("expected one reservation, got %d", res.Reserved)
	}
	if len(before) != 0 || len(after) != 0 {
		t.Fatalf("assignment created attempts: before=%d after=%d", len(before), len(after))
	}
	job := AssignJob(t, h, "job_a")
	if job.AssignedWorkerID == nil || *job.AssignedWorkerID != "w_a" {
		t.Fatalf("job not reserved to w_a: %+v", job.AssignedWorkerID)
	}
	if job.State != domain.StateQueued {
		t.Fatalf("assignment changed job state to %s", job.State)
	}
}

// RunClaimAssignedOnlyAssignedWithinWindowConformance: INV-3, only the assigned worker, inside the
// window, gets its own reservation. A poll never claims a live reservation of another worker, and
// own reservation is preferred to fallback.
func RunClaimAssignedOnlyAssignedWithinWindowConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	t.Run("wrong worker gets no live reservation", func(t *testing.T) {
		h := newHarness(t)
		AssignAddWorker(t, h, "w_1", 1, "echo")
		AssignAddWorker(t, h, "w_2", 1, "echo")
		AssignSubmit(t, h, "job_1", "echo")
		h.SetReservation(t, "job_1", "w_1", AssignTestAcceptWindow)
		if _, err := h.assignRepo(t).ClaimForPoll(context.Background(), "w_2", AssignTestClaimOpts(false)); !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("w_2 claimed w_1's live reservation: err=%v", err)
		}
	})

	t.Run("expired reservation with arms unmet gives 204 to its own worker", func(t *testing.T) {
		h := newHarness(t)
		AssignAddWorker(t, h, "w_1", 1, "echo")
		AssignSubmit(t, h, "job_1", "echo")
		h.SetReservation(t, "job_1", "w_1", -time.Millisecond)
		if _, err := h.assignRepo(t).ClaimForPoll(context.Background(), "w_1", AssignTestClaimOpts(false)); !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("expired own reservation was claimed with arms unmet: err=%v", err)
		}
	})

	t.Run("own reservation beats an older job on the fallback age arm", func(t *testing.T) {
		h := newHarness(t)
		AssignAddWorker(t, h, "w_1", 1, "echo")
		AssignSubmit(t, h, "job_old", "echo")
		h.Advance(t, AssignTestFallbackAge+time.Second)
		AssignSubmit(t, h, "job_own", "echo")
		h.SetReservation(t, "job_own", "w_1", AssignTestAcceptWindow)
		claim, err := h.assignRepo(t).ClaimForPoll(context.Background(), "w_1", AssignTestClaimOpts(true))
		if err != nil {
			t.Fatalf("ClaimForPoll: %v", err)
		}
		if claim.Job.ID != "job_own" || claim.Policy != domain.DispatchPolicyRoundRobin {
			t.Fatalf("claimed %s under %s; want job_own under round_robin", claim.Job.ID, claim.Policy)
		}
	})
}

// RunAssignmentLapseConsumesNoAttemptConformance: INV-7a, a lapse consumes no attempt. attempt_no
// and the attempt list are unchanged.
func RunAssignmentLapseConsumesNoAttemptConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")
	AssignPoll(t, h, "w_1")
	AssignRunPass(t, h)

	h.Advance(t, AssignTestAcceptWindow+time.Millisecond)
	AssignPoll(t, h, "w_1")
	res := AssignRunPass(t, h)
	if res.Lapsed != 1 {
		t.Fatalf("expected one counted lapse, got %d", res.Lapsed)
	}
	job := AssignJob(t, h, "job_1")
	if job.AssignmentLapses != 1 {
		t.Fatalf("assignment_lapses = %d, want 1", job.AssignmentLapses)
	}
	attempts, err := h.Repo.ListAttemptsByJob(ctx, "job_1")
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

// RunAssignmentLapseFallbackAfterLimitConformance: INV-7b, after MINICLOUD_ASSIGNMENT_MAX_LAPSES
// counted lapses the job is claimable by any healthy polling worker, with reason lapses.
func RunAssignmentLapseFallbackAfterLimitConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignAddWorker(t, h, "w_2", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")

	AssignPoll(t, h, "w_1")
	AssignRunPass(t, h)
	for i := 1; i <= AssignTestMaxLapses; i++ {
		h.Advance(t, AssignTestAcceptWindow+time.Millisecond)
		if i < AssignTestMaxLapses {
			// Keep w_1 eligible so the lapse pass reserves it again; the last lapse leaves the
			// job unreserved because w_1 is no longer fresh.
			AssignPoll(t, h, "w_1")
		}
		AssignRunPass(t, h)
	}
	job := AssignJob(t, h, "job_1")
	if job.AssignmentLapses != AssignTestMaxLapses {
		t.Fatalf("assignment_lapses = %d, want %d", job.AssignmentLapses, AssignTestMaxLapses)
	}
	if job.AssignedWorkerID != nil {
		t.Fatalf("job still reserved after the lapse limit")
	}

	AssignPoll(t, h, "w_2")
	claim, err := h.assignRepo(t).ClaimForPoll(ctx, "w_2", AssignTestClaimOpts(false))
	if err != nil {
		t.Fatalf("w_2 claim after %d lapses: %v", AssignTestMaxLapses, err)
	}
	if claim.Policy != domain.DispatchPolicyRoundRobinFallback || claim.FallbackReason != domain.FallbackReasonLapses {
		t.Fatalf("claim policy=%s reason=%s; want round_robin_fallback/lapses", claim.Policy, claim.FallbackReason)
	}
}

// RunCancelAssignedNeverClaimedConformance: INV-8, a cancelled job is never claimed, and cancel
// clears its reservation.
func RunCancelAssignedNeverClaimedConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")
	AssignPoll(t, h, "w_1")
	AssignRunPass(t, h)

	if _, err := h.Repo.CancelJob(ctx, "job_1"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	job := AssignJob(t, h, "job_1")
	if job.State != domain.StateCancelled || job.AssignedWorkerID != nil {
		t.Fatalf("after cancel: state=%s assigned=%v", job.State, job.AssignedWorkerID)
	}
	if _, err := h.assignRepo(t).ClaimForPoll(ctx, "w_1", AssignTestClaimOpts(true)); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("cancelled job was claimed: err=%v", err)
	}
}

// RunAssignOneReservationPerWorkerConformance: INV-10 (a), no worker holds more than one
// reservation.
func RunAssignOneReservationPerWorkerConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")
	AssignSubmit(t, h, "job_2", "echo")
	AssignPoll(t, h, "w_1")

	if res := AssignRunPass(t, h); res.Reserved != 1 {
		t.Fatalf("first pass reserved %d, want 1", res.Reserved)
	}
	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("second pass reserved %d more to a worker that holds one", res.Reserved)
	}
	if AssignJob(t, h, "job_2").AssignedWorkerID != nil {
		t.Fatalf("job_2 reserved to a worker that already holds a reservation")
	}
}

// RunAssignSkipsWorkerWithRunningAttemptConformance: INV-10 (b), no reservation is given to a
// worker with a PENDING or RUNNING attempt, even with a fresh poll and free capacity (one
// effective slot, ADR-0017 Section 2.4).
func RunAssignSkipsWorkerWithRunningAttemptConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_1", 2, "echo")
	AssignSubmit(t, h, "job_run", "echo")
	AssignSubmit(t, h, "job_next", "echo")
	AssignPoll(t, h, "w_1")
	if _, _, err := h.Repo.DispatchNextJob(ctx, "w_1"); err != nil {
		t.Fatalf("DispatchNextJob: %v", err)
	}
	AssignPoll(t, h, "w_1")

	if res := AssignRunPass(t, h); res.Reserved != 0 {
		t.Fatalf("reserved %d jobs to a worker with a RUNNING attempt", res.Reserved)
	}
	if AssignJob(t, h, "job_next").AssignedWorkerID != nil {
		t.Fatalf("job_next reserved to a busy worker")
	}
}

// RunAssignSkipsStaleWorkersConformance: INV-11, only fresh, healthy, polling workers are
// eligible. Each stale input is checked alone, with a fresh control worker that must be reserved.
func RunAssignSkipsStaleWorkersConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	t.Run("stale poll", func(t *testing.T) {
		h := newHarness(t)
		AssignAddWorker(t, h, "w_stale", 1, "echo")
		AssignSubmit(t, h, "job_1", "echo")
		AssignPoll(t, h, "w_stale")
		h.Advance(t, AssignTestAcceptWindow+time.Second)
		if res := AssignRunPass(t, h); res.Reserved != 0 {
			t.Fatalf("reserved to a worker whose poll is older than the window")
		}
	})
	t.Run("stale heartbeat", func(t *testing.T) {
		h := newHarness(t)
		AssignAddWorker(t, h, "w_stale", 1, "echo")
		AssignSubmit(t, h, "job_1", "echo")
		h.Advance(t, AssignTestWorkerTimeout+time.Second)
		if err := h.assignRepo(t).RecordPoll(context.Background(), "w_stale"); err != nil {
			t.Fatalf("RecordPoll: %v", err)
		}
		if res := AssignRunPass(t, h); res.Reserved != 0 {
			t.Fatalf("reserved to a worker whose heartbeat is older than the timeout")
		}
	})
	t.Run("unhealthy", func(t *testing.T) {
		h := newHarness(t)
		AssignAddWorker(t, h, "w_bad", 1, "echo")
		AssignSubmit(t, h, "job_1", "echo")
		AssignPoll(t, h, "w_bad")
		h.Advance(t, time.Second)
		if _, err := h.Repo.ReapStaleWorkers(context.Background(), 0); err != nil {
			t.Fatalf("ReapStaleWorkers: %v", err)
		}
		if res := AssignRunPass(t, h); res.Reserved != 0 {
			t.Fatalf("reserved to an UNHEALTHY worker")
		}
	})
	t.Run("control: fresh worker is reserved", func(t *testing.T) {
		h := newHarness(t)
		AssignAddWorker(t, h, "w_ok", 1, "echo")
		AssignSubmit(t, h, "job_1", "echo")
		AssignPoll(t, h, "w_ok")
		if res := AssignRunPass(t, h); res.Reserved != 1 {
			t.Fatalf("fresh worker not reserved: %d", res.Reserved)
		}
	})
}

// RunFifoPullIgnoresAssignmentColumnsConformance: INV-12, fifo_pull ignores assignment columns. A
// reservation to another worker does not stop DispatchNextJob, and the claim clears the
// reservation.
func RunFifoPullIgnoresAssignmentColumnsConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignAddWorker(t, h, "w_2", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")
	h.SetReservation(t, "job_1", "w_1", AssignTestAcceptWindow)

	job, _, err := h.Repo.DispatchNextJob(ctx, "w_2")
	if err != nil {
		t.Fatalf("fifo DispatchNextJob by w_2 failed on a job reserved to w_1: %v", err)
	}
	if job.ID != "job_1" || job.State != domain.StateRunning {
		t.Fatalf("fifo claimed %s in state %s", job.ID, job.State)
	}
	if AssignJob(t, h, "job_1").AssignedWorkerID != nil {
		t.Fatalf("fifo claim left the reservation set on a RUNNING job")
	}
}

// assignedEvent returns the only JOB_ASSIGNED event of a job, or fails.
func assignedEvent(t *testing.T, h AssignmentHarness, jobID string) *domain.JobEvent {
	t.Helper()
	events, err := h.Repo.ListEventsByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ListEventsByJob: %v", err)
	}
	var found *domain.JobEvent
	seen := 0
	for _, e := range events {
		if e.EventType == domain.JobEventAssigned {
			found = e
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("%d JOB_ASSIGNED events for %s, want 1", seen, jobID)
	}
	return found
}

// RunAssignEventCapsCandidatesConformance: INV-18, a JOB_ASSIGNED payload carries at most 10
// candidate IDs plus the count.
func RunAssignEventCapsCandidatesConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	const workers = 12
	for i := 0; i < workers; i++ {
		id := assignWorkerName(i)
		AssignAddWorker(t, h, id, 1, "echo")
		AssignPoll(t, h, id)
	}
	AssignSubmit(t, h, "job_1", "echo")
	AssignRunPass(t, h)

	assigned := assignedEvent(t, h, "job_1")
	candidates, ok := EventStrings(assigned.Details["candidates"])
	if !ok || len(candidates) != 10 {
		t.Fatalf("candidates = %v, want 10 IDs", assigned.Details["candidates"])
	}
	if count, ok := EventInt(assigned.Details["candidate_count"]); !ok || count != workers {
		t.Fatalf("candidate_count = %v, want %d", assigned.Details["candidate_count"], workers)
	}
}

// RunClaimAssignedConcurrentPollsOneWinnerConformance: INV-1, N concurrent polls for one reserved
// job produce exactly one winner, and exactly one attempt. The job is aged past the fallback age
// and the age arm is open for every poll, so without the claim's row lock and state predicate a
// loser would claim the RUNNING job a second time.
func RunClaimAssignedConcurrentPollsOneWinnerConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	const polls = 16
	for i := 0; i < polls; i++ {
		AssignAddWorker(t, h, assignWorkerName(i), 1, "echo")
	}
	AssignSubmit(t, h, "job_1", "echo")
	h.Advance(t, AssignTestFallbackAge+time.Second)
	h.SetReservation(t, "job_1", "w_a", AssignTestAcceptWindow)
	ar := h.assignRepo(t)

	var wg sync.WaitGroup
	var mu sync.Mutex
	start := make(chan struct{})
	wins, losses := 0, 0
	for i := 0; i < polls; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			_, err := ar.ClaimForPoll(ctx, id, AssignTestClaimOpts(true))
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
		}(assignWorkerName(i))
	}
	close(start)
	wg.Wait()
	if wins != 1 || losses != polls-1 {
		t.Fatalf("wins=%d losses=%d, want 1 and %d", wins, losses, polls-1)
	}
	attempts, err := h.Repo.ListAttemptsByJob(ctx, "job_1")
	if err != nil {
		t.Fatalf("ListAttemptsByJob: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
}

// RunClaimRespectsWorkerCapacityConformance: ADR-0017 Sections 2.2 and 3.2, the claim applies the
// worker capacity check. A one-slot worker with a RUNNING attempt cannot claim a second job, even
// by a fallback arm (F-5a-2).
func RunClaimRespectsWorkerCapacityConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignSubmit(t, h, "job_a", "echo")
	AssignSubmit(t, h, "job_b", "echo")
	for _, id := range []string{"job_a", "job_b"} {
		h.SetLapses(t, id, AssignTestMaxLapses)
	}
	AssignPoll(t, h, "w_1")
	ar := h.assignRepo(t)
	if _, err := ar.ClaimForPoll(ctx, "w_1", AssignTestClaimOpts(false)); err != nil {
		t.Fatalf("control: first claim: %v", err)
	}
	if c, err := ar.ClaimForPoll(ctx, "w_1", AssignTestClaimOpts(false)); !errors.Is(err, domain.ErrWorkerNoCapacity) {
		t.Fatalf("one-slot worker with a RUNNING attempt claimed again: claim=%v err=%v", c, err)
	}
}

// RunAssignPassExcludesJobAtLapseLimitConformance: ADR-0017 implementation note 1 (D9), a job at
// the lapse limit is excluded from the candidate read even when another job of its type keeps the
// type in the pass (F-5a-3).
func RunAssignPassExcludesJobAtLapseLimitConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignAddWorker(t, h, "w_2", 1, "echo")
	AssignPoll(t, h, "w_1")
	AssignPoll(t, h, "w_2")
	AssignSubmit(t, h, "job_limit", "echo")
	AssignSubmit(t, h, "job_fresh", "echo")
	h.SetLapses(t, "job_limit", AssignTestMaxLapses)
	AssignRunPass(t, h)
	if got := AssignJob(t, h, "job_limit"); got.AssignedWorkerID != nil {
		t.Fatalf("job at the lapse limit was reserved to %s", *got.AssignedWorkerID)
	}
	if got := AssignJob(t, h, "job_fresh"); got.AssignedWorkerID == nil {
		t.Fatalf("control: job below the limit was not reserved")
	}
}

// RunClaimLapseArmClosedBelowLimitConformance: ADR-0017 D9 lower boundary, a job at L-1 lapses is
// not claimable by the lapse arm (F-5a-3).
func RunClaimLapseArmClosedBelowLimitConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")
	h.SetLapses(t, "job_1", AssignTestMaxLapses-1)
	AssignPoll(t, h, "w_1")
	if _, err := h.assignRepo(t).ClaimForPoll(context.Background(), "w_1", AssignTestClaimOpts(false)); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("job at L-1 lapses was claimed by the lapse arm: err=%v", err)
	}
}

// RunAssignPassUnhealthyRevokeCountsNoLapseConformance: ADR-0017 D8 and Section 3.4, revoking the
// reservation of a worker that is no longer HEALTHY does not count a lapse (F-5a-4).
func RunAssignPassUnhealthyRevokeCountsNoLapseConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignPoll(t, h, "w_1")
	AssignSubmit(t, h, "job_1", "echo")
	AssignRunPass(t, h)
	if AssignJob(t, h, "job_1").AssignedWorkerID == nil {
		t.Fatalf("control: job was not reserved")
	}
	if err := h.Repo.UpsertWorker(ctx, &domain.Worker{ID: "w_1", Version: "v1.0.0", TotalSlots: 1, Status: domain.WorkerStatusUnhealthy, SupportedTasks: []string{"echo"}, LastHeartbeatAt: h.Now(t)}); err != nil {
		t.Fatalf("UpsertWorker: %v", err)
	}
	res := AssignRunPass(t, h)
	got := AssignJob(t, h, "job_1")
	if res.Revoked != 1 || got.AssignedWorkerID != nil {
		t.Fatalf("control: revoke did not happen: revoked=%d assigned=%v", res.Revoked, got.AssignedWorkerID)
	}
	if got.AssignmentLapses != 0 {
		t.Fatalf("revoke for an UNHEALTHY worker counted a lapse: lapses=%d", got.AssignmentLapses)
	}
}

// RunClaimOwnArmClosedAtExactExpiryConformance: ADR-0017 Sections 1.3 and 3.2, the own arm needs
// assignment_expires_at > now, so a reservation is not claimable at the instant it expires
// (F-5a-5). In PostgreSQL the database clock moves on between the write and the claim, so there
// the case checks an expiry that is at most a few milliseconds old.
func RunClaimOwnArmClosedAtExactExpiryConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")
	h.SetReservation(t, "job_1", "w_1", 0)
	if _, err := h.assignRepo(t).ClaimForPoll(context.Background(), "w_1", AssignTestClaimOpts(false)); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("reservation claimed at its exact expiry: err=%v", err)
	}
}

// RunClaimAgeArmUsesNotBeforeConformance: ADR-0017 Section 3.2, dispatchable_since is
// GREATEST(created_at, not_before). A RETRYING job whose not_before has only just passed is not
// aged, however old created_at is (F-5a-6).
func RunClaimAgeArmUsesNotBeforeConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignSubmit(t, h, "job_1", "echo")
	h.Advance(t, AssignTestFallbackAge+time.Minute)
	h.SetRetrying(t, "job_1", 0)
	AssignPoll(t, h, "w_1")
	if _, err := h.assignRepo(t).ClaimForPoll(context.Background(), "w_1", AssignTestClaimOpts(true)); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("RETRYING job claimed by age although not_before just passed: err=%v", err)
	}
}

// RunAssignEventCandidatesInIDOrderConformance: ADR-0017 Section 2.1, the JOB_ASSIGNED payload
// lists the first 10 eligible worker IDs in ID order. Repeated over fresh repositories with
// workers inserted in reverse, so map iteration order or insertion order would show (F-5a-7).
func RunAssignEventCandidatesInIDOrderConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness, rounds int) {
	want := []string{"w_a", "w_b", "w_c", "w_d", "w_e", "w_f", "w_g", "w_h", "w_i", "w_j"}
	for round := 0; round < rounds; round++ {
		h := newHarness(t)
		for i := 11; i >= 0; i-- {
			id := assignWorkerName(i)
			AssignAddWorker(t, h, id, 1, "echo")
			AssignPoll(t, h, id)
		}
		AssignSubmit(t, h, "job_1", "echo")
		AssignRunPass(t, h)
		got, _ := EventStrings(assignedEvent(t, h, "job_1").Details["candidates"])
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("round %d: candidates %v, want %v", round, got, want)
		}
	}
}

// RunAssignPassSkipsWhenPassLockHeldConformance: ADR-0017 INV-22, a pass that cannot take the pass
// lock skips. It returns Skipped, reserves nothing and leaves the pass heartbeat unchanged. The
// pass runs in a goroutine with a deadline, so a pass that blocks on the lock fails the test
// instead of hanging it (F-5a-9).
func RunAssignPassSkipsWhenPassLockHeldConformance(t *testing.T, newHarness func(t *testing.T) AssignmentHarness) {
	h := newHarness(t)
	ctx := context.Background()
	ar := h.assignRepo(t)
	AssignAddWorker(t, h, "w_1", 1, "echo")
	AssignPoll(t, h, "w_1")
	AssignSubmit(t, h, "job_1", "echo")
	before, err := ar.AssignmentPassState(ctx)
	if err != nil {
		t.Fatalf("AssignmentPassState: %v", err)
	}

	release := h.HoldPassLock(t)
	var once sync.Once
	unlock := func() { once.Do(release) }
	t.Cleanup(unlock)

	type out struct {
		res storage.AssignResult
		err error
	}
	done := make(chan out, 1)
	passCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() {
		res, err := ar.AssignQueuedJobs(passCtx, AssignTestParams())
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
	if AssignJob(t, h, "job_1").AssignedWorkerID != nil {
		t.Fatalf("skipped pass reserved a job")
	}
	after, err := ar.AssignmentPassState(ctx)
	if err != nil {
		t.Fatalf("AssignmentPassState: %v", err)
	}
	if !after.Equal(before) {
		t.Fatalf("skipped pass moved the heartbeat from %v to %v", before, after)
	}

	unlock()
	if res := AssignRunPass(t, h); res.Skipped || AssignJob(t, h, "job_1").AssignedWorkerID == nil {
		t.Fatalf("control: pass after release: res=%+v", res)
	}
}
