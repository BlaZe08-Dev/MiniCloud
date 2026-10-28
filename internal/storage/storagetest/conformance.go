package storagetest

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
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

	t.Run("DispatchNextJob_PreconditionsAndFIFO", func(t *testing.T) {
		ctx := context.Background()
		repo := repoFactory(t)

		// 1. Worker not found
		_, _, err := repo.DispatchNextJob(ctx, "w_nonexistent")
		if !errors.Is(err, domain.ErrWorkerNotFound) {
			t.Fatalf("expected ErrWorkerNotFound, got: %v", err)
		}

		// Setup unhealthy worker
		unhealthyWorker := &domain.Worker{
			ID:             "w_dnj_unhealthy",
			Version:        "v1.0.0",
			TotalSlots:     2,
			Status:         domain.WorkerStatusUnhealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, unhealthyWorker); err != nil {
			t.Fatalf("UpsertWorker unhealthy failed: %v", err)
		}

		// 2. Worker unhealthy
		_, _, err = repo.DispatchNextJob(ctx, "w_dnj_unhealthy")
		if !errors.Is(err, domain.ErrWorkerUnhealthy) {
			t.Fatalf("expected ErrWorkerUnhealthy, got: %v", err)
		}

		// Setup healthy worker with 1 total slot and consume it
		fullWorker := &domain.Worker{
			ID:             "w_dnj_full",
			Version:        "v1.0.0",
			TotalSlots:     1,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, fullWorker); err != nil {
			t.Fatalf("UpsertWorker fullWorker failed: %v", err)
		}

		jobPre := &domain.Job{
			ID:             "job_dnj_pre",
			TaskType:       "echo",
			Payload:        []byte(`{}`),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, jobPre); err != nil {
			t.Fatalf("SubmitJob jobPre failed: %v", err)
		}
		if _, err := repo.DispatchJob(ctx, "job_dnj_pre", "w_dnj_full"); err != nil {
			t.Fatalf("DispatchJob initial failed: %v", err)
		}

		// 3. Worker no capacity
		_, _, err = repo.DispatchNextJob(ctx, "w_dnj_full")
		if !errors.Is(err, domain.ErrWorkerNoCapacity) {
			t.Fatalf("expected ErrWorkerNoCapacity, got: %v", err)
		}

		// Setup healthy worker with 2 slots supporting "echo"
		echoWorker := &domain.Worker{
			ID:             "w_dnj_echo",
			Version:        "v1.0.0",
			TotalSlots:     2,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, echoWorker); err != nil {
			t.Fatalf("UpsertWorker echoWorker failed: %v", err)
		}

		// 4. Empty queue -> ErrNoQueuedJobs
		_, _, err = repo.DispatchNextJob(ctx, "w_dnj_echo")
		if !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("expected ErrNoQueuedJobs for empty queue, got: %v", err)
		}

		// Submit a job with unsupported task type "sleep"
		sleepJob := &domain.Job{
			ID:             "job_dnj_sleep",
			TaskType:       "sleep",
			Payload:        []byte(`{}`),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, sleepJob); err != nil {
			t.Fatalf("SubmitJob sleep failed: %v", err)
		}

		// 5. Queue has jobs, but worker does not support "sleep" -> ErrNoQueuedJobs
		_, _, err = repo.DispatchNextJob(ctx, "w_dnj_echo")
		if !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("expected ErrNoQueuedJobs for unsupported task in queue, got: %v", err)
		}

		// Submit 2 matching "echo" jobs in sequential order
		echoJob1 := &domain.Job{
			ID:             "job_dnj_echo_1",
			TaskType:       "echo",
			Payload:        []byte(`{"seq":1}`),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, echoJob1); err != nil {
			t.Fatalf("SubmitJob echoJob1 failed: %v", err)
		}

		// Give slight gap so created_at ordering is unambiguous
		time.Sleep(10 * time.Millisecond)

		echoJob2 := &domain.Job{
			ID:             "job_dnj_echo_2",
			TaskType:       "echo",
			Payload:        []byte(`{"seq":2}`),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, echoJob2); err != nil {
			t.Fatalf("SubmitJob echoJob2 failed: %v", err)
		}

		// 6. Sequential poll 1: claims echoJob1 (FIFO)
		j1, att1, err := repo.DispatchNextJob(ctx, "w_dnj_echo")
		if err != nil {
			t.Fatalf("DispatchNextJob 1 failed: %v", err)
		}
		if j1.ID != echoJob1.ID {
			t.Fatalf("expected job %s first (FIFO), got %s", echoJob1.ID, j1.ID)
		}
		if j1.State != domain.StateRunning {
			t.Fatalf("expected job state RUNNING, got %s", j1.State)
		}
		if att1.AttemptNo != 1 {
			t.Fatalf("expected attempt_no 1, got %d", att1.AttemptNo)
		}
		if att1.State != domain.AttemptRunning {
			t.Fatalf("expected attempt state RUNNING, got %s", att1.State)
		}
		if att1.LeaseToken == "" {
			t.Fatalf("expected non-empty raw lease token returned to worker")
		}
		if att1.LeaseTokenHash != domain.HashLeaseToken(att1.LeaseToken) {
			t.Fatalf("lease token hash mismatch")
		}

		// Sequential poll 2: claims echoJob2
		j2, att2, err := repo.DispatchNextJob(ctx, "w_dnj_echo")
		if err != nil {
			t.Fatalf("DispatchNextJob 2 failed: %v", err)
		}
		if j2.ID != echoJob2.ID {
			t.Fatalf("expected job %s second (FIFO), got %s", echoJob2.ID, j2.ID)
		}
		if att2.AttemptNo != 1 {
			t.Fatalf("expected attempt_no 1, got %d", att2.AttemptNo)
		}

		// Sequential poll 3: worker totalSlots was 2, now has 2 active attempts -> ErrWorkerNoCapacity
		_, _, err = repo.DispatchNextJob(ctx, "w_dnj_echo")
		if !errors.Is(err, domain.ErrWorkerNoCapacity) {
			t.Fatalf("expected ErrWorkerNoCapacity on 3rd poll, got: %v", err)
		}

		// Verify audit events recorded for job 1
		events, err := repo.ListEventsByJob(ctx, echoJob1.ID)
		if err != nil {
			t.Fatalf("ListEventsByJob failed: %v", err)
		}
		foundDispatched := false
		for _, ev := range events {
			if ev.EventType == domain.JobEventDispatched {
				foundDispatched = true
				break
			}
		}
		if !foundDispatched {
			t.Fatalf("expected JobEventDispatched event in job 1 audit history")
		}
	})
}

// Harness pairs a repository under test with a backend-specific hook that forces a job
// into an arbitrary state. The hook stands in for transitions that the public repository
// API cannot reach yet (for example CANCEL_REQUESTED -> CANCELLED while the attempt is
// still RUNNING, or RETRYING before the retry slices land).
type Harness struct {
	Repo          storage.Repository
	ForceJobState func(t *testing.T, jobID string, state domain.JobState)
	// ForceAttemptState sets an attempt's state directly (for example LOST, which only the
	// sweeper of a later slice produces). Nil when a test does not need it.
	ForceAttemptState func(t *testing.T, attemptID string, state domain.AttemptState)

	// LeaseDuration is the lease length the repository under test was built with.
	LeaseDuration time.Duration
	// Now reads the repository's own clock (database time for PostgreSQL).
	Now func(t *testing.T) time.Time
	// SetLeaseExpiry sets an attempt's lease_expires_at to repository-now + offset; a negative
	// offset makes the lease already expired. It stands in for waiting out a lease.
	SetLeaseExpiry func(t *testing.T, attemptID string, offset time.Duration)
}

func conformanceWorker(t *testing.T, repo storage.Repository, id string, slots int) {
	t.Helper()
	err := repo.UpsertWorker(context.Background(), &domain.Worker{
		ID:             id,
		Version:        "v1.0.0",
		TotalSlots:     slots,
		Status:         domain.WorkerStatusHealthy,
		SupportedTasks: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("UpsertWorker(%s) failed: %v", id, err)
	}
}

func conformanceQueuedJob(t *testing.T, repo storage.Repository, id string) {
	t.Helper()
	err := repo.SubmitJob(context.Background(), &domain.Job{
		ID:             id,
		TaskType:       "echo",
		Payload:        []byte(`{"message":"x"}`),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob(%s) failed: %v", id, err)
	}
}

// conformanceRunningJob submits a job and dispatches it to workerID.
func conformanceRunningJob(t *testing.T, repo storage.Repository, jobID, workerID string) *domain.Attempt {
	t.Helper()
	conformanceQueuedJob(t, repo, jobID)
	att, err := repo.DispatchJob(context.Background(), jobID, workerID)
	if err != nil {
		t.Fatalf("DispatchJob(%s, %s) failed: %v", jobID, workerID, err)
	}
	return att
}

func conformanceSlots(t *testing.T, repo storage.Repository, workerID string) int {
	t.Helper()
	w, err := repo.GetWorker(context.Background(), workerID)
	if err != nil {
		t.Fatalf("GetWorker(%s) failed: %v", workerID, err)
	}
	return w.AvailableSlots
}

func conformanceAttemptState(t *testing.T, repo storage.Repository, attemptID string) domain.AttemptState {
	t.Helper()
	att, err := repo.GetAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatalf("GetAttempt(%s) failed: %v", attemptID, err)
	}
	return att.State
}

func conformanceEvents(t *testing.T, repo storage.Repository, jobID string) []*domain.JobEvent {
	t.Helper()
	events, err := repo.ListEventsByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ListEventsByJob(%s) failed: %v", jobID, err)
	}
	return events
}

func countEvents(events []*domain.JobEvent, typ domain.JobEventType) int {
	n := 0
	for _, ev := range events {
		if ev.EventType == typ {
			n++
		}
	}
	return n
}

