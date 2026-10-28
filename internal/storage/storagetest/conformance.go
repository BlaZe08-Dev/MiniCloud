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
// attempt, so COMPLETED, FAILED and LOST are all reached through the harness's ForceAttemptState
// hook. Wrapped by TestConformance_CommitAttempt_NonRunningAttempt_ReturnsStaleAttempt.
func RunCommitAttemptNonRunningAttemptConformance(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	for _, state := range []domain.AttemptState{domain.AttemptCompleted, domain.AttemptFailed, domain.AttemptLost} {
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
