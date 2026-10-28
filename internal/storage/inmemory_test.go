package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/storagetest"
)

func TestInMemoryRepository_JobLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	job := &domain.Job{
		ID:             "job_100",
		TaskType:       "echo",
		Payload:        []byte(`{"message":"hello"}`),
		State:          domain.StateQueued,
		Priority:       0,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	fetched, err := repo.GetJob(ctx, "job_100")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if fetched.ID != "job_100" || fetched.State != domain.StateQueued {
		t.Fatalf("unexpected fetched job: %+v", fetched)
	}

	queued, err := repo.ListQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("ListQueuedJobs failed: %v", err)
	}
	if len(queued) != 1 || queued[0].ID != "job_100" {
		t.Fatalf("expected 1 queued job, got %d", len(queued))
	}

	// Update job state
	fetched.State = domain.StateRunning
	fetched.CurrentAttemptID = "att_1"
	if err := repo.UpdateJob(ctx, fetched); err != nil {
		t.Fatalf("UpdateJob failed: %v", err)
	}

	queuedAfterRunning, err := repo.ListQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("ListQueuedJobs failed: %v", err)
	}
	if len(queuedAfterRunning) != 0 {
		t.Fatalf("expected 0 queued jobs after transition to RUNNING, got %d", len(queuedAfterRunning))
	}
}

func TestInMemoryRepository_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	var wg sync.WaitGroup
	workersCount := 10
	jobsPerWorker := 20

	for w := 0; w < workersCount; w++ {
		workerID := fmt.Sprintf("worker_%d", w)
		wg.Add(1)
		go func(wID string) {
			defer wg.Done()
			_ = repo.UpsertWorker(ctx, &domain.Worker{
				ID:             wID,
				Status:         domain.WorkerStatusHealthy,
				TotalSlots:     4,
				AvailableSlots: 4,
				SupportedTasks: []string{"echo"},
			})
		}(workerID)
	}

	for w := 0; w < workersCount; w++ {
		wg.Add(1)
		go func(workerIndex int) {
			defer wg.Done()
			for j := 0; j < jobsPerWorker; j++ {
				jobID := fmt.Sprintf("job_%d_%d", workerIndex, j)
				_ = repo.CreateJob(ctx, &domain.Job{
					ID:        jobID,
					TaskType:  "echo",
					State:     domain.StateQueued,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
				_, _ = repo.GetJob(ctx, jobID)
			}
		}(w)
	}

	wg.Wait()

	queued, err := repo.ListQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("ListQueuedJobs failed: %v", err)
	}
	if len(queued) != workersCount*jobsPerWorker {
		t.Fatalf("expected %d queued jobs, got %d", workersCount*jobsPerWorker, len(queued))
	}
}