// RunCancelIdempotentCancelRequestedConformance verifies SEC-022 part (a): a duplicate
// cancel on a CANCEL_REQUESTED job is an idempotent repository no-op. It is wrapped by
// TestConformance_CancelJob_IdempotentCancelRequested in each backend's test package.
func RunCancelIdempotentCancelRequestedConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	repo := newHarness(t).Repo
	conformanceWorker(t, repo, "w_idem", 1)
	att := conformanceRunningJob(t, repo, "j_idem", "w_idem")

	first, err := repo.CancelJob(ctx, "j_idem")
	if err != nil {
		t.Fatalf("first CancelJob failed: %v", err)
	}
	if first.State != domain.StateCancelRequested {
		t.Fatalf("first cancel: expected CANCEL_REQUESTED, got %s", first.State)
	}
	eventsAfterFirst := conformanceEvents(t, repo, "j_idem")

	for i := 2; i <= 3; i++ {
		dup, err := repo.CancelJob(ctx, "j_idem")
		if err != nil {
			t.Fatalf("duplicate cancel #%d must be a no-op, got error: %v", i, err)
		}
		if dup.State != domain.StateCancelRequested {
			t.Fatalf("duplicate cancel #%d: expected CANCEL_REQUESTED, got %s", i, dup.State)
		}
		if dup.ID != first.ID || dup.CurrentAttemptID != first.CurrentAttemptID || !dup.UpdatedAt.Equal(first.UpdatedAt) {
			t.Fatalf("duplicate cancel #%d must return the job unchanged: first=%+v dup=%+v", i, first, dup)
		}
	}

	if got := len(conformanceEvents(t, repo, "j_idem")); got != len(eventsAfterFirst) {
		t.Fatalf("duplicate cancel must record no audit event: %d events before, %d after", len(eventsAfterFirst), got)
	}
	if got := conformanceAttemptState(t, repo, att.ID); got != domain.AttemptRunning {
		t.Fatalf("duplicate cancel must leave the attempt RUNNING, got %s", got)
	}
	if got := conformanceSlots(t, repo, "w_idem"); got != 0 {
		t.Fatalf("worker slot must stay held while the attempt runs, available=%d", got)
	}

	// The worker's own commit must still be accepted: the job was not finalised early.
	job, err := repo.CommitAttempt(ctx, "j_idem", att.ID, att.LeaseToken, "w_idem", domain.AttemptOutcome{ExitCode: 0, Output: "done"})
	if err != nil {
		t.Fatalf("worker commit after duplicate cancels must be accepted, got: %v", err)
	}
	if job.State != domain.StateSucceeded {
		t.Fatalf("expected SUCCEEDED after commit, got %s", job.State)
	}
	if got := conformanceAttemptState(t, repo, att.ID); got != domain.AttemptCompleted {
		t.Fatalf("expected attempt COMPLETED, got %s", got)
	}
	if got := conformanceSlots(t, repo, "w_idem"); got != 1 {
		t.Fatalf("worker slot must be free after commit, available=%d", got)
	}
}

// RunCancelRetryingImmediatelyCancelledConformance verifies that cancelling a RETRYING
// job moves it straight to CANCELLED. Wrapped by
// TestConformance_CancelJob_RetryingImmediatelyCancelled.
func RunCancelRetryingImmediatelyCancelledConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	h := newHarness(t)
	repo := h.Repo
	conformanceQueuedJob(t, repo, "j_retry_cancel")
	h.ForceJobState(t, "j_retry_cancel", domain.StateRetrying)

	job, err := repo.CancelJob(ctx, "j_retry_cancel")
	if err != nil {
		t.Fatalf("CancelJob on RETRYING failed: %v", err)
	}
	if job.State != domain.StateCancelled {
		t.Fatalf("expected CANCELLED, got %s", job.State)
	}
	if job.NotBefore != nil {
		t.Fatalf("expected not_before cleared, got %v", job.NotBefore)
	}

	events := conformanceEvents(t, repo, "j_retry_cancel")
	last := events[len(events)-1]
	if last.EventType != domain.JobEventCancelled || last.FromState == nil || *last.FromState != domain.StateRetrying {
		t.Fatalf("expected JOB_CANCELLED from RETRYING as last event, got %+v", last)
	}

	if _, err := repo.CancelJob(ctx, "j_retry_cancel"); !errors.Is(err, domain.ErrJobAlreadyTerminal) {
		t.Fatalf("second cancel on CANCELLED job: expected ErrJobAlreadyTerminal, got %v", err)
	}
}

// RunCommitAttemptTerminalJobConformance verifies SEC-022 part (b): a commit on an
// already-terminal job is rejected with ErrJobAlreadyTerminal, and the same call closes
// the attempt only while it is still RUNNING, frees the worker slot and records a bounded
// diagnostic event. Wrapped by TestConformance_CommitAttempt_TerminalJob.
func RunCommitAttemptTerminalJobConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	outcome := domain.AttemptOutcome{ExitCode: 0, Output: "late"}

	forced := []struct {
		name         string
		jobState     domain.JobState
		wantAttempt  domain.AttemptState
		wantJobState domain.JobState
	}{
		{"cancelled_job_closes_attempt_cancelled", domain.StateCancelled, domain.AttemptCancelled, domain.StateCancelled},
		{"succeeded_job_closes_attempt_lost", domain.StateSucceeded, domain.AttemptLost, domain.StateSucceeded},
		{"failed_job_closes_attempt_lost", domain.StateFailed, domain.AttemptLost, domain.StateFailed},
	}
	for _, tc := range forced {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			repo := h.Repo
			conformanceWorker(t, repo, "w_term", 1)
			att := conformanceRunningJob(t, repo, "j_term", "w_term")
			h.ForceJobState(t, "j_term", tc.jobState)

			_, err := repo.CommitAttempt(ctx, "j_term", att.ID, att.LeaseToken, "w_term", outcome)
			if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
				t.Fatalf("expected ErrJobAlreadyTerminal, got %v", err)
			}
			closed, err := repo.GetAttempt(ctx, att.ID)
			if err != nil {
				t.Fatalf("GetAttempt failed: %v", err)
			}
			if closed.State != tc.wantAttempt {
				t.Fatalf("expected attempt %s, got %s", tc.wantAttempt, closed.State)
			}
			if closed.FinishedAt == nil {
				t.Fatalf("closed attempt must carry finished_at")
			}
			if got := conformanceSlots(t, repo, "w_term"); got != 1 {
				t.Fatalf("worker slot must be freed by the rejected commit, available=%d", got)
			}
			job, err := repo.GetJob(ctx, "j_term")
			if err != nil {
				t.Fatalf("GetJob failed: %v", err)
			}
			if job.State != tc.wantJobState || job.Result != nil {
				t.Fatalf("rejected commit must not change the job or store a result: %+v", job)
			}
			if got := countEvents(conformanceEvents(t, repo, "j_term"), domain.JobEventDiagnosticLateResult); got != 1 {
				t.Fatalf("expected exactly 1 DIAGNOSTIC_LATE_RESULT event, got %d", got)
			}

			// A retry of the same commit is rejected again: no second event, attempt untouched.
			if _, err := repo.CommitAttempt(ctx, "j_term", att.ID, att.LeaseToken, "w_term", outcome); !errors.Is(err, domain.ErrJobAlreadyTerminal) {
				t.Fatalf("retried commit: expected ErrJobAlreadyTerminal, got %v", err)
			}
			if got := countEvents(conformanceEvents(t, repo, "j_term"), domain.JobEventDiagnosticLateResult); got != 1 {
				t.Fatalf("diagnostic event must be bounded to one per attempt, got %d", got)
			}
			if got := conformanceAttemptState(t, repo, att.ID); got != tc.wantAttempt {
				t.Fatalf("retried commit changed the attempt to %s", got)
			}
		})
	}

	t.Run("completed_attempt_never_overwritten", func(t *testing.T) {
		h := newHarness(t)
		repo := h.Repo
		conformanceWorker(t, repo, "w_done", 1)
		att := conformanceRunningJob(t, repo, "j_done", "w_done")
		if _, err := repo.CommitAttempt(ctx, "j_done", att.ID, att.LeaseToken, "w_done", domain.AttemptOutcome{ExitCode: 0, Output: "first"}); err != nil {
			t.Fatalf("first commit failed: %v", err)
		}
		before, _ := repo.GetAttempt(ctx, att.ID)

		_, err := repo.CommitAttempt(ctx, "j_done", att.ID, att.LeaseToken, "w_done", domain.AttemptOutcome{ExitCode: 1, Error: "replay"})
		if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
			t.Fatalf("expected ErrJobAlreadyTerminal, got %v", err)
		}
		after, _ := repo.GetAttempt(ctx, att.ID)
		if after.State != domain.AttemptCompleted || after.Output != before.Output || after.ExitCode != before.ExitCode {
			t.Fatalf("a COMPLETED attempt must never be overwritten: before=%+v after=%+v", before, after)
		}
		job, _ := repo.GetJob(ctx, "j_done")
		if job.State != domain.StateSucceeded || job.Result == nil || job.Result.Output != "first" {
			t.Fatalf("the first result must stand: %+v", job)
		}
	})

	t.Run("foreign_attempt_is_not_closed", func(t *testing.T) {
		h := newHarness(t)
		repo := h.Repo
		conformanceWorker(t, repo, "w_foreign", 2)
		attA := conformanceRunningJob(t, repo, "j_foreign_a", "w_foreign")
		attB := conformanceRunningJob(t, repo, "j_foreign_b", "w_foreign")
		h.ForceJobState(t, "j_foreign_a", domain.StateCancelled)

		// Terminal job A, but the caller names job B's attempt.
		_, err := repo.CommitAttempt(ctx, "j_foreign_a", attB.ID, attB.LeaseToken, "w_foreign", outcome)
		if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
			t.Fatalf("expected ErrJobAlreadyTerminal, got %v", err)
		}
		if got := conformanceAttemptState(t, repo, attB.ID); got != domain.AttemptRunning {
			t.Fatalf("an attempt of another job must not be closed, got %s", got)
		}
		if got := countEvents(conformanceEvents(t, repo, "j_foreign_a"), domain.JobEventDiagnosticLateResult); got != 0 {
			t.Fatalf("no diagnostic event may be recorded for an attempt of another job, got %d", got)
		}
		_ = attA
	})

	t.Run("unknown_attempt_records_nothing", func(t *testing.T) {
		h := newHarness(t)
		repo := h.Repo
		conformanceWorker(t, repo, "w_unknown", 1)
		conformanceRunningJob(t, repo, "j_unknown", "w_unknown")
		h.ForceJobState(t, "j_unknown", domain.StateCancelled)

		for i := 0; i < 3; i++ {
			_, err := repo.CommitAttempt(ctx, "j_unknown", "att_does_not_exist", "tok", "w_unknown", outcome)
			if !errors.Is(err, domain.ErrJobAlreadyTerminal) {
				t.Fatalf("expected ErrJobAlreadyTerminal, got %v", err)
			}
		}
		if got := countEvents(conformanceEvents(t, repo, "j_unknown"), domain.JobEventDiagnosticLateResult); got != 0 {
			t.Fatalf("unauthenticated junk attempt IDs must not grow job_events, got %d diagnostic events", got)
		}
	})
}

