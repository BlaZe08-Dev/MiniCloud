package coordinator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/queue"
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
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg, workerTimeout)
	clk := newMockClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
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

	// Worker 2 attempts to report result for Worker 1's attempt
	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-2", 0, "rogue result", "")
	if !errors.Is(err, domain.ErrWorkerMismatch) {
		t.Fatalf("expected ErrWorkerMismatch, got: %v", err)
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

type spyQueue struct {
	queue.Queue
	mu           sync.Mutex
	ackedJobs    []string
	releasedJobs []string
}

func newSpyQueue(inner queue.Queue) *spyQueue {
	return &spyQueue{
		Queue:        inner,
		ackedJobs:    make([]string, 0),
		releasedJobs: make([]string, 0),
	}
}

func (s *spyQueue) Ack(ctx context.Context, jobID string) error {
	s.mu.Lock()
	s.ackedJobs = append(s.ackedJobs, jobID)
	s.mu.Unlock()
	return s.Queue.Ack(ctx, jobID)
}

func (s *spyQueue) Release(ctx context.Context, jobID string) error {
	s.mu.Lock()
	s.releasedJobs = append(s.releasedJobs, jobID)
	s.mu.Unlock()
	return s.Queue.Release(ctx, jobID)
}

func (s *spyQueue) getAcked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make([]string, len(s.ackedJobs))
	copy(copied, s.ackedJobs)
	return copied
}

func (s *spyQueue) getReleased() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make([]string, len(s.releasedJobs))
	copy(copied, s.releasedJobs)
	return copied
}

// TestService_Claim_JobNoLongerQueued_Dropped tests Instruction 2:
// After Claim, the coordinator re-validates that the job is still QUEUED in the repository;
// if not, it drops the item, acknowledges it, and claims again.
func TestService_Claim_JobNoLongerQueued_Dropped(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	innerQ := queue.NewInMemoryQueue()
	spy := newSpyQueue(innerQ)
	svc := coordinator.NewServiceWithQueue(repo, sched, reg, spy, 10*time.Second)

	w := &domain.Worker{ID: "worker-1", Status: domain.WorkerStatusHealthy, TotalSlots: 1, AvailableSlots: 1, SupportedTasks: []string{"echo"}}
	if err := svc.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}

	// Submit job 1 and job 2
	job1, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"one"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob 1 failed: %v", err)
	}
	job2, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"two"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob 2 failed: %v", err)
	}

	// Directly modify job 1 in repository to simulate cancellation or out-of-band state change
	storedJob1, _ := repo.GetJob(ctx, job1.ID)
	_ = storedJob1.TransitionTo(domain.StateCancelled)
	_ = repo.UpdateJob(ctx, storedJob1)

	// Worker polls: coordinator claims job 1, checks repository, sees it's CANCELLED, drops it,
	// and claims job 2!
	assignment, err := svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed: %v", err)
	}
	if assignment.JobID != job2.ID {
		t.Fatalf("expected assignment for job 2 (%s), got %s", job2.ID, assignment.JobID)
	}

	// Verify job 1 was acked/dropped from queue
	acked := spy.getAcked()
	foundJob1 := false
	for _, id := range acked {
		if id == job1.ID {
			foundJob1 = true
		}
	}
	if !foundJob1 {
		t.Fatalf("expected dropped job 1 to be acked from queue, got acked: %v", acked)
	}
}

// TestService_Claim_Redelivery_NoDuplicateAttempt tests Instruction 3:
// Redelivery of an already-dispatched job must not create a second attempt.
func TestService_Claim_Redelivery_NoDuplicateAttempt(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	q := queue.NewInMemoryQueue()
	svc := coordinator.NewServiceWithQueue(repo, sched, reg, q, 10*time.Second)

	w := &domain.Worker{ID: "worker-1", Status: domain.WorkerStatusHealthy, TotalSlots: 2, AvailableSlots: 2, SupportedTasks: []string{"echo"}}
	if err := svc.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}

	job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"test"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	// Initial claim: attempt 1 created, job is now RUNNING
	assignment, err := svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed: %v", err)
	}
	if assignment.JobID != job.ID {
		t.Fatalf("expected job %s, got %s", job.ID, assignment.JobID)
	}

	// Simulate duplicate delivery signal in queue (at-least-once redelivery of already dispatched job)
	_ = q.Enqueue(ctx, &queue.Item{JobID: job.ID, TaskType: job.TaskType})

	// Worker polls again: coordinator claims duplicate item, re-validates repository,
	// sees job is already RUNNING (not QUEUED), drops item, and returns ErrNoQueuedJobs.
	secondAssignment, err := svc.PollWork(ctx, "worker-1")
	if !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("expected ErrNoQueuedJobs for duplicate delivery, got %v (assignment=%v)", err, secondAssignment)
	}

	// Assert exactly 1 attempt exists for the job
	attempts, err := svc.ListAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("ListAttempts failed: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected exactly 1 attempt despite redelivery, got %d", len(attempts))
	}
}