func TestInMemoryRepository_AtomicDispatchJob_Preconditions(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		setup       func(r *storage.InMemoryRepository) (jobID, workerID string)
		expectedErr error
	}{
		{
			name: "job not found",
			setup: func(r *storage.InMemoryRepository) (string, string) {
				_ = r.UpsertWorker(ctx, &domain.Worker{
					ID:             "w1",
					Status:         domain.WorkerStatusHealthy,
					TotalSlots:     2,
					SupportedTasks: []string{"echo"},
				})
				return "nonexistent_job", "w1"
			},
			expectedErr: domain.ErrJobNotFound,
		},
		{
			name: "job not in QUEUED state",
			setup: func(r *storage.InMemoryRepository) (string, string) {
				_ = r.UpsertWorker(ctx, &domain.Worker{
					ID:             "w1",
					Status:         domain.WorkerStatusHealthy,
					TotalSlots:     2,
					SupportedTasks: []string{"echo"},
				})
				_ = r.CreateJob(ctx, &domain.Job{
					ID:        "job_sub",
					TaskType:  "echo",
					State:     domain.StateSubmitted,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
				return "job_sub", "w1"
			},
			expectedErr: domain.ErrJobNotQueued,
		},
		{
			name: "worker not found",
			setup: func(r *storage.InMemoryRepository) (string, string) {
				_ = r.CreateJob(ctx, &domain.Job{
					ID:        "job_q",
					TaskType:  "echo",
					State:     domain.StateQueued,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
				return "job_q", "nonexistent_worker"
			},
			expectedErr: domain.ErrWorkerNotFound,
		},
		{
			name: "worker unhealthy",
			setup: func(r *storage.InMemoryRepository) (string, string) {
				_ = r.UpsertWorker(ctx, &domain.Worker{
					ID:             "w_unhealthy",
					Status:         domain.WorkerStatusUnhealthy,
					TotalSlots:     2,
					SupportedTasks: []string{"echo"},
				})
				_ = r.CreateJob(ctx, &domain.Job{
					ID:        "job_q",
					TaskType:  "echo",
					State:     domain.StateQueued,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
				return "job_q", "w_unhealthy"
			},
			expectedErr: domain.ErrWorkerUnhealthy,
		},
		{
			name: "worker unsupported task",
			setup: func(r *storage.InMemoryRepository) (string, string) {
				_ = r.UpsertWorker(ctx, &domain.Worker{
					ID:             "w_sha",
					Status:         domain.WorkerStatusHealthy,
					TotalSlots:     2,
					SupportedTasks: []string{"sha256_loop"},
				})
				_ = r.CreateJob(ctx, &domain.Job{
					ID:        "job_echo",
					TaskType:  "echo",
					State:     domain.StateQueued,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
				return "job_echo", "w_sha"
			},
			expectedErr: domain.ErrWorkerTaskUnsupported,
		},
		{
			name: "worker no capacity",
			setup: func(r *storage.InMemoryRepository) (string, string) {
				_ = r.UpsertWorker(ctx, &domain.Worker{
					ID:             "w_full",
					Status:         domain.WorkerStatusHealthy,
					TotalSlots:     1,
					SupportedTasks: []string{"echo"},
				})
				_ = r.CreateJob(ctx, &domain.Job{
					ID:        "job_1",
					TaskType:  "echo",
					State:     domain.StateQueued,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
				// Dispatch 1st job to exhaust capacity
				_, _ = r.DispatchJob(ctx, "job_1", "w_full")

				_ = r.CreateJob(ctx, &domain.Job{
					ID:        "job_2",
					TaskType:  "echo",
					State:     domain.StateQueued,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
				return "job_2", "w_full"
			},
			expectedErr: domain.ErrWorkerNoCapacity,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := storage.NewInMemoryRepository()
			jobID, workerID := tc.setup(repo)
			att, err := repo.DispatchJob(ctx, jobID, workerID)
			if err == nil {
				t.Fatalf("expected error %v, got nil (attempt=%+v)", tc.expectedErr, att)
			}
			if err != tc.expectedErr {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
		})
	}
}

func TestInMemoryRepository_ConcurrentDispatch_ExactlyOneAttempt(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	_ = repo.UpsertWorker(ctx, &domain.Worker{
		ID:             "worker_race",
		Status:         domain.WorkerStatusHealthy,
		TotalSlots:     100,
		SupportedTasks: []string{"echo"},
	})

	_ = repo.CreateJob(ctx, &domain.Job{
		ID:        "job_target",
		TaskType:  "echo",
		State:     domain.StateQueued,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})

	goroutines := 50
	var wg sync.WaitGroup
	var successCount int64
	var conflictCount int64
	var mu sync.Mutex
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.DispatchJob(ctx, "job_target", "worker_race")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
			} else if err == domain.ErrJobNotQueued {
				conflictCount++
			}
		}()
	}

	close(start)
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful dispatch, got %d", successCount)
	}
	if conflictCount != int64(goroutines-1) {
		t.Fatalf("expected %d conflicts, got %d", goroutines-1, conflictCount)
	}

	attempts, err := repo.ListAttemptsByJob(ctx, "job_target")
	if err != nil {
		t.Fatalf("ListAttemptsByJob failed: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected exactly 1 attempt record, got %d", len(attempts))
	}

	job, err := repo.GetJob(ctx, "job_target")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if job.State != domain.StateRunning {
		t.Fatalf("expected job state RUNNING, got %s", job.State)
	}
}

func TestInMemoryRepository_ConcurrentCommit_ExactlyOneAccepted(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	_ = repo.UpsertWorker(ctx, &domain.Worker{
		ID:             "w_commit",
		Status:         domain.WorkerStatusHealthy,
		TotalSlots:     1,
		SupportedTasks: []string{"echo"},
	})

	_ = repo.CreateJob(ctx, &domain.Job{
		ID:        "job_to_commit",
		TaskType:  "echo",
		State:     domain.StateQueued,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})

	att, err := repo.DispatchJob(ctx, "job_to_commit", "w_commit")
	if err != nil {
		t.Fatalf("DispatchJob failed: %v", err)
	}

	goroutines := 50
	var wg sync.WaitGroup
	var successCount int64
	var conflictCount int64
	var mu sync.Mutex
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.CommitAttempt(ctx, "job_to_commit", att.ID, att.LeaseToken, "w_commit", domain.AttemptOutcome{
				ExitCode: 0,
				Output:   `{"msg":"success"}`,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
			} else if err == domain.ErrJobAlreadyTerminal || err == domain.ErrStaleAttempt {
				conflictCount++
			}
		}()
	}

	close(start)
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful commit, got %d", successCount)
	}
	if conflictCount != int64(goroutines-1) {
		t.Fatalf("expected %d conflicts, got %d", goroutines-1, conflictCount)
	}

	job, err := repo.GetJob(ctx, "job_to_commit")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if job.State != domain.StateSucceeded {
		t.Fatalf("expected job state SUCCEEDED, got %s", job.State)
	}
}

func TestInMemoryRepository_LeaseTokenHashVerification(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	_ = repo.UpsertWorker(ctx, &domain.Worker{
		ID:             "w_token",
		Status:         domain.WorkerStatusHealthy,
		TotalSlots:     2,
		SupportedTasks: []string{"echo"},
	})

	_ = repo.CreateJob(ctx, &domain.Job{
		ID:        "job_tok",
		TaskType:  "echo",
		State:     domain.StateQueued,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})

	att, err := repo.DispatchJob(ctx, "job_tok", "w_token")
	if err != nil {
		t.Fatalf("DispatchJob failed: %v", err)
	}

	// Verify returned attempt has raw LeaseToken
	if att.LeaseToken == "" {
		t.Fatalf("expected non-empty raw LeaseToken returned on dispatch")
	}

	// Verify stored attempt in repository has raw LeaseToken stripped and LeaseTokenHash populated
	storedAtt, err := repo.GetAttempt(ctx, att.ID)
	if err != nil {
		t.Fatalf("GetAttempt failed: %v", err)
	}
	if storedAtt.LeaseToken != "" {
		t.Fatalf("expected stored LeaseToken to be empty, got %q", storedAtt.LeaseToken)
	}
	if storedAtt.LeaseTokenHash == "" {
		t.Fatalf("expected stored LeaseTokenHash to be non-empty")
	}

	// Attempt commit with invalid lease token -> rejected with ErrStaleAttempt
	_, err = repo.CommitAttempt(ctx, "job_tok", att.ID, "wrong_token", "w_token", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   "ok",
	})
	if err != domain.ErrStaleAttempt {
		t.Fatalf("expected ErrStaleAttempt for wrong token, got %v", err)
	}

	// Commit with correct raw lease token -> succeeds
	committedJob, err := repo.CommitAttempt(ctx, "job_tok", att.ID, att.LeaseToken, "w_token", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   "ok",
	})
	if err != nil {
		t.Fatalf("expected commit with valid token to succeed, got %v", err)
	}
	if committedJob.State != domain.StateSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", committedJob.State)
	}
}

func TestInMemoryRepository_DynamicAvailableSlots(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	worker := &domain.Worker{
		ID:             "w_dynamic",
		Status:         domain.WorkerStatusHealthy,
		TotalSlots:     2,
		AvailableSlots: 999, // Should be ignored; capacity is computed dynamically
		SupportedTasks: []string{"echo"},
	}
	if err := repo.UpsertWorker(ctx, worker); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}

	// Check 1: Initial capacity computed as TotalSlots - 0 = 2
	w, err := repo.GetWorker(ctx, "w_dynamic")
	if err != nil {
		t.Fatalf("GetWorker failed: %v", err)
	}
	if w.AvailableSlots != 2 {
		t.Fatalf("expected AvailableSlots=2, got %d", w.AvailableSlots)
	}

	// Check 2: RecordHeartbeat with dummy slots does not override computed capacity
	if err := repo.RecordHeartbeat(ctx, "w_dynamic", 50); err != nil {
		t.Fatalf("RecordHeartbeat failed: %v", err)
	}
	w, _ = repo.GetWorker(ctx, "w_dynamic")
	if w.AvailableSlots != 2 {
		t.Fatalf("expected AvailableSlots=2 after heartbeat, got %d", w.AvailableSlots)
	}

	// Submit 3 jobs
	for i := 1; i <= 3; i++ {
		_ = repo.CreateJob(ctx, &domain.Job{
			ID:        fmt.Sprintf("job_slot_%d", i),
			TaskType:  "echo",
			State:     domain.StateQueued,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		})
	}

	// Dispatch Job 1 -> AvailableSlots becomes 1
	att1, err := repo.DispatchJob(ctx, "job_slot_1", "w_dynamic")
	if err != nil {
		t.Fatalf("DispatchJob 1 failed: %v", err)
	}
	w, _ = repo.GetWorker(ctx, "w_dynamic")
	if w.AvailableSlots != 1 {
		t.Fatalf("expected AvailableSlots=1 after dispatching 1 job, got %d", w.AvailableSlots)
	}

	// Dispatch Job 2 -> AvailableSlots becomes 0
	att2, err := repo.DispatchJob(ctx, "job_slot_2", "w_dynamic")
	if err != nil {
		t.Fatalf("DispatchJob 2 failed: %v", err)
	}
	w, _ = repo.GetWorker(ctx, "w_dynamic")
	if w.AvailableSlots != 0 {
		t.Fatalf("expected AvailableSlots=0 after dispatching 2 jobs, got %d", w.AvailableSlots)
	}

	// Attempt Dispatch Job 3 -> must fail with ErrWorkerNoCapacity
	_, err = repo.DispatchJob(ctx, "job_slot_3", "w_dynamic")
	if err != domain.ErrWorkerNoCapacity {
		t.Fatalf("expected ErrWorkerNoCapacity, got %v", err)
	}

	// Commit Job 1 -> AvailableSlots recovers to 1
	_, err = repo.CommitAttempt(ctx, "job_slot_1", att1.ID, att1.LeaseToken, "w_dynamic", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   "done",
	})
	if err != nil {
		t.Fatalf("CommitAttempt 1 failed: %v", err)
	}
	w, _ = repo.GetWorker(ctx, "w_dynamic")
	if w.AvailableSlots != 1 {
		t.Fatalf("expected AvailableSlots=1 after committing 1 job, got %d", w.AvailableSlots)
	}

	// Commit Job 2 -> AvailableSlots recovers to 2
	_, err = repo.CommitAttempt(ctx, "job_slot_2", att2.ID, att2.LeaseToken, "w_dynamic", domain.AttemptOutcome{
		ExitCode: 0,
		Output:   "done",
	})
	if err != nil {
		t.Fatalf("CommitAttempt 2 failed: %v", err)
	}
	w, _ = repo.GetWorker(ctx, "w_dynamic")
	if w.AvailableSlots != 2 {
		t.Fatalf("expected AvailableSlots=2 after committing both jobs, got %d", w.AvailableSlots)
	}
}