type modelAttempt struct {
	id     string
	token  string
	worker string
	state  domain.AttemptState
}

type modelJob struct {
	id       string
	state    domain.JobState
	attempts []*modelAttempt
}

func (m *modelJob) current() *modelAttempt {
	if len(m.attempts) == 0 {
		return nil
	}
	return m.attempts[len(m.attempts)-1]
}

// RunStateMachineModelConformance drives a repository and a small reference model with the same
// seeded random sequence of submit, poll, commit (valid, wrong token, replay) and cancel
// operations, and requires identical job states, attempt states, error classes and worker slot
// counts after every step. It also checks the no-active-attempt-after-terminal invariant
// (fault-injection-plan.md invariant 4). Wrapped by TestProperty_StateMachine_ModelConformance.
func RunStateMachineModelConformance(t *testing.T, newHarness func(t *testing.T) Harness, seeds []int64, steps int) {
	for _, seed := range seeds {
		seed := seed
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			runModelSeed(t, newHarness(t).Repo, seed, steps)
		})
	}
}

func runModelSeed(t *testing.T, repo storage.Repository, seed int64, steps int) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	workers := map[string]int{"w_model_a": 2, "w_model_b": 1}
	workerIDs := []string{"w_model_a", "w_model_b"}
	for _, id := range workerIDs {
		conformanceWorker(t, repo, id, workers[id])
	}

	var order []*modelJob
	jobs := map[string]*modelJob{}
	freeSlots := func(workerID string) int {
		used := 0
		for _, j := range order {
			for _, a := range j.attempts {
				if a.worker == workerID && a.state == domain.AttemptRunning {
					used++
				}
			}
		}
		return workers[workerID] - used
	}
	pick := func(pred func(*modelJob) bool) *modelJob {
		var cands []*modelJob
		for _, j := range order {
			if pred(j) {
				cands = append(cands, j)
			}
		}
		if len(cands) == 0 {
			return nil
		}
		return cands[rng.Intn(len(cands))]
	}

	check := func(step int, op string, j *modelJob) {
		t.Helper()
		if j != nil {
			got, err := repo.GetJob(ctx, j.id)
			if err != nil {
				t.Fatalf("seed %d step %d (%s): GetJob(%s): %v", seed, step, op, j.id, err)
			}
			if got.State != j.state {
				t.Fatalf("seed %d step %d (%s): job %s state: repo=%s model=%s", seed, step, op, j.id, got.State, j.state)
			}
			atts, err := repo.ListAttemptsByJob(ctx, j.id)
			if err != nil {
				t.Fatalf("seed %d step %d (%s): ListAttemptsByJob: %v", seed, step, op, err)
			}
			if len(atts) != len(j.attempts) {
				t.Fatalf("seed %d step %d (%s): job %s attempts: repo=%d model=%d", seed, step, op, j.id, len(atts), len(j.attempts))
			}
			for _, a := range atts {
				var want *modelAttempt
				for _, m := range j.attempts {
					if m.id == a.ID {
						want = m
					}
				}
				if want == nil || want.state != a.State {
					t.Fatalf("seed %d step %d (%s): job %s attempt %s: repo=%s model=%v", seed, step, op, j.id, a.ID, a.State, want)
				}
				if j.state.IsTerminal() && (a.State == domain.AttemptRunning || a.State == domain.AttemptPending) {
					t.Fatalf("seed %d step %d (%s): INVARIANT: terminal job %s has active attempt %s", seed, step, op, j.id, a.ID)
				}
			}
		}
		for _, id := range workerIDs {
			if got := conformanceSlots(t, repo, id); got != freeSlots(id) {
				t.Fatalf("seed %d step %d (%s): worker %s slots: repo=%d model=%d", seed, step, op, id, got, freeSlots(id))
			}
		}
	}

	wantErr := func(step int, op string, got, want error) {
		t.Helper()
		if want == nil {
			if got != nil {
				t.Fatalf("seed %d step %d (%s): unexpected error: %v", seed, step, op, got)
			}
			return
		}
		if !errors.Is(got, want) {
			t.Fatalf("seed %d step %d (%s): expected %v, got %v", seed, step, op, want, got)
		}
	}

	for step := 0; step < steps; step++ {
		switch roll := rng.Intn(100); {
		case roll < 22: // submit
			id := fmt.Sprintf("j_model_%d_%d", seed, len(order))
			conformanceQueuedJob(t, repo, id)
			j := &modelJob{id: id, state: domain.StateQueued}
			order = append(order, j)
			jobs[id] = j
			check(step, "submit", j)

		case roll < 50: // poll
			workerID := workerIDs[rng.Intn(len(workerIDs))]
			job, att, err := repo.DispatchNextJob(ctx, workerID)
			var next *modelJob
			for _, j := range order {
				if j.state == domain.StateQueued {
					next = j
					break
				}
			}
			switch {
			case freeSlots(workerID) <= 0:
				wantErr(step, "poll", err, domain.ErrWorkerNoCapacity)
				check(step, "poll", nil)
			case next == nil:
				wantErr(step, "poll", err, domain.ErrNoQueuedJobs)
				check(step, "poll", nil)
			default:
				wantErr(step, "poll", err, nil)
				if job.ID != next.id {
					t.Fatalf("seed %d step %d (poll): FIFO violated: repo dispatched %s, model expected %s", seed, step, job.ID, next.id)
				}
				next.state = domain.StateRunning
				next.attempts = append(next.attempts, &modelAttempt{id: att.ID, token: att.LeaseToken, worker: workerID, state: domain.AttemptRunning})
				check(step, "poll", next)
			}

		case roll < 70: // valid commit on a job that has a RUNNING attempt (RUNNING or CANCEL_REQUESTED)
			j := pick(func(j *modelJob) bool { return j.current() != nil && j.current().state == domain.AttemptRunning })
			if j == nil {
				continue
			}
			a := j.current()
			outcome := domain.AttemptOutcome{ExitCode: 0, Output: "ok"}
			wantAttempt, wantJob := domain.AttemptCompleted, domain.StateSucceeded
			if rng.Intn(3) == 0 {
				outcome = domain.AttemptOutcome{ExitCode: 1, Error: "boom"}
				wantAttempt, wantJob = domain.AttemptFailed, domain.StateFailed
			}
			_, err := repo.CommitAttempt(ctx, j.id, a.id, a.token, a.worker, outcome)
			wantErr(step, "commit", err, nil)
			a.state, j.state = wantAttempt, wantJob
			check(step, "commit", j)

		case roll < 78: // commit with a wrong lease token is rejected and changes nothing
			j := pick(func(j *modelJob) bool { return j.current() != nil && j.current().state == domain.AttemptRunning })
			if j == nil {
				continue
			}
			a := j.current()
			_, err := repo.CommitAttempt(ctx, j.id, a.id, "wrong-token", a.worker, domain.AttemptOutcome{ExitCode: 0})
			wantErr(step, "commit-stale", err, domain.ErrStaleAttempt)
			check(step, "commit-stale", j)

		case roll < 86: // replayed commit on a terminal job
			j := pick(func(j *modelJob) bool { return j.state.IsTerminal() && j.current() != nil })
			if j == nil {
				continue
			}
			a := j.current()
			_, err := repo.CommitAttempt(ctx, j.id, a.id, a.token, a.worker, domain.AttemptOutcome{ExitCode: 0})
			wantErr(step, "commit-replay", err, domain.ErrJobAlreadyTerminal)
			check(step, "commit-replay", j)

		default: // cancel
			j := pick(func(j *modelJob) bool { return true })
			if j == nil {
				continue
			}
			_, err := repo.CancelJob(ctx, j.id)
			switch {
			case j.state.IsTerminal():
				wantErr(step, "cancel", err, domain.ErrJobAlreadyTerminal)
			case j.state == domain.StateQueued:
				wantErr(step, "cancel", err, nil)
				j.state = domain.StateCancelled
			case j.state == domain.StateRunning:
				wantErr(step, "cancel", err, nil)
				j.state = domain.StateCancelRequested
			default: // CANCEL_REQUESTED: idempotent no-op
				wantErr(step, "cancel", err, nil)
			}
			check(step, "cancel", j)
		}
	}

	for _, j := range order {
		check(steps, "final", j)
	}
}