// TestService_CommitResult_AckOnlyOnAccepted tests Instruction 4:
// Ack only after an ACCEPTED result commit. Stale, mismatched or rejected results must not Ack.
func TestService_CommitResult_AckOnlyOnAccepted(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	innerQ := queue.NewInMemoryQueue()
	spy := newSpyQueue(innerQ)
	svc := coordinator.NewServiceWithQueue(repo, sched, reg, spy, 10*time.Second)

	w1 := &domain.Worker{ID: "worker-1", Status: domain.WorkerStatusHealthy, TotalSlots: 1, AvailableSlots: 1, SupportedTasks: []string{"echo"}}
	w2 := &domain.Worker{ID: "worker-2", Status: domain.WorkerStatusHealthy, TotalSlots: 1, AvailableSlots: 1, SupportedTasks: []string{"echo"}}
	if err := svc.RegisterWorker(ctx, w1); err != nil {
		t.Fatalf("RegisterWorker w1 failed: %v", err)
	}
	if err := svc.RegisterWorker(ctx, w2); err != nil {
		t.Fatalf("RegisterWorker w2 failed: %v", err)
	}

	job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"test"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	assignment, err := svc.PollWork(ctx, "worker-1")
	if err != nil {
		t.Fatalf("PollWork failed: %v", err)
	}

	// 1. Worker Mismatch: worker-2 attempts to report result -> rejected with ErrWorkerMismatch
	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-2", 0, "out", "")
	if !errors.Is(err, domain.ErrWorkerMismatch) {
		t.Fatalf("expected ErrWorkerMismatch, got %v", err)
	}
	if len(spy.getAcked()) != 0 {
		t.Fatalf("expected no Ack on worker mismatch, got %v", spy.getAcked())
	}

	// 2. Stale Attempt: wrong lease token -> rejected with ErrStaleAttempt
	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, "wrong-lease", "worker-1", 0, "out", "")
	if !errors.Is(err, domain.ErrStaleAttempt) {
		t.Fatalf("expected ErrStaleAttempt, got %v", err)
	}
	if len(spy.getAcked()) != 0 {
		t.Fatalf("expected no Ack on wrong lease token, got %v", spy.getAcked())
	}

	// 3. Stale Attempt: wrong attempt ID -> rejected with ErrStaleAttempt
	_, err = svc.CommitResult(ctx, job.ID, "wrong-att", assignment.LeaseToken, "worker-1", 0, "out", "")
	if !errors.Is(err, domain.ErrStaleAttempt) {
		t.Fatalf("expected ErrStaleAttempt, got %v", err)
	}
	if len(spy.getAcked()) != 0 {
		t.Fatalf("expected no Ack on wrong attempt ID, got %v", spy.getAcked())
	}

	// 4. Valid Report: ACCEPTED -> Must trigger Ack
	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-1", 0, "out", "")
	if err != nil {
		t.Fatalf("CommitResult failed on valid report: %v", err)
	}
	acked := spy.getAcked()
	if len(acked) != 1 || acked[0] != job.ID {
		t.Fatalf("expected exactly 1 Ack for job %s, got: %v", job.ID, acked)
	}

	// 5. Duplicate Report on terminal job -> rejected with ErrJobAlreadyTerminal
	_, err = svc.CommitResult(ctx, job.ID, assignment.AttemptID, assignment.LeaseToken, "worker-1", 0, "out", "")
	if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
		t.Fatalf("expected ErrJobAlreadyTerminal, got %v", err)
	}
	if len(spy.getAcked()) != 1 {
		t.Fatalf("expected no duplicate Ack on already terminal job, got: %v", spy.getAcked())
	}
}

type failingRepoStub struct {
	storage.Repository
	dispatchErr error
}

func (f *failingRepoStub) DispatchJob(ctx context.Context, jobID string, workerID string) (*domain.Attempt, error) {
	if f.dispatchErr != nil {
		return nil, f.dispatchErr
	}
	return f.Repository.DispatchJob(ctx, jobID, workerID)
}