func TestInMemoryRepository_SubmitJob_And_CancelJob_Events(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	now := time.Now().UTC()
	job := &domain.Job{
		ID:        "job_event_test",
		TaskType:  "echo",
		Payload:   []byte(`{"msg":"test"}`),
		State:     domain.StateQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := repo.SubmitJob(ctx, job); err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	events, err := repo.ListEventsByJob(ctx, "job_event_test")
	if err != nil {
		t.Fatalf("ListEventsByJob failed: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (SUBMITTED, QUEUED), got %d", len(events))
	}
	if events[0].EventType != domain.JobEventSubmitted || events[1].EventType != domain.JobEventQueued {
		t.Fatalf("unexpected events: %+v", events)
	}

	// Cancel the queued job
	cancelledJob, err := repo.CancelJob(ctx, "job_event_test")
	if err != nil {
		t.Fatalf("CancelJob failed: %v", err)
	}
	if cancelledJob.State != domain.StateCancelled {
		t.Fatalf("expected CANCELLED, got %s", cancelledJob.State)
	}

	eventsAfterCancel, err := repo.ListEventsByJob(ctx, "job_event_test")
	if err != nil {
		t.Fatalf("ListEventsByJob failed: %v", err)
	}
	lastEvent := eventsAfterCancel[len(eventsAfterCancel)-1]
	if lastEvent.EventType != domain.JobEventCancelled {
		t.Fatalf("expected last event CANCELLED, got %s", lastEvent.EventType)
	}

	// Cancelling already cancelled job returns ErrJobAlreadyTerminal
	_, err = repo.CancelJob(ctx, "job_event_test")
	if err != domain.ErrJobAlreadyTerminal {
		t.Fatalf("expected ErrJobAlreadyTerminal, got %v", err)
	}
}

func TestInMemoryRepository_Conformance(t *testing.T) {
	storagetest.RunRepositoryConformanceTests(t, func(t *testing.T) storage.Repository {
		return storage.NewInMemoryRepository()
	})
}

func newInMemoryHarness(t *testing.T) storagetest.Harness {
	repo := storage.NewInMemoryRepository()
	return storagetest.Harness{
		Repo: repo,
		ForceJobState: func(t *testing.T, jobID string, state domain.JobState) {
			t.Helper()
			job, err := repo.GetJob(context.Background(), jobID)
			if err != nil {
				t.Fatalf("GetJob(%s) failed: %v", jobID, err)
			}
			job.State = state
			if err := repo.UpdateJob(context.Background(), job); err != nil {
				t.Fatalf("UpdateJob(%s) failed: %v", jobID, err)
			}
		},
		ForceAttemptState: func(t *testing.T, attemptID string, state domain.AttemptState) {
			t.Helper()
			att, err := repo.GetAttempt(context.Background(), attemptID)
			if err != nil {
				t.Fatalf("GetAttempt(%s) failed: %v", attemptID, err)
			}
			att.State = state
			if err := repo.UpdateAttempt(context.Background(), att); err != nil {
				t.Fatalf("UpdateAttempt(%s) failed: %v", attemptID, err)
			}
		},
		LeaseDuration: domain.DefaultLeaseDuration,
		Now:           func(t *testing.T) time.Time { return time.Now().UTC() },
		SetLeaseExpiry: func(t *testing.T, attemptID string, offset time.Duration) {
			t.Helper()
			att, err := repo.GetAttempt(context.Background(), attemptID)
			if err != nil {
				t.Fatalf("GetAttempt(%s) failed: %v", attemptID, err)
			}
			expiry := time.Now().UTC().Add(offset)
			att.LeaseExpiresAt = &expiry
			if err := repo.UpdateAttempt(context.Background(), att); err != nil {
				t.Fatalf("UpdateAttempt(%s) failed: %v", attemptID, err)
			}
		},
	}
}

func TestConformance_Dispatch_StampsLease(t *testing.T) {
	storagetest.RunDispatchLeaseConformance(t, newInMemoryHarness)
}

func TestConformance_RenewLease_AcceptUntilSwept(t *testing.T) {
	storagetest.RunRenewLeaseAcceptUntilSweptConformance(t, newInMemoryHarness)
}

func TestConformance_RenewLease_RejectedAfterLost(t *testing.T) {
	storagetest.RunRenewLeaseRejectedAfterLostConformance(t, newInMemoryHarness)
}

func TestConformance_SweepExpiredLeases_RetryOrFail(t *testing.T) {
	storagetest.RunSweepRetryOrFailConformance(t, newInMemoryHarness)
}

func TestConformance_SweepExpiredLeases_GraceInputSuppressesReaping(t *testing.T) {
	storagetest.RunSweepGraceInputConformance(t, newInMemoryHarness)
}

func TestConformance_CommitAttempt_ConflictEvaluationOrder(t *testing.T) {
	storagetest.RunCommitAttemptConflictOrderConformance(t, newInMemoryHarness)
}

func TestConformance_CancelJob_IdempotentCancelRequested(t *testing.T) {
	storagetest.RunCancelIdempotentCancelRequestedConformance(t, newInMemoryHarness)
}

func TestConformance_CancelJob_RetryingImmediatelyCancelled(t *testing.T) {
	storagetest.RunCancelRetryingImmediatelyCancelledConformance(t, newInMemoryHarness)
}

func TestConformance_CommitAttempt_TerminalJob(t *testing.T) {
	storagetest.RunCommitAttemptTerminalJobConformance(t, newInMemoryHarness)
}

type retryTestClock struct{ now time.Time }

func (c *retryTestClock) Now() time.Time { return c.now }

// TestInMemoryRepository_RetryBackoff verifies that a RETRYING job is dispatched only once its
// not_before time has been reached on the repository clock, goes straight RETRYING -> RUNNING
// with a new attempt number, and is cancellable while it waits.
func TestInMemoryRepository_RetryBackoff(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &retryTestClock{now: start}
	repo := storage.NewInMemoryRepository(storage.WithClock(clk))

	if err := repo.UpsertWorker(ctx, &domain.Worker{ID: "w_retry", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}
	if err := repo.SubmitJob(ctx, &domain.Job{ID: "j_retry", TaskType: "echo", Payload: []byte(`{"message":"x"}`), State: domain.StateQueued, MaxAttempts: 3, TimeoutSeconds: 30}); err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}
	first, err := repo.DispatchJob(ctx, "j_retry", "w_retry")
	if err != nil {
		t.Fatalf("DispatchJob failed: %v", err)
	}

	// Attempt 1 is lost and the job waits for its backoff (stands in for the sweeper of slice 4b).
	att, _ := repo.GetAttempt(ctx, first.ID)
	att.State = domain.AttemptLost
	if err := repo.UpdateAttempt(ctx, att); err != nil {
		t.Fatalf("UpdateAttempt failed: %v", err)
	}
	notBefore := start.Add(5 * time.Second)
	job, _ := repo.GetJob(ctx, "j_retry")
	job.State = domain.StateRetrying
	job.NotBefore = &notBefore
	if err := repo.UpdateJob(ctx, job); err != nil {
		t.Fatalf("UpdateJob failed: %v", err)
	}

	assertNotDispatchable := func(step string) {
		t.Helper()
		queued, _ := repo.ListQueuedJobs(ctx)
		if len(queued) != 0 {
			t.Fatalf("%s: ListQueuedJobs must not list a RETRYING job before not_before, got %d", step, len(queued))
		}
		if _, _, err := repo.DispatchNextJob(ctx, "w_retry"); err != domain.ErrNoQueuedJobs {
			t.Fatalf("%s: DispatchNextJob must find no work before not_before, got %v", step, err)
		}
		if _, err := repo.DispatchJob(ctx, "j_retry", "w_retry"); err != domain.ErrJobNotQueued {
			t.Fatalf("%s: DispatchJob must reject a RETRYING job before not_before, got %v", step, err)
		}
	}
	assertNotDispatchable("t+0s")
	clk.now = start.Add(4*time.Second + 999*time.Millisecond)
	assertNotDispatchable("t+4.999s")

	clk.now = notBefore // boundary: not_before <= now is eligible
	queued, _ := repo.ListQueuedJobs(ctx)
	if len(queued) != 1 || queued[0].ID != "j_retry" {
		t.Fatalf("ListQueuedJobs must list the due RETRYING job, got %d jobs", len(queued))
	}
	dispatched, second, err := repo.DispatchNextJob(ctx, "w_retry")
	if err != nil {
		t.Fatalf("DispatchNextJob on a due RETRYING job failed: %v", err)
	}
	if dispatched.State != domain.StateRunning || dispatched.NotBefore != nil {
		t.Fatalf("expected RUNNING with not_before cleared, got %s / %v", dispatched.State, dispatched.NotBefore)
	}
	if second.AttemptNo != 2 || second.ID == first.ID {
		t.Fatalf("expected a new attempt number 2, got no=%d id=%s", second.AttemptNo, second.ID)
	}
	events, _ := repo.ListEventsByJob(ctx, "j_retry")
	last := events[len(events)-1]
	if last.EventType != domain.JobEventDispatched || last.FromState == nil || *last.FromState != domain.StateRetrying {
		t.Fatalf("expected JOB_DISPATCHED from RETRYING, got %+v", last)
	}
}

// TestInMemoryRepository_CancelRequestedAtSetOnce verifies the cancel timestamp is recorded on
// the first cancel only; a duplicate cancel on CANCEL_REQUESTED leaves it untouched.
func TestInMemoryRepository_CancelRequestedAtSetOnce(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &retryTestClock{now: start}
	repo := storage.NewInMemoryRepository(storage.WithClock(clk))
	if err := repo.UpsertWorker(ctx, &domain.Worker{ID: "w_c", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}
	if err := repo.SubmitJob(ctx, &domain.Job{ID: "j_c", TaskType: "echo", Payload: []byte(`{"message":"x"}`), State: domain.StateQueued, MaxAttempts: 3, TimeoutSeconds: 30}); err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}
	if _, err := repo.DispatchJob(ctx, "j_c", "w_c"); err != nil {
		t.Fatalf("DispatchJob failed: %v", err)
	}

	clk.now = start.Add(time.Second)
	first, err := repo.CancelJob(ctx, "j_c")
	if err != nil || first.CancelRequestedAt == nil || !first.CancelRequestedAt.Equal(clk.now) {
		t.Fatalf("first cancel must record cancel_requested_at=%v, got %v (err=%v)", clk.now, first.CancelRequestedAt, err)
	}
	clk.now = start.Add(9 * time.Second)
	dup, err := repo.CancelJob(ctx, "j_c")
	if err != nil || dup.CancelRequestedAt == nil || !dup.CancelRequestedAt.Equal(*first.CancelRequestedAt) {
		t.Fatalf("duplicate cancel must keep cancel_requested_at=%v, got %v (err=%v)", first.CancelRequestedAt, dup.CancelRequestedAt, err)
	}
}

func TestProperty_StateMachine_ModelConformance(t *testing.T) {
	storagetest.RunStateMachineModelConformance(t, newInMemoryHarness, []int64{1, 2, 3, 4, 5, 6, 7, 8}, 400)
}

func TestConformance_CommitAttempt_NonRunningAttempt_ReturnsStaleAttempt(t *testing.T) {
	storagetest.RunCommitAttemptNonRunningAttemptConformance(t, newInMemoryHarness)
}

// TestInMemoryRepository_LeaseRenewalAndExpiry drives leases, renewal, expiry and the sweep with
// an injected clock: every instant is repository time, never the wall clock.
func TestInMemoryRepository_LeaseRenewalAndExpiry(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

	type fixture struct {
		clock *retryTestClock
		repo  *storage.InMemoryRepository
		att   *domain.Attempt
	}
	newFixture := func(t *testing.T, opts ...storage.InMemoryOption) fixture {
		t.Helper()
		clock := &retryTestClock{now: t0}
		repo := storage.NewInMemoryRepository(append([]storage.InMemoryOption{storage.WithClock(clock)}, opts...)...)
		if err := repo.UpsertWorker(ctx, &domain.Worker{ID: "w1", Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, LastHeartbeatAt: t0}); err != nil {
			t.Fatal(err)
		}
		if err := repo.SubmitJob(ctx, &domain.Job{ID: "j1", TaskType: "echo", Payload: []byte(`{}`), State: domain.StateQueued, MaxAttempts: 3, TimeoutSeconds: 30, CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		_, att, err := repo.DispatchNextJob(ctx, "w1")
		if err != nil {
			t.Fatal(err)
		}
		return fixture{clock, repo, att}
	}
	sweep := func(t *testing.T, f fixture, p domain.SweepParams) *domain.SweepResult {
		t.Helper()
		if p.Backoff == nil {
			p.Backoff = func(int) time.Duration { return 0 }
		}
		res, err := f.repo.SweepExpiredLeases(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	lease := func(t *testing.T, f fixture) time.Time {
		t.Helper()
		a, err := f.repo.GetAttempt(ctx, f.att.ID)
		if err != nil || a.LeaseExpiresAt == nil {
			t.Fatalf("GetAttempt: %+v %v", a, err)
		}
		return *a.LeaseExpiresAt
	}

	t.Run("dispatch_stamps_repository_now_plus_default_30s", func(t *testing.T) {
		f := newFixture(t)
		if got := lease(t, f); !got.Equal(t0.Add(30 * time.Second)) {
			t.Fatalf("lease = %v, want %v", got, t0.Add(30*time.Second))
		}
	})

	t.Run("lease_duration_option_applies_and_ignores_non_positive", func(t *testing.T) {
		if got := lease(t, newFixture(t, storage.WithLeaseDuration(5*time.Second))); !got.Equal(t0.Add(5 * time.Second)) {
			t.Fatalf("lease with 5s option = %v", got)
		}
		if got := lease(t, newFixture(t, storage.WithLeaseDuration(0))); !got.Equal(t0.Add(30 * time.Second)) {
			t.Fatalf("zero option changed the default: %v", got)
		}
		if got := lease(t, newFixture(t, storage.WithLeaseDuration(-time.Second))); !got.Equal(t0.Add(30 * time.Second)) {
			t.Fatalf("negative option changed the default: %v", got)
		}
	})

	t.Run("dispatch_job_path_stamps_lease_too", func(t *testing.T) {
		clock := &retryTestClock{now: t0}
		repo := storage.NewInMemoryRepository(storage.WithClock(clock))
		_ = repo.UpsertWorker(ctx, &domain.Worker{ID: "w1", Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, LastHeartbeatAt: t0})
		_ = repo.SubmitJob(ctx, &domain.Job{ID: "j1", TaskType: "echo", Payload: []byte(`{}`), State: domain.StateQueued, MaxAttempts: 3, TimeoutSeconds: 30})
		att, err := repo.DispatchJob(ctx, "j1", "w1")
		if err != nil {
			t.Fatal(err)
		}
		if att.LeaseExpiresAt == nil || !att.LeaseExpiresAt.Equal(t0.Add(30*time.Second)) {
			t.Fatalf("DispatchJob lease = %v", att.LeaseExpiresAt)
		}
	})

	t.Run("expiry_is_strictly_after_the_lease_instant", func(t *testing.T) {
		f := newFixture(t)
		f.clock.now = t0.Add(30 * time.Second) // exactly at lease_expires_at
		if res := sweep(t, f, domain.SweepParams{}); res.Candidates != 0 {
			t.Fatalf("an attempt whose lease ends exactly now was swept: %+v", res)
		}
		f.clock.now = t0.Add(30*time.Second + time.Nanosecond)
		if res := sweep(t, f, domain.SweepParams{}); len(res.Swept) != 1 {
			t.Fatalf("an attempt one nanosecond past its lease was not swept: %+v", res)
		}
	})

	t.Run("periodic_renewal_keeps_the_attempt_alive_until_renewals_stop", func(t *testing.T) {
		f := newFixture(t)
		for i, at := range []time.Duration{10 * time.Second, 20 * time.Second} {
			f.clock.now = t0.Add(at)
			got, err := f.repo.RenewLease(ctx, "j1", f.att.ID, f.att.LeaseToken, "w1")
			if err != nil {
				t.Fatalf("renewal %d: %v", i, err)
			}
			if want := f.clock.now.Add(30 * time.Second); !got.LeaseExpiresAt.Equal(want) || got.CancelRequested {
				t.Fatalf("renewal %d = %+v, want expiry %v", i, got, want)
			}
		}
		// Original lease would have ended at +30s; the last renewal moved it to +50s.
		f.clock.now = t0.Add(45 * time.Second)
		if res := sweep(t, f, domain.SweepParams{}); res.Candidates != 0 {
			t.Fatalf("renewed attempt swept at +45s: %+v", res)
		}
		f.clock.now = t0.Add(50*time.Second + time.Nanosecond)
		res := sweep(t, f, domain.SweepParams{})
		if len(res.Swept) != 1 || res.Swept[0].AttemptState != domain.AttemptLost {
			t.Fatalf("attempt not swept after renewals stopped: %+v", res)
		}
	})

	t.Run("unswept_expired_attempt_still_renews_and_commits", func(t *testing.T) {
		f := newFixture(t)
		f.clock.now = t0.Add(31 * time.Second)
		got, err := f.repo.RenewLease(ctx, "j1", f.att.ID, f.att.LeaseToken, "w1")
		if err != nil || !got.LeaseExpiresAt.Equal(f.clock.now.Add(30*time.Second)) {
			t.Fatalf("renewal of an expired, unswept attempt = %+v, %v", got, err)
		}
		f.clock.now = t0.Add(5 * time.Minute)
		if _, err := f.repo.CommitAttempt(ctx, "j1", f.att.ID, f.att.LeaseToken, "w1", domain.AttemptOutcome{ExitCode: 0, Output: "ok"}); err != nil {
			t.Fatalf("commit of an expired, unswept attempt rejected: %v", err)
		}
	})

	t.Run("swept_attempt_is_never_resurrected", func(t *testing.T) {
		f := newFixture(t)
		f.clock.now = t0.Add(time.Minute)
		sweep(t, f, domain.SweepParams{Backoff: func(int) time.Duration { return time.Hour }})
		if _, err := f.repo.RenewLease(ctx, "j1", f.att.ID, f.att.LeaseToken, "w1"); !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("renew after sweep = %v", err)
		}
		if _, err := f.repo.CommitAttempt(ctx, "j1", f.att.ID, f.att.LeaseToken, "w1", domain.AttemptOutcome{}); !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("commit after sweep = %v", err)
		}
		if a, _ := f.repo.GetAttempt(ctx, f.att.ID); a.State != domain.AttemptLost {
			t.Fatalf("attempt = %s", a.State)
		}
	})

	t.Run("backoff_not_before_is_repository_time_and_gates_redispatch", func(t *testing.T) {
		f := newFixture(t)
		f.clock.now = t0.Add(time.Minute)
		sweep(t, f, domain.SweepParams{Backoff: func(int) time.Duration { return 4 * time.Second }})
		job, _ := f.repo.GetJob(ctx, "j1")
		if job.NotBefore == nil || !job.NotBefore.Equal(f.clock.now.Add(4*time.Second)) {
			t.Fatalf("not_before = %v, want %v", job.NotBefore, f.clock.now.Add(4*time.Second))
		}
		f.clock.now = f.clock.now.Add(4*time.Second - time.Nanosecond)
		if _, _, err := f.repo.DispatchNextJob(ctx, "w1"); !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("dispatch 1ns before not_before = %v", err)
		}
		f.clock.now = f.clock.now.Add(time.Nanosecond)
		if _, att2, err := f.repo.DispatchNextJob(ctx, "w1"); err != nil || att2.AttemptNo != 2 {
			t.Fatalf("dispatch at not_before = %+v, %v", att2, err)
		}
	})

	t.Run("grace_is_evaluated_on_the_repository_clock", func(t *testing.T) {
		f := newFixture(t)
		f.clock.now = t0.Add(45 * time.Second)
		grace := domain.SweepGraceState{StartupGraceUntil: t0.Add(60 * time.Second)}
		if res := sweep(t, f, domain.SweepParams{Grace: grace}); res.SuppressedByGrace != 1 || len(res.Swept) != 0 {
			t.Fatalf("sweep inside grace = %+v", res)
		}
		f.clock.now = t0.Add(60 * time.Second) // grace ends exactly now
		if res := sweep(t, f, domain.SweepParams{Grace: grace}); len(res.Swept) != 1 {
			t.Fatalf("sweep when grace ends = %+v", res)
		}
	})

	t.Run("cancel_requested_renewal_does_not_move_the_lease_so_the_sweeper_cancels", func(t *testing.T) {
		f := newFixture(t)
		f.clock.now = t0.Add(10 * time.Second)
		if _, err := f.repo.CancelJob(ctx, "j1"); err != nil {
			t.Fatal(err)
		}
		got, err := f.repo.RenewLease(ctx, "j1", f.att.ID, f.att.LeaseToken, "w1")
		if err != nil || !got.CancelRequested || !got.LeaseExpiresAt.Equal(t0.Add(30*time.Second)) {
			t.Fatalf("renewal on CANCEL_REQUESTED = %+v, %v", got, err)
		}
		f.clock.now = t0.Add(30*time.Second + time.Nanosecond)
		res := sweep(t, f, domain.SweepParams{})
		if len(res.Swept) != 1 || res.Swept[0].JobState != domain.StateCancelled {
			t.Fatalf("sweep = %+v, want the CANCEL_REQUESTED job cancelled once the unextended lease lapsed", res)
		}
	})
}

func TestConformance_RetryingJob_DispatchAndCancelParity(t *testing.T) {
	storagetest.RunRetryingJobParityConformance(t, newInMemoryHarness)
}

func TestConformance_Now_IsRepositoryClock(t *testing.T) {
	storagetest.RunNowConformance(t, newInMemoryHarness)
}

func TestInMemoryRepository_Now_UsesInjectedClock(t *testing.T) {
	want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	repo := storage.NewInMemoryRepository(storage.WithClock(&retryTestClock{now: want}))
	got, err := repo.Now(context.Background())
	if err != nil || !got.Equal(want) {
		t.Fatalf("Now = %v, %v, want the injected clock %v", got, err, want)
	}
}