// RunCommitAttemptNonRunningAttemptConformance verifies that a commit naming an attempt that is no
// longer RUNNING is rejected with ErrStaleAttempt while the job is still non-terminal, and that the
// rejection changes nothing: job state, attempt, worker slot, events and stored result stay as they
// were. In slice 4a no public repository path leaves a non-terminal job with a non-RUNNING current
// attempt, so the states below are all reached through the harness's ForceAttemptState
// hook. Wrapped by TestConformance_CommitAttempt_NonRunningAttempt_ReturnsStaleAttempt.
func RunCommitAttemptNonRunningAttemptConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	// LOST is deliberately absent: since slice 4b a commit naming a LOST attempt returns
	// ErrLeaseExpired (RunCommitAttemptConflictOrderConformance). Every other non-RUNNING
	// attempt state keeps returning ErrStaleAttempt.
	for _, state := range []domain.AttemptState{domain.AttemptCompleted, domain.AttemptFailed, domain.AttemptCancelled, domain.AttemptTimedOut} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			h := newHarness(t)
			repo := h.Repo
			conformanceWorker(t, repo, "w_stale", 1)
			att := conformanceRunningJob(t, repo, "j_stale", "w_stale")
			h.ForceAttemptState(t, att.ID, state)

			beforeJob, _ := repo.GetJob(ctx, "j_stale")
			beforeAtt, _ := repo.GetAttempt(ctx, att.ID)
			beforeEvents := len(conformanceEvents(t, repo, "j_stale"))
			beforeSlots := conformanceSlots(t, repo, "w_stale")

			_, err := repo.CommitAttempt(ctx, "j_stale", att.ID, att.LeaseToken, "w_stale", domain.AttemptOutcome{ExitCode: 0, Output: "late"})
			if !errors.Is(err, domain.ErrStaleAttempt) {
				t.Fatalf("commit on a %s attempt: expected ErrStaleAttempt, got %v", state, err)
			}

			job, _ := repo.GetJob(ctx, "j_stale")
			if job.State != beforeJob.State || job.Result != nil || !job.UpdatedAt.Equal(beforeJob.UpdatedAt) {
				t.Fatalf("rejected commit changed the job: before=%+v after=%+v", beforeJob, job)
			}
			after, _ := repo.GetAttempt(ctx, att.ID)
			if after.State != state || after.Output != beforeAtt.Output || after.ExitCode != beforeAtt.ExitCode || (after.FinishedAt == nil) != (beforeAtt.FinishedAt == nil) {
				t.Fatalf("rejected commit changed the attempt: before=%+v after=%+v", beforeAtt, after)
			}
			if got := conformanceSlots(t, repo, "w_stale"); got != beforeSlots {
				t.Fatalf("rejected commit changed the worker slot count: before=%d after=%d", beforeSlots, got)
			}
			if got := len(conformanceEvents(t, repo, "j_stale")); got != beforeEvents {
				t.Fatalf("rejected commit recorded an event: before=%d after=%d", beforeEvents, got)
			}
		})
	}
}

// ---- Slice 4b: leases, renewal, sweep, conflict order ----------------------------------------

const leaseTolerance = 2 * time.Millisecond // PostgreSQL stores microseconds

func conformanceJobN(t *testing.T, repo storage.Repository, id string, maxAttempts int) {
	t.Helper()
	err := repo.SubmitJob(context.Background(), &domain.Job{
		ID: id, TaskType: "echo", Payload: []byte(`{"message":"x"}`),
		State: domain.StateQueued, MaxAttempts: maxAttempts, TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob(%s) failed: %v", id, err)
	}
}

// conformanceDispatchNext dispatches the next job to workerID and returns the attempt.
func conformanceDispatchNext(t *testing.T, repo storage.Repository, workerID string) *domain.Attempt {
	t.Helper()
	_, att, err := repo.DispatchNextJob(context.Background(), workerID)
	if err != nil {
		t.Fatalf("DispatchNextJob(%s) failed: %v", workerID, err)
	}
	return att
}

func conformanceBackoff(d time.Duration) domain.BackoffFunc {
	return func(int) time.Duration { return d }
}

func conformanceSweep(t *testing.T, repo storage.Repository, p domain.SweepParams) *domain.SweepResult {
	t.Helper()
	if p.Backoff == nil {
		p.Backoff = conformanceBackoff(time.Hour)
	}
	res, err := repo.SweepExpiredLeases(context.Background(), p)
	if err != nil {
		t.Fatalf("SweepExpiredLeases failed: %v", err)
	}
	return res
}

func conformanceAttempt(t *testing.T, repo storage.Repository, id string) *domain.Attempt {
	t.Helper()
	att, err := repo.GetAttempt(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAttempt(%s) failed: %v", id, err)
	}
	return att
}

func conformanceJob(t *testing.T, repo storage.Repository, id string) *domain.Job {
	t.Helper()
	j, err := repo.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s) failed: %v", id, err)
	}
	return j
}

func requireLeaseHooks(t *testing.T, h Harness) {
	t.Helper()
	if h.Now == nil || h.SetLeaseExpiry == nil || h.LeaseDuration <= 0 {
		t.Fatalf("harness must provide Now, SetLeaseExpiry and LeaseDuration")
	}
}

func timesClose(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= leaseTolerance
}

func requireLease(t *testing.T, att *domain.Attempt) time.Time {
	t.Helper()
	if att.LeaseExpiresAt == nil {
		t.Fatalf("attempt %s has no lease_expires_at", att.ID)
	}
	return *att.LeaseExpiresAt
}

func eventTypes(events []*domain.JobEvent) []domain.JobEventType {
	out := make([]domain.JobEventType, len(events))
	for i, e := range events {
		out[i] = e.EventType
	}
	return out
}

func requireEventTail(t *testing.T, events []*domain.JobEvent, want ...domain.JobEventType) {
	t.Helper()
	got := eventTypes(events)
	if len(got) < len(want) {
		t.Fatalf("events %v: shorter than expected tail %v", got, want)
	}
	tail := got[len(got)-len(want):]
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("event tail = %v, want %v (all events %v)", tail, want, got)
		}
	}
}

// RunDispatchLeaseConformance verifies that every dispatch path stamps the new attempt with a
// lease of the configured length, measured on the repository clock. Wrapped by
// TestConformance_Dispatch_StampsLease.
func RunDispatchLeaseConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	for _, path := range []string{"DispatchJob", "DispatchNextJob"} {
		path := path
		t.Run(path, func(t *testing.T) {
			h := newHarness(t)
			requireLeaseHooks(t, h)
			conformanceWorker(t, h.Repo, "w_l", 1)
			conformanceQueuedJob(t, h.Repo, "j_l")
			lo := h.Now(t)
			var att *domain.Attempt
			if path == "DispatchJob" {
				a, err := h.Repo.DispatchJob(context.Background(), "j_l", "w_l")
				if err != nil {
					t.Fatalf("DispatchJob failed: %v", err)
				}
				att = a
			} else {
				att = conformanceDispatchNext(t, h.Repo, "w_l")
			}
			hi := h.Now(t)
			lease := requireLease(t, conformanceAttempt(t, h.Repo, att.ID))
			if lease.Before(lo.Add(h.LeaseDuration).Add(-leaseTolerance)) || lease.After(hi.Add(h.LeaseDuration).Add(leaseTolerance)) {
				t.Fatalf("lease_expires_at %v not within [%v, %v] (dispatch time + %v)", lease, lo.Add(h.LeaseDuration), hi.Add(h.LeaseDuration), h.LeaseDuration)
			}
			if att.LeaseExpiresAt == nil || !timesClose(*att.LeaseExpiresAt, lease) {
				t.Fatalf("returned attempt lease %v does not match stored lease %v", att.LeaseExpiresAt, lease)
			}
		})
	}
}

