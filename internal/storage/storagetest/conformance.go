package storagetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// RunRepositoryConformanceTests runs a comprehensive conformance suite verifying that a
// storage.Repository implementation correctly satisfies all required invariants, behaviors,
// and exact error precedence.
func RunRepositoryConformanceTests(t *testing.T, repoFactory func(t *testing.T) storage.Repository) {
	t.Run("SubmitJob_SuccessAndInvariants", func(t *testing.T) {
		ctx := context.Background()
		repo := repoFactory(t)

		job := &domain.Job{
			ID:             "job_conf_submit",
			TaskType:       "echo",
			Payload:        []byte("hello world"),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, job); err != nil {
			t.Fatalf("SubmitJob failed: %v", err)
		}

		// Duplicate submit must fail
		if err := repo.SubmitJob(ctx, job); err == nil {
			t.Fatalf("expected duplicate SubmitJob to fail, got nil")
		}

		// Oversized payload (> 64 KiB) must fail
		oversized := &domain.Job{
			ID:             "job_conf_oversized",
			TaskType:       "echo",
			Payload:        make([]byte, 65537),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, oversized); err == nil {
			t.Fatalf("expected oversized SubmitJob to fail, got nil")
		}

		// Verify events
		events, err := repo.ListEventsByJob(ctx, "job_conf_submit")
		if err != nil {
			t.Fatalf("ListEventsByJob failed: %v", err)
		}
		if len(events) < 2 {
			t.Fatalf("expected at least 2 events (SUBMITTED, QUEUED), got %d", len(events))
		}
		if events[0].EventType != domain.JobEventSubmitted || events[1].EventType != domain.JobEventQueued {
			t.Fatalf("expected SUBMITTED and QUEUED events, got %s, %s", events[0].EventType, events[1].EventType)
		}
	})

	t.Run("DispatchJob_PreconditionsAndPrecedence", func(t *testing.T) {
		ctx := context.Background()
		repo := repoFactory(t)

		// Setup workers
		healthyWorker := &domain.Worker{
			ID:             "w_healthy",
			Version:        "v1.0.0",
			TotalSlots:     1,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, healthyWorker); err != nil {
			t.Fatalf("UpsertWorker healthy failed: %v", err)
		}

		unhealthyWorker := &domain.Worker{
			ID:             "w_unhealthy",
			Version:        "v1.0.0",
			TotalSlots:     1,
			Status:         domain.WorkerStatusUnhealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, unhealthyWorker); err != nil {
			t.Fatalf("UpsertWorker unhealthy failed: %v", err)
		}

		noTasksWorker := &domain.Worker{
			ID:             "w_notasks",
			Version:        "v1.0.0",
			TotalSlots:     1,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"sleep"},
		}
		if err := repo.UpsertWorker(ctx, noTasksWorker); err != nil {
			t.Fatalf("UpsertWorker noTasks failed: %v", err)
		}

		// Setup jobs
		queuedJob := &domain.Job{
			ID:             "j_queued",
			TaskType:       "echo",
			Payload:        []byte("test"),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, queuedJob); err != nil {
			t.Fatalf("SubmitJob j_queued failed: %v", err)
		}

		runningJob := &domain.Job{
			ID:             "j_running",
			TaskType:       "echo",
			Payload:        []byte("test"),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, runningJob); err != nil {
			t.Fatalf("SubmitJob j_running failed: %v", err)
		}
		// Dispatch runningJob onto healthyWorker so it is RUNNING and healthyWorker has 0 capacity
		att1, err := repo.DispatchJob(ctx, "j_running", "w_healthy")
		if err != nil {
			t.Fatalf("DispatchJob initial failed: %v", err)
		}
		if att1.LeaseToken == "" {
			t.Fatalf("expected LeaseToken to be populated for worker")
		}

		// Table-driven single precondition failures
		tests := []struct {
			name        string
			jobID       string
			workerID    string
			expectedErr error
		}{
			{
				name:        "JobNotFound",
				jobID:       "j_unknown",
				workerID:    "w_healthy",
				expectedErr: domain.ErrJobNotFound,
			},
			{
				name:        "JobNotQueued",
				jobID:       "j_running",
				workerID:    "w_notasks",
				expectedErr: domain.ErrJobNotQueued,
			},
			{
				name:        "WorkerNotFound",
				jobID:       "j_queued",
				workerID:    "w_nonexistent",
				expectedErr: domain.ErrWorkerNotFound,
			},
			{
				name:        "WorkerUnhealthy",
				jobID:       "j_queued",
				workerID:    "w_unhealthy",
				expectedErr: domain.ErrWorkerUnhealthy,
			},
			{
				name:        "TaskUnsupported",
				jobID:       "j_queued",
				workerID:    "w_notasks",
				expectedErr: domain.ErrWorkerTaskUnsupported,
			},
			{
				name:        "WorkerNoCapacity",
				jobID:       "j_queued",
				workerID:    "w_healthy", // capacity was 1, consumed by j_running
				expectedErr: domain.ErrWorkerNoCapacity,
			},
			// Plan adjustment a: Several preconditions failing at once
			{
				name:        "Precedence_JobNotQueued_Before_WorkerUnhealthyAndNoCapacity",
				jobID:       "j_running", // not queued
				workerID:    "w_unhealthy",
				expectedErr: domain.ErrJobNotQueued,
			},
			{
				name:        "Precedence_WorkerUnhealthy_Before_TaskUnsupportedAndNoCapacity",
				jobID:       "j_queued",
				workerID:    "w_unhealthy", // unhealthy + does not support task + no capacity if full
				expectedErr: domain.ErrWorkerUnhealthy,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				_, err := repo.DispatchJob(ctx, tc.jobID, tc.workerID)
				if err == nil {
					t.Fatalf("expected error %v, got nil", tc.expectedErr)
				}
				if err != tc.expectedErr {
					t.Fatalf("precedence mismatch: expected %v, got %v", tc.expectedErr, err)
				}
			})
		}
	})

	t.Run("CommitAttempt_ValidationsAndOutcome", func(t *testing.T) {
		ctx := context.Background()
		repo := repoFactory(t)

		worker := &domain.Worker{
			ID:             "w_commit",
			Version:        "v1.0.0",
			TotalSlots:     2,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, worker); err != nil {
			t.Fatalf("UpsertWorker failed: %v", err)
		}

		job := &domain.Job{
			ID:             "j_commit",
			TaskType:       "echo",
			Payload:        []byte("payload"),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, job); err != nil {
			t.Fatalf("SubmitJob failed: %v", err)
		}

		att, err := repo.DispatchJob(ctx, "j_commit", "w_commit")
		if err != nil {
			t.Fatalf("DispatchJob failed: %v", err)
		}

		// Wrong worker commit rejected
		_, err = repo.CommitAttempt(ctx, "j_commit", att.ID, att.LeaseToken, "w_wrong", domain.AttemptOutcome{
			ExitCode: 0,
			Output:   "ok",
		})
		if !errors.Is(err, domain.ErrWorkerMismatch) {
			t.Fatalf("expected ErrWorkerMismatch, got %v", err)
		}

		// Wrong lease token rejected
		_, err = repo.CommitAttempt(ctx, "j_commit", att.ID, "invalid_lease_token", "w_commit", domain.AttemptOutcome{
			ExitCode: 0,
			Output:   "ok",
		})
		if !errors.Is(err, domain.ErrStaleAttempt) {
			t.Fatalf("expected ErrStaleAttempt for wrong token, got %v", err)
		}

		// Wrong attempt ID rejected
		_, err = repo.CommitAttempt(ctx, "j_commit", "att_wrong_id", att.LeaseToken, "w_commit", domain.AttemptOutcome{
			ExitCode: 0,
			Output:   "ok",
		})
		if !errors.Is(err, domain.ErrStaleAttempt) && !errors.Is(err, domain.ErrAttemptNotFound) {
			t.Fatalf("expected ErrStaleAttempt or ErrAttemptNotFound, got %v", err)
		}

		// Successful commit (ExitCode = 0)
		committedJob, err := repo.CommitAttempt(ctx, "j_commit", att.ID, att.LeaseToken, "w_commit", domain.AttemptOutcome{
			ExitCode: 0,
			Output:   "success output",
		})
		if err != nil {
			t.Fatalf("CommitAttempt success failed: %v", err)
		}
		if committedJob.State != domain.StateSucceeded {
			t.Fatalf("expected state SUCCEEDED, got %s", committedJob.State)
		}
		if committedJob.Result == nil || committedJob.Result.Output != "success output" {
			t.Fatalf("expected result saved with output 'success output'")
		}

		// Duplicate commit must be rejected
		_, err = repo.CommitAttempt(ctx, "j_commit", att.ID, att.LeaseToken, "w_commit", domain.AttemptOutcome{
			ExitCode: 0,
			Output:   "again",
		})
		if !errors.Is(err, domain.ErrJobAlreadyTerminal) && !errors.Is(err, domain.ErrStaleAttempt) {
			t.Fatalf("expected ErrJobAlreadyTerminal or ErrStaleAttempt on duplicate commit, got %v", err)
		}

		// Worker capacity must be restored to 2
		w, err := repo.GetWorker(ctx, "w_commit")
		if err != nil {
			t.Fatalf("GetWorker failed: %v", err)
		}
		if w.AvailableSlots != 2 {
			t.Fatalf("expected AvailableSlots restored to 2, got %d", w.AvailableSlots)
		}
	})

	t.Run("CancelJob_QueuedAndRunning", func(t *testing.T) {
		ctx := context.Background()
		repo := repoFactory(t)

		worker := &domain.Worker{
			ID:             "w_cancel",
			Version:        "v1.0.0",
			TotalSlots:     2,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, worker); err != nil {
			t.Fatalf("UpsertWorker failed: %v", err)
		}

		// Cancel QUEUED job -> transitions to CANCELLED
		qJob := &domain.Job{
			ID:             "j_cancel_q",
			TaskType:       "echo",
			Payload:        []byte("test"),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, qJob); err != nil {
			t.Fatalf("SubmitJob failed: %v", err)
		}
		cancelledJob, err := repo.CancelJob(ctx, "j_cancel_q")
		if err != nil {
			t.Fatalf("CancelJob on QUEUED failed: %v", err)
		}
		if cancelledJob.State != domain.StateCancelled {
			t.Fatalf("expected state CANCELLED, got %s", cancelledJob.State)
		}

		// Cancelling already terminal job must fail
		_, err = repo.CancelJob(ctx, "j_cancel_q")
		if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
			t.Fatalf("expected ErrJobAlreadyTerminal, got %v", err)
		}

		// Cancel RUNNING job -> transitions to CANCEL_REQUESTED
		rJob := &domain.Job{
			ID:             "j_cancel_r",
			TaskType:       "echo",
			Payload:        []byte("test"),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, rJob); err != nil {
			t.Fatalf("SubmitJob failed: %v", err)
		}
		if _, err := repo.DispatchJob(ctx, "j_cancel_r", "w_cancel"); err != nil {
			t.Fatalf("DispatchJob failed: %v", err)
		}

		reqJob, err := repo.CancelJob(ctx, "j_cancel_r")
		if err != nil {
			t.Fatalf("CancelJob on RUNNING failed: %v", err)
		}
		if reqJob.State != domain.StateCancelRequested {
			t.Fatalf("expected state CANCEL_REQUESTED, got %s", reqJob.State)
		}
	})

	t.Run("WorkerReapAndHeartbeatRecovery", func(t *testing.T) {
		ctx := context.Background()
		repo := repoFactory(t)

		worker := &domain.Worker{
			ID:             "w_reap",
			Version:        "v1.0.0",
			TotalSlots:     1,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, worker); err != nil {
			t.Fatalf("UpsertWorker failed: %v", err)
		}

		// Sleep slightly so database/mock time advances past 10ms
		time.Sleep(50 * time.Millisecond)

		// Reap workers older than 10 milliseconds
		reaped, err := repo.ReapStaleWorkers(ctx, 10*time.Millisecond)
		if err != nil {
			t.Fatalf("ReapStaleWorkers failed: %v", err)
		}
		found := false
		for _, id := range reaped {
			if id == "w_reap" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected worker w_reap to be reaped, reaped list: %v", reaped)
		}

		// Verify status is UNHEALTHY
		w, err := repo.GetWorker(ctx, "w_reap")
		if err != nil {
			t.Fatalf("GetWorker failed: %v", err)
		}
		if w.Status != domain.WorkerStatusUnhealthy {
			t.Fatalf("expected status UNHEALTHY, got %s", w.Status)
		}

		// Record heartbeat restores HEALTHY status
		if err := repo.RecordHeartbeat(ctx, "w_reap", 1); err != nil {
			t.Fatalf("RecordHeartbeat failed: %v", err)
		}
		wAfter, err := repo.GetWorker(ctx, "w_reap")
		if err != nil {
			t.Fatalf("GetWorker after heartbeat failed: %v", err)
		}
		if wAfter.Status != domain.WorkerStatusHealthy {
			t.Fatalf("expected status restored to HEALTHY, got %s", wAfter.Status)
		}
	})
}
