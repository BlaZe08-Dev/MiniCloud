package storage_test

import (
	"context"
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
	}
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