// RunRenewLeaseAcceptUntilSweptConformance verifies RenewLease under the symmetric
// accept-until-swept rule (ADR-0014): an unswept RUNNING attempt is renewed from repository-now
// even after its lease passed, a CANCEL_REQUESTED job gets cancel_requested and no extension, and
// every rejection leaves the lease and the event log untouched. Wrapped by
// TestConformance_RenewLease_AcceptUntilSwept.
func RunRenewLeaseAcceptUntilSweptConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	setup := func(t *testing.T) (Harness, *domain.Attempt) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_r", 2)
		return h, conformanceRunningJob(t, h.Repo, "j_r", "w_r")
	}

	t.Run("extends_from_repository_now_and_records_no_event", func(t *testing.T) {
		h, att := setup(t)
		h.SetLeaseExpiry(t, att.ID, 2*time.Second)
		eventsBefore := len(conformanceEvents(t, h.Repo, "j_r"))
		lo := h.Now(t)
		got, err := h.Repo.RenewLease(ctx, "j_r", att.ID, att.LeaseToken, "w_r")
		hi := h.Now(t)
		if err != nil {
			t.Fatalf("RenewLease failed: %v", err)
		}
		if got.CancelRequested {
			t.Fatalf("cancel_requested = true on a RUNNING job")
		}
		if got.LeaseExpiresAt.Before(lo.Add(h.LeaseDuration).Add(-leaseTolerance)) || got.LeaseExpiresAt.After(hi.Add(h.LeaseDuration).Add(leaseTolerance)) {
			t.Fatalf("renewed expiry %v not within [%v, %v]", got.LeaseExpiresAt, lo.Add(h.LeaseDuration), hi.Add(h.LeaseDuration))
		}
		stored := conformanceAttempt(t, h.Repo, att.ID)
		if !timesClose(requireLease(t, stored), got.LeaseExpiresAt) || stored.State != domain.AttemptRunning {
			t.Fatalf("stored attempt %+v does not reflect the renewal %v", stored, got.LeaseExpiresAt)
		}
		if n := len(conformanceEvents(t, h.Repo, "j_r")); n != eventsBefore {
			t.Fatalf("renewal recorded %d event(s); a renewal must not write to the audit log", n-eventsBefore)
		}
		if job := conformanceJob(t, h.Repo, "j_r"); job.State != domain.StateRunning {
			t.Fatalf("job state after renewal = %s", job.State)
		}
	})

	t.Run("accepted_after_the_lease_passed_while_unswept", func(t *testing.T) {
		h, att := setup(t)
		h.SetLeaseExpiry(t, att.ID, -time.Minute)
		lo := h.Now(t)
		got, err := h.Repo.RenewLease(ctx, "j_r", att.ID, att.LeaseToken, "w_r")
		if err != nil {
			t.Fatalf("renewal of an expired but unswept lease rejected: %v", err)
		}
		if got.LeaseExpiresAt.Before(lo.Add(h.LeaseDuration).Add(-leaseTolerance)) {
			t.Fatalf("expired lease was not extended from now: %v", got.LeaseExpiresAt)
		}
		res := conformanceSweep(t, h.Repo, domain.SweepParams{})
		if len(res.Swept) != 0 || res.Candidates != 0 {
			t.Fatalf("a renewed attempt was still a sweep candidate: %+v", res)
		}
	})

	t.Run("cancel_requested_returns_flag_and_does_not_extend", func(t *testing.T) {
		for _, offset := range []time.Duration{5 * time.Second, -time.Minute} {
			h, att := setup(t)
			h.SetLeaseExpiry(t, att.ID, offset)
			before := requireLease(t, conformanceAttempt(t, h.Repo, att.ID))
			if _, err := h.Repo.CancelJob(ctx, "j_r"); err != nil {
				t.Fatalf("CancelJob failed: %v", err)
			}
			got, err := h.Repo.RenewLease(ctx, "j_r", att.ID, att.LeaseToken, "w_r")
			if err != nil {
				t.Fatalf("renewal on CANCEL_REQUESTED (lease offset %v) failed: %v", offset, err)
			}
			if !got.CancelRequested {
				t.Fatalf("cancel_requested = false on a CANCEL_REQUESTED job (lease offset %v)", offset)
			}
			if !timesClose(got.LeaseExpiresAt, before) {
				t.Fatalf("lease offset %v: returned expiry %v differs from the current expiry %v; it must not be extended", offset, got.LeaseExpiresAt, before)
			}
			if after := requireLease(t, conformanceAttempt(t, h.Repo, att.ID)); !timesClose(after, before) {
				t.Fatalf("lease offset %v: stored expiry moved %v -> %v on a CANCEL_REQUESTED job", offset, before, after)
			}
		}
	})

	t.Run("rejections_leave_lease_and_events_untouched", func(t *testing.T) {
		cases := []struct {
			name    string
			prepare func(h Harness, att *domain.Attempt)
			call    func(h Harness, att *domain.Attempt) error
			check   func(err error) bool
		}{
			{"wrong_token", nil, func(h Harness, att *domain.Attempt) error {
				_, err := h.Repo.RenewLease(ctx, "j_r", att.ID, "not-the-token", "w_r")
				return err
			}, func(err error) bool { return errors.Is(err, domain.ErrStaleAttempt) }},
			{"wrong_worker", nil, func(h Harness, att *domain.Attempt) error {
				_, err := h.Repo.RenewLease(ctx, "j_r", att.ID, att.LeaseToken, "w_intruder")
				return err
			}, func(err error) bool { return errors.Is(err, domain.ErrWorkerMismatch) }},
			{"unknown_job", nil, func(h Harness, att *domain.Attempt) error {
				_, err := h.Repo.RenewLease(ctx, "no_such_job", att.ID, att.LeaseToken, "w_r")
				return err
			}, func(err error) bool { return errors.Is(err, domain.ErrJobNotFound) }},
			{"unknown_attempt", nil, func(h Harness, att *domain.Attempt) error {
				_, err := h.Repo.RenewLease(ctx, "j_r", "no_such_attempt", att.LeaseToken, "w_r")
				return err
			}, func(err error) bool { return errors.Is(err, domain.ErrStaleAttempt) }},
			{"attempt_of_another_job", func(h Harness, att *domain.Attempt) {
				conformanceWorker(t, h.Repo, "w_other", 1)
				conformanceRunningJob(t, h.Repo, "j_other", "w_other")
			}, func(h Harness, att *domain.Attempt) error {
				other, _ := h.Repo.ListAttemptsByJob(ctx, "j_other")
				_, err := h.Repo.RenewLease(ctx, "j_r", other[0].ID, att.LeaseToken, "w_r")
				return err
			}, func(err error) bool { return errors.Is(err, domain.ErrStaleAttempt) }},
			{"terminal_job", func(h Harness, att *domain.Attempt) {
				h.ForceJobState(t, "j_r", domain.StateSucceeded)
			}, func(h Harness, att *domain.Attempt) error {
				_, err := h.Repo.RenewLease(ctx, "j_r", att.ID, att.LeaseToken, "w_r")
				return err
			}, func(err error) bool { return errors.Is(err, domain.ErrJobAlreadyTerminal) }},
		}
		for _, state := range []domain.AttemptState{domain.AttemptCompleted, domain.AttemptFailed, domain.AttemptCancelled, domain.AttemptTimedOut} {
			state := state
			cases = append(cases, struct {
				name    string
				prepare func(h Harness, att *domain.Attempt)
				call    func(h Harness, att *domain.Attempt) error
				check   func(err error) bool
			}{"attempt_" + string(state), func(h Harness, att *domain.Attempt) {
				h.ForceAttemptState(t, att.ID, state)
			}, func(h Harness, att *domain.Attempt) error {
				_, err := h.Repo.RenewLease(ctx, "j_r", att.ID, att.LeaseToken, "w_r")
				return err
			}, func(err error) bool { return errors.Is(err, domain.ErrStaleAttempt) }})
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				h, att := setup(t)
				h.SetLeaseExpiry(t, att.ID, 3*time.Second)
				if tc.prepare != nil {
					tc.prepare(h, att)
				}
				leaseBefore := requireLease(t, conformanceAttempt(t, h.Repo, att.ID))
				stateBefore := conformanceAttempt(t, h.Repo, att.ID).State
				eventsBefore := len(conformanceEvents(t, h.Repo, "j_r"))
				err := tc.call(h, att)
				if err == nil || !tc.check(err) {
					t.Fatalf("unexpected renewal result: %v", err)
				}
				after := conformanceAttempt(t, h.Repo, att.ID)
				if !timesClose(requireLease(t, after), leaseBefore) || after.State != stateBefore {
					t.Fatalf("rejected renewal changed the attempt: lease %v -> %v, state %s -> %s", leaseBefore, requireLease(t, after), stateBefore, after.State)
				}
				if n := len(conformanceEvents(t, h.Repo, "j_r")); n != eventsBefore {
					t.Fatalf("rejected renewal recorded %d event(s)", n-eventsBefore)
				}
			})
		}
	})
}

// RunRenewLeaseRejectedAfterLostConformance verifies that a renewal naming an attempt the sweeper
// already marked LOST gets ErrLeaseExpired, whether or not a newer attempt exists, and never
// resurrects it. Wrapped by TestConformance_RenewLease_RejectedAfterLost.
func RunRenewLeaseRejectedAfterLostConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	assertNotResurrected := func(t *testing.T, h Harness, attemptID string, leaseBefore time.Time) {
		t.Helper()
		att := conformanceAttempt(t, h.Repo, attemptID)
		if att.State != domain.AttemptLost || !timesClose(requireLease(t, att), leaseBefore) {
			t.Fatalf("LOST attempt changed by a rejected renewal: %+v (lease before %v)", att, leaseBefore)
		}
	}

	t.Run("after_sweep_job_retrying", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_x", 1)
		att := conformanceRunningJob(t, h.Repo, "j_x", "w_x")
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		conformanceSweep(t, h.Repo, domain.SweepParams{})
		leaseBefore := requireLease(t, conformanceAttempt(t, h.Repo, att.ID))
		eventsBefore := len(conformanceEvents(t, h.Repo, "j_x"))

		_, err := h.Repo.RenewLease(ctx, "j_x", att.ID, att.LeaseToken, "w_x")
		if !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("renewal after sweep: want ErrLeaseExpired, got %v", err)
		}
		assertNotResurrected(t, h, att.ID, leaseBefore)
		if job := conformanceJob(t, h.Repo, "j_x"); job.State != domain.StateRetrying {
			t.Fatalf("job state = %s, want RETRYING (renewal must not resurrect the attempt)", job.State)
		}
		if n := len(conformanceEvents(t, h.Repo, "j_x")); n != eventsBefore {
			t.Fatalf("rejected renewal recorded %d event(s)", n-eventsBefore)
		}
		if got := conformanceSlots(t, h.Repo, "w_x"); got != 1 {
			t.Fatalf("worker slots = %d, want 1 (LOST attempt holds no slot)", got)
		}
	})

	t.Run("superseded_lost_attempt", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_x", 1)
		att1 := conformanceRunningJob(t, h.Repo, "j_x", "w_x")
		h.SetLeaseExpiry(t, att1.ID, -time.Second)
		conformanceSweep(t, h.Repo, domain.SweepParams{Backoff: conformanceBackoff(0)})
		att2 := conformanceDispatchNext(t, h.Repo, "w_x")
		if att2.AttemptNo != 2 {
			t.Fatalf("second attempt number = %d", att2.AttemptNo)
		}
		lease1 := requireLease(t, conformanceAttempt(t, h.Repo, att1.ID))
		lease2 := requireLease(t, conformanceAttempt(t, h.Repo, att2.ID))

		// Old attempt: LEASE_EXPIRED (it is LOST), even though it is also superseded.
		if _, err := h.Repo.RenewLease(ctx, "j_x", att1.ID, att1.LeaseToken, "w_x"); !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("renewal of the superseded LOST attempt: want ErrLeaseExpired, got %v", err)
		}
		// The ADR evaluates LOST before token mismatch, so a wrong token on a LOST attempt is also LEASE_EXPIRED.
		if _, err := h.Repo.RenewLease(ctx, "j_x", att1.ID, "wrong", "w_x"); !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("wrong token on a LOST attempt: want ErrLeaseExpired (ADR-0014 evaluation order), got %v", err)
		}
		assertNotResurrected(t, h, att1.ID, lease1)
		if got := requireLease(t, conformanceAttempt(t, h.Repo, att2.ID)); !timesClose(got, lease2) {
			t.Fatalf("rejected renewal of attempt 1 touched attempt 2's lease: %v -> %v", lease2, got)
		}
		// The live attempt keeps renewing.
		if _, err := h.Repo.RenewLease(ctx, "j_x", att2.ID, att2.LeaseToken, "w_x"); err != nil {
			t.Fatalf("renewal of the current attempt failed: %v", err)
		}
	})

	t.Run("lost_attempt_forced_on_current_attempt", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_x", 1)
		att := conformanceRunningJob(t, h.Repo, "j_x", "w_x")
		h.ForceAttemptState(t, att.ID, domain.AttemptLost)
		if _, err := h.Repo.RenewLease(ctx, "j_x", att.ID, att.LeaseToken, "w_x"); !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("renewal of a LOST current attempt: want ErrLeaseExpired, got %v", err)
		}
	})
}

