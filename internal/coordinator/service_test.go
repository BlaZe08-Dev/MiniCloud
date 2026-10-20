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

	// Poll from UNHEALTHY worker: must return ErrNoWorkersAvailable and no assignment
	assignment, err := svc.PollWork(ctx, "worker-1")
	if !errors.Is(err, domain.ErrNoWorkersAvailable) {
		t.Fatalf("expected ErrNoWorkersAvailable when polling from unhealthy worker, got %v (assignment=%v)", err, assignment)
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