// TestService_ClaimOutcome_TransientFailure_QueueReleased verifies Scope 6 and Adjustment 4(b):
// When attempt creation / dispatch fails with a transient error after a queue claim,
// Queue.Release MUST be called with the job ID so the item is available for redelivery.
func TestService_ClaimOutcome_TransientFailure_QueueReleased(t *testing.T) {
	ctx := context.Background()
	innerRepo := storage.NewInMemoryRepository()
	transientErr := errors.New("transient database connection error")
	stubRepo := &failingRepoStub{
		Repository:  innerRepo,
		dispatchErr: transientErr,
	}

	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	innerQ := queue.NewInMemoryQueue()
	spy := newSpyQueue(innerQ)
	svc := coordinator.NewServiceWithQueue(stubRepo, sched, reg, spy, 10*time.Second)

	w := &domain.Worker{
		ID:             "worker-fail-test",
		Status:         domain.WorkerStatusHealthy,
		TotalSlots:     1,
		SupportedTasks: []string{"echo"},
	}
	if err := svc.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}

	job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"test"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	// Worker polls: claim succeeds from queue, but DispatchJob fails with transientErr.
	// Coordinator must call Queue.Release and return error.
	assignment, err := svc.PollWork(ctx, "worker-fail-test")
	if !errors.Is(err, transientErr) {
		t.Fatalf("expected transientErr, got %v (assignment=%v)", err, assignment)
	}

	// Verify Queue.Release was called for job.ID
	released := spy.getReleased()
	if len(released) != 1 || released[0] != job.ID {
		t.Fatalf("expected job %s to be Released, got released: %v", job.ID, released)
	}
	// Verify Queue.Ack was NOT called
	acked := spy.getAcked()
	if len(acked) != 0 {
		t.Fatalf("expected no Ack on transient failure, got acked: %v", acked)
	}
}

// TestService_ClaimOutcome_JobNotFoundOrNotQueued_AckedNeverReleased verifies Adjustment 4(a):
// When job is no longer QUEUED or not found, coordinator drops and Acks the item (never Releases it).
func TestService_ClaimOutcome_JobNotFoundOrNotQueued_AckedNeverReleased(t *testing.T) {
	ctx := context.Background()
	innerRepo := storage.NewInMemoryRepository()
	stubRepo := &failingRepoStub{
		Repository:  innerRepo,
		dispatchErr: domain.ErrJobNotFound,
	}

	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	innerQ := queue.NewInMemoryQueue()
	spy := newSpyQueue(innerQ)
	svc := coordinator.NewServiceWithQueue(stubRepo, sched, reg, spy, 10*time.Second)

	w := &domain.Worker{
		ID:             "worker-ack-test",
		Status:         domain.WorkerStatusHealthy,
		TotalSlots:     1,
		SupportedTasks: []string{"echo"},
	}
	if err := svc.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}

	job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"test"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	// Worker polls: DispatchJob returns ErrJobNotFound. Coordinator drops and Acks item.
	assignment, err := svc.PollWork(ctx, "worker-ack-test")
	if !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("expected ErrNoQueuedJobs after dropping missing job, got %v (assignment=%v)", err, assignment)
	}

	// Verify Queue.Ack was called and Queue.Release was NEVER called
	acked := spy.getAcked()
	if len(acked) != 1 || acked[0] != job.ID {
		t.Fatalf("expected job %s to be Acked, got acked: %v", job.ID, acked)
	}
	released := spy.getReleased()
	if len(released) != 0 {
		t.Fatalf("expected no Release for missing job, got released: %v", released)
	}
}

// TestService_ClaimOutcome_WorkerLacksCapacityOrUnhealthy_Released verifies Adjustment 4(c):
// When worker lacks capacity or is unhealthy, coordinator Releases the item without creating an attempt.
func TestService_ClaimOutcome_WorkerLacksCapacityOrUnhealthy_Released(t *testing.T) {
	ctx := context.Background()
	innerRepo := storage.NewInMemoryRepository()
	stubRepo := &failingRepoStub{
		Repository:  innerRepo,
		dispatchErr: domain.ErrWorkerNoCapacity,
	}

	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	innerQ := queue.NewInMemoryQueue()
	spy := newSpyQueue(innerQ)
	svc := coordinator.NewServiceWithQueue(stubRepo, sched, reg, spy, 10*time.Second)

	w := &domain.Worker{
		ID:             "worker-cap-test",
		Status:         domain.WorkerStatusHealthy,
		TotalSlots:     1,
		SupportedTasks: []string{"echo"},
	}
	if err := svc.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}

	job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"test"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	assignment, err := svc.PollWork(ctx, "worker-cap-test")
	if !errors.Is(err, domain.ErrWorkerNoCapacity) {
		t.Fatalf("expected ErrWorkerNoCapacity, got %v (assignment=%v)", err, assignment)
	}

	// Verify item was Released (not Acked)
	released := spy.getReleased()
	if len(released) != 1 || released[0] != job.ID {
		t.Fatalf("expected job %s to be Released, got released: %v", job.ID, released)
	}
	acked := spy.getAcked()
	if len(acked) != 0 {
		t.Fatalf("expected no Ack on worker no capacity, got acked: %v", acked)
	}
}