// RunSweepRetryOrFailConformance verifies how SweepExpiredLeases resolves each kind of expired
// attempt (ADR-0014 sweeper state resolution rules, plan R-02). Wrapped by
// TestConformance_SweepExpiredLeases_RetryOrFail.
func RunSweepRetryOrFailConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	setup := func(t *testing.T, maxAttempts int) (Harness, *domain.Attempt) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_s", 1)
		conformanceJobN(t, h.Repo, "j_s", maxAttempts)
		att, err := h.Repo.DispatchJob(ctx, "j_s", "w_s")
		if err != nil {
			t.Fatalf("DispatchJob failed: %v", err)
		}
		return h, att
	}
	requireDetail := func(t *testing.T, ev *domain.JobEvent, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if _, ok := ev.Details[k]; !ok {
				t.Fatalf("event %s details %v lack %q", ev.EventType, ev.Details, k)
			}
		}
	}

	t.Run("retry_when_attempts_remain", func(t *testing.T) {
		h, att := setup(t, 3)
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		var asked []int
		backoff := func(n int) time.Duration { asked = append(asked, n); return 7 * time.Second }
		lo := h.Now(t)
		res := conformanceSweep(t, h.Repo, domain.SweepParams{Backoff: backoff})
		hi := h.Now(t)

		if res.Candidates != 1 || len(res.Swept) != 1 || res.SkippedLocked != 0 || res.SuppressedByGrace != 0 || res.SkippedChanged != 0 {
			t.Fatalf("sweep result = %+v", res)
		}
		out := res.Swept[0]
		if out.JobID != "j_s" || out.AttemptID != att.ID || out.AttemptState != domain.AttemptLost || out.JobState != domain.StateRetrying {
			t.Fatalf("outcome = %+v", out)
		}
		if len(asked) != 1 || asked[0] != 1 {
			t.Fatalf("backoff asked for attempts %v, want [1] (the attempt that was lost)", asked)
		}
		a := conformanceAttempt(t, h.Repo, att.ID)
		if a.State != domain.AttemptLost || a.ErrorClass != domain.ClassLeaseExpired || a.FinishedAt == nil || a.Error != "lease expired: worker failed to renew" {
			t.Fatalf("lost attempt = %+v", a)
		}
		job := conformanceJob(t, h.Repo, "j_s")
		if job.State != domain.StateRetrying || job.NotBefore == nil || job.Result != nil {
			t.Fatalf("job = %+v", job)
		}
		if job.NotBefore.Before(lo.Add(7*time.Second).Add(-leaseTolerance)) || job.NotBefore.After(hi.Add(7*time.Second).Add(leaseTolerance)) {
			t.Fatalf("not_before %v not within [%v, %v] (sweep time + 7s)", *job.NotBefore, lo.Add(7*time.Second), hi.Add(7*time.Second))
		}
		if got := conformanceSlots(t, h.Repo, "w_s"); got != 1 {
			t.Fatalf("worker slots = %d, want 1 (slot freed)", got)
		}
		events := conformanceEvents(t, h.Repo, "j_s")
		requireEventTail(t, events, domain.JobEventLeaseExpired, domain.JobEventRetrying)
		lease, retry := events[len(events)-2], events[len(events)-1]
		requireDetail(t, lease, "attempt_id", "worker_id")
		requireDetail(t, retry, "attempt_no", "not_before", "backoff_seconds")
		if retry.FromState == nil || *retry.FromState != domain.StateRunning || retry.ToState == nil || *retry.ToState != domain.StateRetrying {
			t.Fatalf("JOB_RETRYING states = %v -> %v", retry.FromState, retry.ToState)
		}
		if lease.AttemptID == nil || *lease.AttemptID != att.ID || retry.AttemptID == nil || *retry.AttemptID != att.ID {
			t.Fatalf("events must carry the attempt id")
		}

		// Not eligible for dispatch before not_before.
		if _, _, err := h.Repo.DispatchNextJob(ctx, "w_s"); !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("dispatch before not_before: want ErrNoQueuedJobs, got %v", err)
		}
		// A second sweep finds nothing: the attempt is closed.
		if again := conformanceSweep(t, h.Repo, domain.SweepParams{}); again.Candidates != 0 || len(again.Swept) != 0 {
			t.Fatalf("second sweep = %+v", again)
		}
	})

	t.Run("redispatch_once_not_before_reached_uses_attempt_number_2", func(t *testing.T) {
		h, att := setup(t, 3)
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		conformanceSweep(t, h.Repo, domain.SweepParams{Backoff: conformanceBackoff(0)})
		att2 := conformanceDispatchNext(t, h.Repo, "w_s")
		if att2.AttemptNo != 2 || att2.ID == att.ID {
			t.Fatalf("redispatched attempt = %+v", att2)
		}
		if job := conformanceJob(t, h.Repo, "j_s"); job.State != domain.StateRunning || job.NotBefore != nil {
			t.Fatalf("job after redispatch = %+v", job)
		}
		if conformanceAttemptState(t, h.Repo, att.ID) != domain.AttemptLost {
			t.Fatalf("attempt 1 must stay LOST")
		}
	})

	t.Run("fail_when_attempts_exhausted", func(t *testing.T) {
		h, att := setup(t, 1)
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		asked := 0
		res := conformanceSweep(t, h.Repo, domain.SweepParams{Backoff: func(int) time.Duration { asked++; return time.Second }})
		if len(res.Swept) != 1 || res.Swept[0].JobState != domain.StateFailed || res.Swept[0].AttemptState != domain.AttemptLost {
			t.Fatalf("sweep result = %+v", res)
		}
		if asked != 0 {
			t.Fatalf("backoff consulted %d times for an exhausted job", asked)
		}
		a := conformanceAttempt(t, h.Repo, att.ID)
		if a.State != domain.AttemptLost || a.ErrorClass != domain.ClassLeaseExpired {
			t.Fatalf("attempt = %+v", a)
		}
		job := conformanceJob(t, h.Repo, "j_s")
		if job.State != domain.StateFailed || job.NotBefore != nil || job.Result != nil {
			t.Fatalf("job = %+v (a swept job has no worker-committed result)", job)
		}
		if got := conformanceSlots(t, h.Repo, "w_s"); got != 1 {
			t.Fatalf("worker slots = %d, want 1", got)
		}
		events := conformanceEvents(t, h.Repo, "j_s")
		requireEventTail(t, events, domain.JobEventLeaseExpired, domain.JobEventFailed)
		failed := events[len(events)-1]
		if failed.Details["error"] != "max attempts exhausted" || failed.Details["error_class"] != string(domain.ClassLeaseExpired) {
			t.Fatalf("JOB_FAILED details = %v", failed.Details)
		}
		if countEvents(events, domain.JobEventRetrying) != 0 {
			t.Fatalf("exhausted job recorded JOB_RETRYING")
		}
	})

	t.Run("lost_attempts_consume_max_attempts", func(t *testing.T) {
		h, att := setup(t, 3)
		for n := 1; n <= 3; n++ {
			h.SetLeaseExpiry(t, att.ID, -time.Second)
			res := conformanceSweep(t, h.Repo, domain.SweepParams{Backoff: conformanceBackoff(0)})
			want := domain.StateRetrying
			if n == 3 {
				want = domain.StateFailed
			}
			if len(res.Swept) != 1 || res.Swept[0].JobState != want {
				t.Fatalf("sweep %d result = %+v, want job %s", n, res, want)
			}
			if n < 3 {
				att = conformanceDispatchNext(t, h.Repo, "w_s")
				if att.AttemptNo != n+1 {
					t.Fatalf("attempt number after sweep %d = %d", n, att.AttemptNo)
				}
			}
		}
		if job := conformanceJob(t, h.Repo, "j_s"); job.State != domain.StateFailed {
			t.Fatalf("job = %s after 3 lost attempts, want FAILED", job.State)
		}
		if _, _, err := h.Repo.DispatchNextJob(ctx, "w_s"); !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("a FAILED job must not be dispatchable: %v", err)
		}
	})

	t.Run("cancel_requested_records_only_job_cancelled", func(t *testing.T) {
		h, att := setup(t, 3)
		if _, err := h.Repo.CancelJob(ctx, "j_s"); err != nil {
			t.Fatalf("CancelJob failed: %v", err)
		}
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		asked := 0
		res := conformanceSweep(t, h.Repo, domain.SweepParams{Backoff: func(int) time.Duration { asked++; return 0 }})
		if len(res.Swept) != 1 || res.Swept[0].AttemptState != domain.AttemptCancelled || res.Swept[0].JobState != domain.StateCancelled {
			t.Fatalf("sweep result = %+v", res)
		}
		if asked != 0 {
			t.Fatalf("a cancelled job consulted the retry backoff")
		}
		a := conformanceAttempt(t, h.Repo, att.ID)
		if a.State != domain.AttemptCancelled || a.FinishedAt == nil {
			t.Fatalf("attempt = %+v", a)
		}
		job := conformanceJob(t, h.Repo, "j_s")
		if job.State != domain.StateCancelled || job.NotBefore != nil {
			t.Fatalf("job = %+v (cancel wins, never RETRYING, even with attempts left)", job)
		}
		events := conformanceEvents(t, h.Repo, "j_s")
		requireEventTail(t, events, domain.JobEventCancelRequested, domain.JobEventCancelled)
		if countEvents(events, domain.JobEventLeaseExpired) != 0 || countEvents(events, domain.JobEventRetrying) != 0 || countEvents(events, domain.JobEventCancelled) != 1 {
			t.Fatalf("a CANCEL_REQUESTED sweep must record only JOB_CANCELLED, events = %v", eventTypes(events))
		}
		ev := events[len(events)-1]
		if ev.Details["resolved_by"] != "lease_sweeper" || ev.FromState == nil || *ev.FromState != domain.StateCancelRequested || ev.ToState == nil || *ev.ToState != domain.StateCancelled {
			t.Fatalf("JOB_CANCELLED = %+v details %v", ev, ev.Details)
		}
		if got := conformanceSlots(t, h.Repo, "w_s"); got != 1 {
			t.Fatalf("worker slots = %d, want 1", got)
		}
	})

	t.Run("terminal_job_closes_attempt_without_touching_the_job", func(t *testing.T) {
		cases := []struct {
			jobState domain.JobState
			want     domain.AttemptState
		}{
			{domain.StateCancelled, domain.AttemptCancelled},
			{domain.StateSucceeded, domain.AttemptLost},
			{domain.StateFailed, domain.AttemptLost},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(string(tc.jobState), func(t *testing.T) {
				h, att := setup(t, 3)
				h.ForceJobState(t, "j_s", tc.jobState)
				jobBefore := conformanceJob(t, h.Repo, "j_s")
				eventsBefore := len(conformanceEvents(t, h.Repo, "j_s"))
				h.SetLeaseExpiry(t, att.ID, -time.Second)
				res := conformanceSweep(t, h.Repo, domain.SweepParams{})
				if len(res.Swept) != 1 || res.Swept[0].AttemptState != tc.want || res.Swept[0].JobState != tc.jobState {
					t.Fatalf("sweep result = %+v", res)
				}
				a := conformanceAttempt(t, h.Repo, att.ID)
				if a.State != tc.want || a.FinishedAt == nil {
					t.Fatalf("attempt = %+v, want %s", a, tc.want)
				}
				if tc.want == domain.AttemptLost && a.ErrorClass != domain.ClassLeaseExpired {
					t.Fatalf("LOST attempt error_class = %q", a.ErrorClass)
				}
				if tc.want == domain.AttemptCancelled && a.ErrorClass != "" {
					t.Fatalf("CANCELLED attempt error_class = %q, want none", a.ErrorClass)
				}
				jobAfter := conformanceJob(t, h.Repo, "j_s")
				if jobAfter.State != jobBefore.State || !jobAfter.UpdatedAt.Equal(jobBefore.UpdatedAt) || jobAfter.NotBefore != nil {
					t.Fatalf("terminal job mutated: before=%+v after=%+v", jobBefore, jobAfter)
				}
				events := conformanceEvents(t, h.Repo, "j_s")
				if len(events) != eventsBefore+1 || events[len(events)-1].EventType != domain.JobEventLeaseExpired {
					t.Fatalf("events = %v, want exactly one new LEASE_EXPIRED", eventTypes(events))
				}
				if got := conformanceSlots(t, h.Repo, "w_s"); got != 1 {
					t.Fatalf("worker slots = %d, want 1", got)
				}
			})
		}
	})

	t.Run("candidates_are_only_expired_running_attempts", func(t *testing.T) {
		h, att := setup(t, 3)
		h.SetLeaseExpiry(t, att.ID, time.Minute)
		if res := conformanceSweep(t, h.Repo, domain.SweepParams{}); res.Candidates != 0 {
			t.Fatalf("an unexpired attempt was a candidate: %+v", res)
		}
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		h.ForceAttemptState(t, att.ID, domain.AttemptCompleted)
		if res := conformanceSweep(t, h.Repo, domain.SweepParams{}); res.Candidates != 0 {
			t.Fatalf("a COMPLETED attempt was a candidate: %+v", res)
		}
	})

	t.Run("limit_bounds_the_batch_oldest_lease_first", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_s", 3)
		atts := make([]*domain.Attempt, 3)
		for i := range atts {
			atts[i] = conformanceRunningJob(t, h.Repo, fmt.Sprintf("j_b%d", i), "w_s")
		}
		// Lease ages: j_b2 oldest, then j_b0, then j_b1.
		h.SetLeaseExpiry(t, atts[2].ID, -30*time.Second)
		h.SetLeaseExpiry(t, atts[0].ID, -20*time.Second)
		h.SetLeaseExpiry(t, atts[1].ID, -10*time.Second)
		res := conformanceSweep(t, h.Repo, domain.SweepParams{Limit: 2})
		if res.Candidates != 2 || len(res.Swept) != 2 || res.Swept[0].AttemptID != atts[2].ID || res.Swept[1].AttemptID != atts[0].ID {
			t.Fatalf("limited sweep = %+v, want the two oldest leases (%s, %s)", res, atts[2].ID, atts[0].ID)
		}
		if conformanceAttemptState(t, h.Repo, atts[1].ID) != domain.AttemptRunning {
			t.Fatalf("the third attempt was swept beyond the limit")
		}
		if res := conformanceSweep(t, h.Repo, domain.SweepParams{Limit: 2}); len(res.Swept) != 1 || res.Swept[0].AttemptID != atts[1].ID {
			t.Fatalf("second limited sweep = %+v", res)
		}
	})

	t.Run("nil_backoff_is_rejected_and_nothing_changes", func(t *testing.T) {
		h, att := setup(t, 3)
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		_, err := h.Repo.SweepExpiredLeases(ctx, domain.SweepParams{})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("nil Backoff: want ErrInvalidRequest, got %v", err)
		}
		if conformanceAttemptState(t, h.Repo, att.ID) != domain.AttemptRunning {
			t.Fatalf("a rejected sweep changed the attempt")
		}
	})
}

