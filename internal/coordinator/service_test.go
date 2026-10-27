package coordinator_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMockClock(t time.Time) *mockClock {
	return &mockClock{now: t}
}

func (m *mockClock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *mockClock) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}

func setupTestCoordinator(workerTimeout time.Duration) (*coordinator.Service, *mockClock, storage.Repository) {
	clk := newMockClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	repo := storage.NewInMemoryRepository(storage.WithClock(clk))
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg, workerTimeout)
	svc.SetClock(clk)
	return svc, clk, repo
}

// TestService_WorkerLiveness_ReaperAndRecovery tests the full lifecycle:
// worker is healthy -> timeout elapses -> reaped to UNHEALTHY -> poll rejected -> heartbeat restores HEALTHY.
func TestService_WorkerLiveness_ReaperAndRecovery(t *testing.T) {
	ctx := context.Background()
	timeout := 10 * time.Second
	svc, clk, repo := setupTestCoordinator(timeout)

	w := &domain.Worker{
		ID:             "worker-1",
		TotalSlots:     2,
		AvailableSlots: 2,
		SupportedTasks: []string{"echo"},
	}
	if err := svc.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}

	// Verify worker is healthy initially
	stored, err := repo.GetWorker(ctx, "worker-1")
	if err != nil {
		t.Fatalf("GetWorker failed: %v", err)
	}
	if stored.Status != domain.WorkerStatusHealthy {
		t.Fatalf("expected healthy, got %v", stored.Status)
	}

	// Advance time within timeout (5 seconds)
	clk.Advance(5 * time.Second)
	reaped, err := svc.ReapInactiveWorkers(ctx)
	if err != nil {
		t.Fatalf("ReapInactiveWorkers failed: %v", err)
	}
	if len(reaped) != 0 {
		t.Fatalf("expected 0 reaped workers, got %v", reaped)
	}

	// Advance time past timeout (total 11 seconds)
	clk.Advance(6 * time.Second)
	reaped, err = svc.ReapInactiveWorkers(ctx)
	if err != nil {
		t.Fatalf("ReapInactiveWorkers failed: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != "worker-1" {
		t.Fatalf("expected ['worker-1'] reaped, got %v", reaped)
	}

	stored, err = repo.GetWorker(ctx, "worker-1")
	if err != nil {
		t.Fatalf("GetWorker failed: %v", err)
	}
	if stored.Status != domain.WorkerStatusUnhealthy {
		t.Fatalf("expected UNHEALTHY, got %v", stored.Status)
	}

	// Submit a job
	_, err = svc.SubmitJob(ctx, "echo", []byte(`{"message":"hello"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	// Poll from UNHEALTHY worker: must return ErrWorkerUnhealthy and no assignment
	assignment, err := svc.PollWork(ctx, "worker-1")
	if !errors.Is(err, domain.ErrWorkerUnhealthy) {
		t.Fatalf("expected ErrWorkerUnhealthy when polling from unhealthy worker, got %v (assignment=%v)", err, assignment)
	}

	// Send heartbeat: restores HEALTHY status
	if err := svc.Heartbeat(ctx, "worker-1", 2); err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	stored, err = repo.GetWorker(ctx, "worker-1")
	if err != nil {
		t.Fatalf("GetWorker failed: %v", err)
	}
	if stored.Status != domain.WorkerStatusHealthy {
		t.Fatalf("expected HEALTHY after heartbeat, got %v", stored.Status)
	}

	// Now poll work must succeed
	assignment, err = svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed after recovery: %v", err)
	}
	if assignment == nil || assignment.JobID == "" {
		t.Fatalf("expected valid assignment, got nil or empty")
	}
}

// TestService_UnhealthyWorker_CommitAccepted verifies that a worker marked UNHEALTHY
// during task execution still has its completed result accepted.
func TestService_UnhealthyWorker_CommitAccepted(t *testing.T) {
	ctx := context.Background()
	timeout := 10 * time.Second
	svc, clk, repo := setupTestCoordinator(timeout)

	w := &domain.Worker{
		ID:             "worker-1",
		TotalSlots:     2,
		AvailableSlots: 2,
		SupportedTasks: []string{"echo"},
	}
	if err := svc.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}

	job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"test"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	assignment, err := svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed: %v", err)
	}

	// Advance clock past timeout and reap worker so worker becomes UNHEALTHY
	clk.Advance(15 * time.Second)
	reaped, err := svc.ReapInactiveWorkers(ctx)
	if err != nil {
		t.Fatalf("ReapInactiveWorkers failed: %v", err)
	}
	if len(reaped) != 1 {
		t.Fatalf("expected worker to be reaped, got %v", reaped)
	}

	storedWorker, _ := repo.GetWorker(ctx, "worker-1")
	if storedWorker.Status != domain.WorkerStatusUnhealthy {
		t.Fatalf("expected worker to be UNHEALTHY, got %v", storedWorker.Status)
	}

	// Worker reports execution result for its still-current attempt
	updatedJob, err := svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-1", 0, "hello output", "")
	if err != nil {
		t.Fatalf("expected commit from unhealthy worker to be accepted, but got: %v", err)
	}

	if updatedJob.State != domain.StateSucceeded {
		t.Fatalf("expected job state SUCCEEDED, got %v", updatedJob.State)
	}

	attempts, err := svc.ListAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("ListAttempts failed: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(attempts))
	}
	if attempts[0].State != domain.AttemptCompleted {
		t.Fatalf("expected attempt state COMPLETED, got %v", attempts[0].State)
	}
	if attempts[0].Output != "hello output" {
		t.Fatalf("expected output 'hello output', got %q", attempts[0].Output)
	}
}

// TestService_WorkerMismatch_Rejected verifies that an attempt completed by an unauthorized
// worker (WorkerID != attempt.WorkerID) is rejected with ErrWorkerMismatch.
func TestService_WorkerMismatch_Rejected(t *testing.T) {
	ctx := context.Background()
	timeout := 10 * time.Second
	svc, _, _ := setupTestCoordinator(timeout)

	w1 := &domain.Worker{ID: "worker-1", TotalSlots: 1, AvailableSlots: 1, SupportedTasks: []string{"echo"}}
	w2 := &domain.Worker{ID: "worker-2", TotalSlots: 1, AvailableSlots: 1, SupportedTasks: []string{"echo"}}
	_ = svc.RegisterWorker(ctx, w1)
	_ = svc.RegisterWorker(ctx, w2)

	job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"test"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	assignment, err := svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed: %v", err)
	}
	if assignment.JobID != job.ID {
		t.Fatalf("expected assigned job %s, got %s", job.ID, assignment.JobID)
	}

	// Capture slog output to verify true assigned worker is logged without leaking lease token
	var logBuf bytes.Buffer
	origLogger := slog.Default()
	defer slog.SetDefault(origLogger)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, nil)))

	// Worker 2 attempts to report result for Worker 1's attempt
	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-2", 0, "rogue result", "")
	if !errors.Is(err, domain.ErrWorkerMismatch) {
		t.Fatalf("expected ErrWorkerMismatch, got: %v", err)
	}

	// Verify log contains true assigned worker (worker-1), reporting worker (worker-2), and NO lease token
	logStr := logBuf.String()
	if !strings.Contains(logStr, `"assigned_worker":"worker-1"`) {
		t.Fatalf("expected log to contain assigned_worker=worker-1, got: %s", logStr)
	}
	if !strings.Contains(logStr, `"reporting_worker":"worker-2"`) {
		t.Fatalf("expected log to contain reporting_worker=worker-2, got: %s", logStr)
	}
	if strings.Contains(logStr, assignment.LeaseToken) {
		t.Fatalf("CRITICAL SECURITY INVARIANT: lease token was leaked in logs: %s", logStr)
	}

	// Verify job remains in RUNNING
	currentJob, err := svc.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if currentJob.State != domain.StateRunning {
		t.Fatalf("expected job to remain RUNNING, got %v", currentJob.State)
	}
}

// TestService_ReaperHeartbeatRace tests that concurrent heartbeats and reaper runs
// do not overwrite fresh heartbeats or cause race conditions.
func TestService_ReaperHeartbeatRace(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg, 100*time.Millisecond)

	w := &domain.Worker{
		ID:             "race-worker",
		TotalSlots:     2,
		AvailableSlots: 2,
		SupportedTasks: []string{"echo"},
	}
	if err := svc.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Goroutine 1: Rapid heartbeats
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = svc.Heartbeat(ctx, "race-worker", 2)
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()

	// Goroutine 2: Rapid reaper runs
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = svc.ReapInactiveWorkers(ctx)
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	// Let them race for 100ms
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Send a final guaranteed heartbeat
	if err := svc.Heartbeat(ctx, "race-worker", 2); err != nil {
		t.Fatalf("final heartbeat failed: %v", err)
	}

	worker, err := repo.GetWorker(ctx, "race-worker")
	if err != nil {
		t.Fatalf("GetWorker failed: %v", err)
	}
	if worker.Status != domain.WorkerStatusHealthy {
		t.Fatalf("expected worker to be HEALTHY after final heartbeat, got %v", worker.Status)
	}
}

// Dispatch is repository-driven (repo.DispatchNextJob) in every mode, so these tests assert
// observable outcomes (job state, attempts, worker slots) rather than queue calls.

func newDispatchCoordinator(t *testing.T, repo storage.Repository, workerID string, slots int) *coordinator.Service {
	t.Helper()
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	w := &domain.Worker{ID: workerID, Status: domain.WorkerStatusHealthy, TotalSlots: slots, SupportedTasks: []string{"echo"}}
	if err := svc.RegisterWorker(context.Background(), w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}
	return svc
}

func submitEcho(t *testing.T, svc *coordinator.Service, msg string) *domain.Job {
	t.Helper()
	job, err := svc.SubmitJob(context.Background(), "echo", []byte(`{"message":"`+msg+`"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}
	return job
}

func requireJobState(t *testing.T, repo storage.Repository, jobID string, want domain.JobState) {
	t.Helper()
	job, err := repo.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GetJob(%s) failed: %v", jobID, err)
	}
	if job.State != want {
		t.Fatalf("job %s: expected state %s, got %s", jobID, want, job.State)
	}
}

func requireAttempts(t *testing.T, repo storage.Repository, jobID string, want int) []*domain.Attempt {
	t.Helper()
	attempts, err := repo.ListAttemptsByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ListAttemptsByJob(%s) failed: %v", jobID, err)
	}
	if len(attempts) != want {
		t.Fatalf("job %s: expected %d attempts, got %d", jobID, want, len(attempts))
	}
	return attempts
}

func requireSlots(t *testing.T, repo storage.Repository, workerID string, want int) {
	t.Helper()
	w, err := repo.GetWorker(context.Background(), workerID)
	if err != nil {
		t.Fatalf("GetWorker(%s) failed: %v", workerID, err)
	}
	if w.AvailableSlots != want {
		t.Fatalf("worker %s: expected %d available slots, got %d", workerID, want, w.AvailableSlots)
	}
}

// TestService_Claim_JobNoLongerQueued_Dropped: a job cancelled before the poll is never
// dispatched; the poll hands the worker the next queued job instead.
func TestService_Claim_JobNoLongerQueued_Dropped(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	svc := newDispatchCoordinator(t, repo, "worker-1", 1)

	job1 := submitEcho(t, svc, "one")
	job2 := submitEcho(t, svc, "two")
	if _, err := repo.CancelJob(ctx, job1.ID); err != nil {
		t.Fatalf("CancelJob failed: %v", err)
	}

	assignment, err := svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed: %v", err)
	}
	if assignment.JobID != job2.ID {
		t.Fatalf("expected assignment for job 2 (%s), got %s", job2.ID, assignment.JobID)
	}
	requireJobState(t, repo, job1.ID, domain.StateCancelled)
	requireAttempts(t, repo, job1.ID, 0)
	requireJobState(t, repo, job2.ID, domain.StateRunning)
	requireAttempts(t, repo, job2.ID, 1)
	requireSlots(t, repo, "worker-1", 0)
}

// TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased: with a
// cancelled job and an UNHEALTHY worker, the poll is refused with ErrWorkerUnhealthy and the
// cancelled job is neither revived nor given an attempt.
func TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	svc := newDispatchCoordinator(t, repo, "worker-race", 1)

	job := submitEcho(t, svc, "cancel-me")
	if _, err := repo.CancelJob(ctx, job.ID); err != nil {
		t.Fatalf("CancelJob failed: %v", err)
	}
	w, err := repo.GetWorker(ctx, "worker-race")
	if err != nil {
		t.Fatalf("GetWorker failed: %v", err)
	}
	w.Status = domain.WorkerStatusUnhealthy
	if err := repo.UpsertWorker(ctx, w); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}

	assignment, err := svc.PollWork(ctx, "worker-race")
	if assignment != nil || !errors.Is(err, domain.ErrWorkerUnhealthy) {
		t.Fatalf("expected ErrWorkerUnhealthy and no assignment, got %v (assignment=%+v)", err, assignment)
	}
	requireJobState(t, repo, job.ID, domain.StateCancelled)
	requireAttempts(t, repo, job.ID, 0)
	requireSlots(t, repo, "worker-race", 1)
}

// TestService_Claim_Redelivery_NoDuplicateAttempt: polling again after a job was dispatched
// finds no work and never creates a second attempt for the running job.
func TestService_Claim_Redelivery_NoDuplicateAttempt(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	svc := newDispatchCoordinator(t, repo, "worker-1", 2)

	job := submitEcho(t, svc, "test")
	assignment, err := svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed: %v", err)
	}
	if assignment.JobID != job.ID {
		t.Fatalf("expected job %s, got %s", job.ID, assignment.JobID)
	}

	second, err := svc.PollWork(ctx, "worker-1")
	if !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("expected ErrNoQueuedJobs on repeated poll, got %v (assignment=%v)", err, second)
	}
	requireAttempts(t, repo, job.ID, 1)
	requireJobState(t, repo, job.ID, domain.StateRunning)
	requireSlots(t, repo, "worker-1", 1)
}