// RunSweepGraceInputConformance verifies the in-lock grace check driven by the caller's grace
// state (ADR-0014): while the state suppresses reaping, nothing is mutated and nothing is
// extended; the cooldown lifts an outage window but not the startup grace. Wrapped by
// TestConformance_SweepExpiredLeases_GraceInputSuppressesReaping.
func RunSweepGraceInputConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	for _, kind := range []string{"running", "cancel_requested", "terminal_job"} {
		kind := kind
		t.Run("job_"+kind, func(t *testing.T) {
			h := newHarness(t)
			requireLeaseHooks(t, h)
			conformanceWorker(t, h.Repo, "w_g", 1)
			att := conformanceRunningJob(t, h.Repo, "j_g", "w_g")
			switch kind {
			case "cancel_requested":
				if _, err := h.Repo.CancelJob(ctx, "j_g"); err != nil {
					t.Fatalf("CancelJob failed: %v", err)
				}
			case "terminal_job":
				h.ForceJobState(t, "j_g", domain.StateCancelled)
			}
			h.SetLeaseExpiry(t, att.ID, -time.Second)
			jobBefore := conformanceJob(t, h.Repo, "j_g")
			attBefore := conformanceAttempt(t, h.Repo, att.ID)
			eventsBefore := len(conformanceEvents(t, h.Repo, "j_g"))
			slotsBefore := conformanceSlots(t, h.Repo, "w_g")

			future := h.Now(t).Add(time.Hour)
			past := h.Now(t).Add(-time.Hour)
			suppressing := map[string]domain.SweepGraceState{
				"startup_grace":                 {StartupGraceUntil: future},
				"outage_window":                 {OutageWindowUntil: future},
				"cooldown_does_not_end_startup": {StartupGraceUntil: future, CooldownUntil: future},
			}
			for name, grace := range suppressing {
				res := conformanceSweep(t, h.Repo, domain.SweepParams{Grace: grace})
				if res.Candidates != 1 || len(res.Swept) != 0 || res.SuppressedByGrace != 1 {
					t.Fatalf("%s: sweep result = %+v, want the candidate suppressed", name, res)
				}
				jobAfter, attAfter := conformanceJob(t, h.Repo, "j_g"), conformanceAttempt(t, h.Repo, att.ID)
				if jobAfter.State != jobBefore.State || !jobAfter.UpdatedAt.Equal(jobBefore.UpdatedAt) {
					t.Fatalf("%s: job mutated %+v -> %+v", name, jobBefore, jobAfter)
				}
				if attAfter.State != attBefore.State || !timesClose(requireLease(t, attAfter), requireLease(t, attBefore)) || attAfter.FinishedAt != nil {
					t.Fatalf("%s: attempt mutated %+v -> %+v", name, attBefore, attAfter)
				}
				if n := len(conformanceEvents(t, h.Repo, "j_g")); n != eventsBefore {
					t.Fatalf("%s: %d event(s) recorded while suppressed", name, n-eventsBefore)
				}
				if got := conformanceSlots(t, h.Repo, "w_g"); got != slotsBefore {
					t.Fatalf("%s: worker slots changed %d -> %d", name, slotsBefore, got)
				}
			}

			// Grace that has ended, a cooldown over an outage window, and the zero value all let reaping proceed.
			proceeding := domain.SweepGraceState{StartupGraceUntil: past, OutageWindowUntil: past}
			if kind == "running" {
				proceeding = domain.SweepGraceState{OutageWindowUntil: future, CooldownUntil: future}
			}
			res := conformanceSweep(t, h.Repo, domain.SweepParams{Grace: proceeding})
			if len(res.Swept) != 1 || res.SuppressedByGrace != 0 {
				t.Fatalf("sweep with non-suppressing grace %+v = %+v, want the attempt swept", proceeding, res)
			}
			if conformanceAttemptState(t, h.Repo, att.ID) == domain.AttemptRunning {
				t.Fatalf("attempt still RUNNING after an unsuppressed sweep")
			}
		})
	}
}