// TestService_CommitResult_AckOnlyOnAccepted: only an accepted commit settles the job. Rejected
// commits (wrong worker, wrong lease, wrong attempt) leave the job and attempt RUNNING with the
// slot held; the valid commit completes both and frees the slot; a replay is rejected.
func TestService_CommitResult_AckOnlyOnAccepted(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	svc := newDispatchCoordinator(t, repo, "worker-1", 1)
	if err := svc.RegisterWorker(ctx, &domain.Worker{ID: "worker-2", TotalSlots: 1, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatalf("RegisterWorker w2 failed: %v", err)
	}

	job := submitEcho(t, svc, "test")
	assignment, err := svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed: %v", err)
	}

	requireUnsettled := func(step string) {
		t.Helper()
		requireJobState(t, repo, job.ID, domain.StateRunning)
		if got := requireAttempts(t, repo, job.ID, 1)[0].State; got != domain.AttemptRunning {
			t.Fatalf("%s: expected attempt RUNNING, got %s", step, got)
		}
		requireSlots(t, repo, "worker-1", 0)
	}

	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-2", 0, "out", "")
	if !errors.Is(err, domain.ErrWorkerMismatch) {
		t.Fatalf("expected ErrWorkerMismatch, got %v", err)
	}
	requireUnsettled("worker mismatch")

	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, "wrong-lease", "worker-1", 0, "out", "")
	if !errors.Is(err, domain.ErrStaleAttempt) {
		t.Fatalf("expected ErrStaleAttempt, got %v", err)
	}
	requireUnsettled("wrong lease token")

	_, err = svc.CommitResult(ctx, job.ID, "wrong-att", assignment.LeaseToken, "worker-1", 0, "out", "")
	if !errors.Is(err, domain.ErrStaleAttempt) {
		t.Fatalf("expected ErrStaleAttempt, got %v", err)
	}
	requireUnsettled("wrong attempt id")

	if _, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-1", 0, "out", ""); err != nil {
		t.Fatalf("CommitResult failed on valid report: %v", err)
	}
	requireJobState(t, repo, job.ID, domain.StateSucceeded)
	if got := requireAttempts(t, repo, job.ID, 1)[0].State; got != domain.AttemptCompleted {
		t.Fatalf("expected attempt COMPLETED, got %s", got)
	}
	requireSlots(t, repo, "worker-1", 1)

	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-1", 0, "out", "")
	if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
		t.Fatalf("expected ErrJobAlreadyTerminal on replay, got %v", err)
	}
	requireJobState(t, repo, job.ID, domain.StateSucceeded)
	if got := requireAttempts(t, repo, job.ID, 1)[0].State; got != domain.AttemptCompleted {
		t.Fatalf("replay must not change the attempt, got %s", got)
	}
}

type failingRepoStub struct {
	storage.Repository
	dispatchErr error
}