// RunCommitAttemptConflictOrderConformance verifies the CommitAttempt 409 evaluation order of
// ADR-0014 / protocol 6.3: terminal job, then swept (LOST) attempt, then superseded / not RUNNING /
// wrong token. A late commit on a LOST attempt records one bounded DIAGNOSTIC_LATE_RESULT and is
// never stored. Wrapped by TestConformance_CommitAttempt_ConflictEvaluationOrder.
func RunCommitAttemptConflictOrderConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	commit := func(h Harness, jobID string, att *domain.Attempt, token string) error {
		_, err := h.Repo.CommitAttempt(ctx, jobID, att.ID, token, "w_c", domain.AttemptOutcome{ExitCode: 0, Output: "late"})
		return err
	}
	diagnostics := func(t *testing.T, h Harness, jobID string) []*domain.JobEvent {
		var out []*domain.JobEvent
		for _, e := range conformanceEvents(t, h.Repo, jobID) {
			if e.EventType == domain.JobEventDiagnosticLateResult {
				out = append(out, e)
			}
		}
		return out
	}

	t.Run("swept_attempt_is_lease_expired_with_one_bounded_diagnostic", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_c", 1)
		att := conformanceRunningJob(t, h.Repo, "j_c", "w_c")
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		conformanceSweep(t, h.Repo, domain.SweepParams{Backoff: conformanceBackoff(time.Hour)})
		jobBefore := conformanceJob(t, h.Repo, "j_c")
		attBefore := conformanceAttempt(t, h.Repo, att.ID)

		for i := 0; i < 5; i++ {
			if err := commit(h, "j_c", att, att.LeaseToken); !errors.Is(err, domain.ErrLeaseExpired) {
				t.Fatalf("commit %d on a LOST attempt: want ErrLeaseExpired, got %v", i, err)
			}
		}
		if d := diagnostics(t, h, "j_c"); len(d) != 1 {
			t.Fatalf("%d DIAGNOSTIC_LATE_RESULT events after 5 late commits, want exactly 1", len(d))
		} else if d[0].AttemptID == nil || *d[0].AttemptID != att.ID || d[0].Details["attempt_id"] != att.ID || d[0].Details["rejected"] != true {
			t.Fatalf("diagnostic event = %+v details %v", d[0], d[0].Details)
		}
		jobAfter, attAfter := conformanceJob(t, h.Repo, "j_c"), conformanceAttempt(t, h.Repo, att.ID)
		if jobAfter.State != jobBefore.State || jobAfter.Result != nil || !jobAfter.UpdatedAt.Equal(jobBefore.UpdatedAt) {
			t.Fatalf("late commit changed the job: %+v -> %+v", jobBefore, jobAfter)
		}
		if attAfter.State != domain.AttemptLost || attAfter.Output != attBefore.Output || (attAfter.FinishedAt == nil) != (attBefore.FinishedAt == nil) {
			t.Fatalf("late commit changed the LOST attempt: %+v -> %+v", attBefore, attAfter)
		}
	})

	t.Run("lost_is_evaluated_before_token_mismatch_and_supersession", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_c", 1)
		att1 := conformanceRunningJob(t, h.Repo, "j_c", "w_c")
		h.SetLeaseExpiry(t, att1.ID, -time.Second)
		conformanceSweep(t, h.Repo, domain.SweepParams{Backoff: conformanceBackoff(0)})
		att2 := conformanceDispatchNext(t, h.Repo, "w_c")

		// FS-04: the old worker commits after a new attempt started.
		if err := commit(h, "j_c", att1, att1.LeaseToken); !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("old attempt, right token: want ErrLeaseExpired, got %v", err)
		}
		if err := commit(h, "j_c", att1, "wrong-token"); !errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("old attempt, wrong token: want ErrLeaseExpired (LOST precedes token mismatch), got %v", err)
		}
		if d := diagnostics(t, h, "j_c"); len(d) != 1 {
			t.Fatalf("%d diagnostics, want 1 per attempt", len(d))
		}
		// The live attempt is untouched and a wrong token on it is STALE, with no diagnostic.
		if err := commit(h, "j_c", att2, "wrong-token"); !errors.Is(err, domain.ErrStaleAttempt) || errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("wrong token on the live attempt: want ErrStaleAttempt, got %v", err)
		}
		if d := diagnostics(t, h, "j_c"); len(d) != 1 {
			t.Fatalf("a stale-token rejection recorded a diagnostic")
		}
		if job := conformanceJob(t, h.Repo, "j_c"); job.State != domain.StateRunning || conformanceAttemptState(t, h.Repo, att2.ID) != domain.AttemptRunning {
			t.Fatalf("live attempt disturbed: job %s", job.State)
		}
		if err := commit(h, "j_c", att2, att2.LeaseToken); err != nil {
			t.Fatalf("the live attempt could not commit: %v", err)
		}
	})

	t.Run("terminal_job_beats_lost_attempt", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_c", 1)
		conformanceJobN(t, h.Repo, "j_c", 1)
		att, err := h.Repo.DispatchJob(ctx, "j_c", "w_c")
		if err != nil {
			t.Fatal(err)
		}
		h.SetLeaseExpiry(t, att.ID, -time.Second)
		conformanceSweep(t, h.Repo, domain.SweepParams{}) // max_attempts 1: job FAILED, attempt LOST
		if job := conformanceJob(t, h.Repo, "j_c"); job.State != domain.StateFailed {
			t.Fatalf("setup: job = %s", job.State)
		}
		for i := 0; i < 3; i++ {
			if err := commit(h, "j_c", att, att.LeaseToken); !errors.Is(err, domain.ErrJobAlreadyTerminal) || errors.Is(err, domain.ErrLeaseExpired) {
				t.Fatalf("commit on a terminal job with a LOST attempt: want ErrJobAlreadyTerminal, got %v", err)
			}
		}
		if got := conformanceAttemptState(t, h.Repo, att.ID); got != domain.AttemptLost {
			t.Fatalf("attempt = %s, a closed attempt must never be overwritten", got)
		}
		if d := diagnostics(t, h, "j_c"); len(d) != 1 {
			t.Fatalf("%d diagnostics, want 1", len(d))
		}
	})

	t.Run("non_lost_non_running_and_unknown_attempts_are_stale_without_diagnostic", func(t *testing.T) {
		for _, state := range []domain.AttemptState{domain.AttemptCompleted, domain.AttemptFailed, domain.AttemptCancelled, domain.AttemptTimedOut} {
			h := newHarness(t)
			conformanceWorker(t, h.Repo, "w_c", 1)
			att := conformanceRunningJob(t, h.Repo, "j_c", "w_c")
			h.ForceAttemptState(t, att.ID, state)
			if err := commit(h, "j_c", att, att.LeaseToken); !errors.Is(err, domain.ErrStaleAttempt) {
				t.Fatalf("%s attempt: want ErrStaleAttempt, got %v", state, err)
			}
			if d := diagnostics(t, h, "j_c"); len(d) != 0 {
				t.Fatalf("%s attempt: stale rejection recorded %d diagnostic(s)", state, len(d))
			}
		}
		h := newHarness(t)
		conformanceWorker(t, h.Repo, "w_c", 1)
		att := conformanceRunningJob(t, h.Repo, "j_c", "w_c")
		ghost := &domain.Attempt{ID: "no_such_attempt"}
		if err := commit(h, "j_c", ghost, att.LeaseToken); !errors.Is(err, domain.ErrStaleAttempt) {
			t.Fatalf("unknown attempt: want ErrStaleAttempt, got %v", err)
		}
		if d := diagnostics(t, h, "j_c"); len(d) != 0 {
			t.Fatalf("unknown attempt recorded %d diagnostic(s)", len(d))
		}
	})

	t.Run("lost_attempt_of_another_job_is_not_disclosed_or_recorded", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_c", 2)
		lost := conformanceRunningJob(t, h.Repo, "j_lost", "w_c")
		h.SetLeaseExpiry(t, lost.ID, -time.Second)
		conformanceSweep(t, h.Repo, domain.SweepParams{})
		conformanceRunningJob(t, h.Repo, "j_live", "w_c")
		if err := commit(h, "j_live", lost, lost.LeaseToken); !errors.Is(err, domain.ErrStaleAttempt) || errors.Is(err, domain.ErrLeaseExpired) {
			t.Fatalf("another job's LOST attempt named on j_live: want ErrStaleAttempt, got %v", err)
		}
		if d := diagnostics(t, h, "j_live"); len(d) != 0 {
			t.Fatalf("a foreign attempt recorded %d diagnostic(s) on j_live", len(d))
		}
	})

	t.Run("expired_but_unswept_attempt_still_commits", func(t *testing.T) {
		h := newHarness(t)
		requireLeaseHooks(t, h)
		conformanceWorker(t, h.Repo, "w_c", 1)
		att := conformanceRunningJob(t, h.Repo, "j_c", "w_c")
		h.SetLeaseExpiry(t, att.ID, -time.Minute)
		if err := commit(h, "j_c", att, att.LeaseToken); err != nil {
			t.Fatalf("commit of an expired but unswept attempt rejected: %v", err)
		}
		if job := conformanceJob(t, h.Repo, "j_c"); job.State != domain.StateSucceeded {
			t.Fatalf("job = %s", job.State)
		}
	})
}