func (f *failingRepoStub) DispatchNextJob(ctx context.Context, workerID string) (*domain.Job, *domain.Attempt, error) {
	if f.dispatchErr != nil {
		return nil, nil, f.dispatchErr
	}
	return f.Repository.DispatchNextJob(ctx, workerID)
}

// TestService_ClaimOutcome_TransientFailure_QueueReleased: a transient dispatch failure is
// returned unchanged, leaves the job QUEUED with no attempt, and the job is claimable once the
// failure clears.
func TestService_ClaimOutcome_TransientFailure_QueueReleased(t *testing.T) {
	ctx := context.Background()
	transientErr := errors.New("transient database connection error")
	stub := &failingRepoStub{Repository: storage.NewInMemoryRepository(), dispatchErr: transientErr}
	svc := newDispatchCoordinator(t, stub, "worker-fail-test", 1)
	job := submitEcho(t, svc, "test")

	assignment, err := svc.PollWork(ctx, "worker-fail-test")
	if !errors.Is(err, transientErr) {
		t.Fatalf("expected transientErr, got %v (assignment=%v)", err, assignment)
	}
	requireJobState(t, stub, job.ID, domain.StateQueued)
	requireAttempts(t, stub, job.ID, 0)
	requireSlots(t, stub, "worker-fail-test", 1)

	stub.dispatchErr = nil
	assignment, err = svc.PollWork(ctx, "worker-fail-test")
	if err != nil || assignment.JobID != job.ID {
		t.Fatalf("expected job %s to be claimable after the failure cleared, got %v (assignment=%v)", job.ID, err, assignment)
	}
}

// TestService_ClaimOutcome_JobNotFoundOrNotQueued_AckedNeverReleased: with only cancelled jobs
// or nothing at all in the repository, a poll finds no work and creates no attempt; the
// cancelled job stays CANCELLED.
func TestService_ClaimOutcome_JobNotFoundOrNotQueued_AckedNeverReleased(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	svc := newDispatchCoordinator(t, repo, "worker-ack-test", 1)

	if _, err := svc.PollWork(ctx, "worker-ack-test"); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("expected ErrNoQueuedJobs on an empty repository, got %v", err)
	}

	job := submitEcho(t, svc, "test")
	if _, err := svc.CancelJob(ctx, job.ID); err != nil {
		t.Fatalf("CancelJob failed: %v", err)
	}
	if _, err := svc.PollWork(ctx, "worker-ack-test"); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("expected ErrNoQueuedJobs for a cancelled job, got %v", err)
	}
	requireJobState(t, repo, job.ID, domain.StateCancelled)
	requireAttempts(t, repo, job.ID, 0)
	requireSlots(t, repo, "worker-ack-test", 1)
}

// TestService_ClaimOutcome_WorkerLacksCapacityOrUnhealthy_Released: a worker with no free slot
// gets ErrNoWorkersAvailable and the second job stays QUEUED with no attempt; it is dispatched
// once the first attempt commits and frees the slot.
func TestService_ClaimOutcome_WorkerLacksCapacityOrUnhealthy_Released(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	svc := newDispatchCoordinator(t, repo, "worker-cap-test", 1)

	job1 := submitEcho(t, svc, "one")
	job2 := submitEcho(t, svc, "two")
	first, err := svc.PollWork(ctx, "worker-cap-test")
	if err != nil || first.JobID != job1.ID {
		t.Fatalf("expected job 1 first, got %v (assignment=%v)", err, first)
	}

	assignment, err := svc.PollWork(ctx, "worker-cap-test")
	if !errors.Is(err, domain.ErrNoWorkersAvailable) {
		t.Fatalf("expected ErrNoWorkersAvailable, got %v (assignment=%v)", err, assignment)
	}
	requireJobState(t, repo, job2.ID, domain.StateQueued)
	requireAttempts(t, repo, job2.ID, 0)

	if _, err := svc.CommitResult(ctx, job1.ID, first.AttemptID, first.LeaseToken, "worker-cap-test", 0, "out", ""); err != nil {
		t.Fatalf("CommitResult failed: %v", err)
	}
	second, err := svc.PollWork(ctx, "worker-cap-test")
	if err != nil || second.JobID != job2.ID {
		t.Fatalf("expected job 2 after the slot was freed, got %v (assignment=%v)", err, second)
	}
}
