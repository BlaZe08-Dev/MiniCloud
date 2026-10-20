package coordinator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

var idCounter int64

// generateID creates a unique, readable identifier with the given prefix.
func generateID(prefix string) string {
	seq := atomic.AddInt64(&idCounter, 1)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%d_%s", prefix, seq, hex.EncodeToString(b))
}

// Clock provides time abstraction for deterministic testing.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}

// WorkAssignment represents the payload delivered to a worker upon claiming a job attempt.
type WorkAssignment struct {
	JobID          string          `json:"job_id"`
	AttemptID      string          `json:"attempt_id"`
	AttemptNo      int             `json:"attempt_no"`
	LeaseToken     string          `json:"lease_token"`
	TaskType       string          `json:"task_type"`
	Payload        json.RawMessage `json:"payload"`
	TimeoutSeconds int             `json:"timeout_seconds"`
}

// Service orchestrates jobs, worker coordination, scheduling, and attempt lifecycle.
type Service struct {
	mu            sync.Mutex
	repo          storage.Repository
	scheduler     scheduler.Scheduler
	executorReg   *executor.Registry
	clock         Clock
	workerTimeout time.Duration
}

// NewService constructs a new Coordinator Service.
func NewService(repo storage.Repository, sched scheduler.Scheduler, reg *executor.Registry, workerTimeout time.Duration) *Service {
	if workerTimeout <= 0 {
		workerTimeout = 10 * time.Second
	}
	return &Service{
		repo:          repo,
		scheduler:     sched,
		executorReg:   reg,
		clock:         realClock{},
		workerTimeout: workerTimeout,
	}
}

// SetClock injects a custom clock (e.g. for deterministic unit tests).
func (s *Service) SetClock(c Clock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = c
}

// SubmitJob validates a new job submission, records it in SUBMITTED, and transitions to QUEUED.
func (s *Service) SubmitJob(ctx context.Context, taskType string, payload []byte, timeoutSec, maxAttempts int) (*domain.Job, error) {
	if len(payload) > executor.MaxPayloadSizeBytes {
		return nil, domain.ErrPayloadTooLarge
	}

	task, err := s.executorReg.Get(taskType)
	if err != nil {
		return nil, err
	}

	if err := task.Validate(payload); err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrInvalidRequest, err)
	}

	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if timeoutSec <= 0 {
		timeoutSec = 60
	}

	now := s.clock.Now()
	job := &domain.Job{
		ID:             generateID("job"),
		TaskType:       taskType,
		Payload:        payload,
		State:          domain.StateSubmitted,
		MaxAttempts:    maxAttempts,
		TimeoutSeconds: timeoutSec,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	// Validate and apply transition SUBMITTED -> QUEUED
	if err := job.TransitionTo(domain.StateQueued); err != nil {
		return nil, err
	}

	if err := s.repo.CreateJob(ctx, job); err != nil {
		return nil, err
	}

	slog.Info("job submitted and queued",
		"job_id", job.ID,
		"task_type", job.TaskType,
		"state", job.State,
		"max_attempts", job.MaxAttempts,
	)

	return job, nil
}

// GetJob retrieves a job and its latest status.
func (s *Service) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	return s.repo.GetJob(ctx, id)
}

// ListAttempts retrieves all attempt history for a job.
func (s *Service) ListAttempts(ctx context.Context, jobID string) ([]*domain.Attempt, error) {
	// Verify job exists
	if _, err := s.repo.GetJob(ctx, jobID); err != nil {
		return nil, err
	}
	return s.repo.ListAttemptsByJob(ctx, jobID)
}

// CancelJob cancels a queued or running job.
func (s *Service) CancelJob(ctx context.Context, id string) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, err := s.repo.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}

	if job.State.IsTerminal() {
		return nil, domain.ErrJobAlreadyTerminal
	}

	var targetState domain.JobState
	if job.State == domain.StateQueued {
		targetState = domain.StateCancelled
	} else if job.State == domain.StateRunning {
		targetState = domain.StateCancelRequested
	} else {
		targetState = domain.StateCancelled
	}

	if err := job.TransitionTo(targetState); err != nil {
		return nil, err
	}

	if err := s.repo.UpdateJob(ctx, job); err != nil {
		return nil, err
	}

	slog.Info("job cancellation requested",
		"job_id", job.ID,
		"state", job.State,
	)

	return job, nil
}

// RegisterWorker registers or updates a worker process.
func (s *Service) RegisterWorker(ctx context.Context, worker *domain.Worker) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if worker.ID == "" {
		return fmt.Errorf("%w: worker_id cannot be empty", domain.ErrInvalidRequest)
	}
	if worker.TotalSlots <= 0 {
		return fmt.Errorf("%w: total_slots must be positive", domain.ErrInvalidRequest)
	}
	if worker.AvailableSlots <= 0 || worker.AvailableSlots > worker.TotalSlots {
		worker.AvailableSlots = worker.TotalSlots
	}
	worker.Status = domain.WorkerStatusHealthy
	now := s.clock.Now()
	worker.LastHeartbeatAt = now
	worker.RegisteredAt = now

	if err := s.repo.UpsertWorker(ctx, worker); err != nil {
		return err
	}

	slog.Info("worker registered",
		"worker_id", worker.ID,
		"version", worker.Version,
		"slots", worker.TotalSlots,
		"supported_tasks", worker.SupportedTasks,
	)

	return nil
}

// Heartbeat records a worker heartbeat and updates its available capacity.
// A valid heartbeat from an UNHEALTHY worker restores its status to HEALTHY.
func (s *Service) Heartbeat(ctx context.Context, workerID string, availableSlots int) error {
	return s.repo.RecordHeartbeat(ctx, workerID, s.clock.Now(), availableSlots)
}

// ListWorkers returns all registered workers.
func (s *Service) ListWorkers(ctx context.Context) ([]*domain.Worker, error) {
	return s.repo.ListWorkers(ctx)
}

// ReapInactiveWorkers scans registered workers and marks any worker UNHEALTHY
// whose last heartbeat is older than workerTimeout.
func (s *Service) ReapInactiveWorkers(ctx context.Context) ([]string, error) {
	now := s.clock.Now()
	cutoff := now.Add(-s.workerTimeout)
	reaped, err := s.repo.ReapStaleWorkers(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	if len(reaped) > 0 {
		slog.Warn("reaped inactive workers due to heartbeat timeout",
			"count", len(reaped),
			"worker_ids", reaped,
			"cutoff", cutoff,
		)
	}
	return reaped, nil
}

// StartReaper runs a periodic reaper loop until the context is cancelled.
func (s *Service) StartReaper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.ReapInactiveWorkers(ctx)
		}
	}
}

// PollWork allows an idle worker with capacity to claim an eligible queued job.
// A poll from an UNHEALTHY worker does not count as liveness and returns no work.
func (s *Service) PollWork(ctx context.Context, workerID string) (*WorkAssignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	worker, err := s.repo.GetWorker(ctx, workerID)
	if err != nil {
		return nil, err
	}

	// Poll from an UNHEALTHY worker returns no work
	if worker.Status != domain.WorkerStatusHealthy {
		return nil, domain.ErrNoWorkersAvailable
	}

	if !worker.HasCapacity() {
		return nil, domain.ErrNoWorkersAvailable
	}

	queuedJobs, err := s.repo.ListQueuedJobs(ctx)
	if err != nil {
		return nil, err
	}
	if len(queuedJobs) == 0 {
		return nil, domain.ErrNoQueuedJobs
	}

	// Find the first queued job that this worker supports
	var selectedJob *domain.Job
	for _, j := range queuedJobs {
		if worker.SupportsTask(j.TaskType) {
			selectedJob = j
			break
		}
	}

	if selectedJob == nil {
		return nil, domain.ErrNoQueuedJobs
	}

	// Fetch existing attempts for this job to determine attempt_no
	prevAttempts, err := s.repo.ListAttemptsByJob(ctx, selectedJob.ID)
	if err != nil {
		return nil, err
	}
	attemptNo := len(prevAttempts) + 1

	now := s.clock.Now()
	attempt := &domain.Attempt{
		ID:         generateID("att"),
		JobID:      selectedJob.ID,
		AttemptNo:  attemptNo,
		WorkerID:   worker.ID,
		LeaseToken: generateID("lease"),
		State:      domain.AttemptRunning,
		StartedAt:  now,
	}

	// Transition job to RUNNING
	if err := selectedJob.TransitionTo(domain.StateRunning); err != nil {
		return nil, err
	}
	selectedJob.CurrentAttemptID = attempt.ID

	if err := s.repo.CreateAttempt(ctx, attempt); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateJob(ctx, selectedJob); err != nil {
		return nil, err
	}

	// Deduct 1 available slot from worker
	worker.AvailableSlots--
	_ = s.repo.UpsertWorker(ctx, worker)

	slog.Info("job dispatched to worker",
		"job_id", selectedJob.ID,
		"attempt_id", attempt.ID,
		"attempt_no", attempt.AttemptNo,
		"worker_id", worker.ID,
	)

	return &WorkAssignment{
		JobID:          selectedJob.ID,
		AttemptID:      attempt.ID,
		AttemptNo:      attempt.AttemptNo,
		LeaseToken:     attempt.LeaseToken,
		TaskType:       selectedJob.TaskType,
		Payload:        selectedJob.Payload,
		TimeoutSeconds: selectedJob.TimeoutSeconds,
	}, nil
}

// CommitResult commits an execution outcome reported by a worker.
//
// CRITICAL INVARIANTS:
// 1. Rejects commit if job is already terminal.
// 2. Rejects stale attempts if attempt_id or lease_token does not match active attempt.
// 3. Rejects commit if reporting worker is not the worker assigned to the attempt (WORKER_MISMATCH).
// 4. Accepts commit from a worker marked UNHEALTHY if attempt_id, lease_token, and worker_id match.
func (s *Service) CommitResult(ctx context.Context, jobID, attemptID, leaseToken, workerID string, exitCode int, output, errSummary string) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, err := s.repo.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}

	// Check 1: If job is already in a terminal state, reject commit
	if job.State.IsTerminal() {
		slog.Warn("rejecting result for already-terminal job",
			"job_id", job.ID,
			"attempt_id", attemptID,
			"job_state", job.State,
		)
		return nil, domain.ErrJobAlreadyTerminal
	}

	// Check 2: Verify that reported attempt_id is indeed the job's active current attempt
	if job.CurrentAttemptID != attemptID {
		slog.Warn("stale attempt rejected: attempt ID mismatch",
			"job_id", job.ID,
			"reported_attempt_id", attemptID,
			"active_attempt_id", job.CurrentAttemptID,
		)
		return nil, domain.ErrStaleAttempt
	}

	attempt, err := s.repo.GetAttempt(ctx, attemptID)
	if err != nil {
		return nil, err
	}

	// Check 3: Verify lease token
	if attempt.LeaseToken != leaseToken {
		slog.Warn("stale attempt rejected: lease token mismatch",
			"job_id", job.ID,
			"attempt_id", attemptID,
		)
		return nil, domain.ErrStaleAttempt
	}

	// Check 4: Verify reporting worker is the assigned worker
	if attempt.WorkerID != workerID {
		slog.Warn("worker mismatch rejected: reporting worker not assigned to attempt",
			"job_id", job.ID,
			"attempt_id", attemptID,
			"assigned_worker", attempt.WorkerID,
			"reporting_worker", workerID,
		)
		return nil, domain.ErrWorkerMismatch
	}

	// Update attempt
	now := s.clock.Now()
	attempt.FinishedAt = &now
	attempt.ExitCode = exitCode
	attempt.Output = output
	attempt.Error = errSummary

	var targetState domain.JobState
	if exitCode == 0 && errSummary == "" {
		attempt.State = domain.AttemptCompleted
		targetState = domain.StateSucceeded
	} else {
		attempt.State = domain.AttemptFailed
		targetState = domain.StateFailed
	}

	if err := s.repo.UpdateAttempt(ctx, attempt); err != nil {
		return nil, err
	}

	if err := job.TransitionTo(targetState); err != nil {
		return nil, err
	}
	job.Result = &domain.Result{
		ExitCode: exitCode,
		Output:   output,
		Error:    errSummary,
	}

	if err := s.repo.UpdateJob(ctx, job); err != nil {
		return nil, err
	}

	// Reclaim worker slot
	if w, err := s.repo.GetWorker(ctx, workerID); err == nil {
		if w.AvailableSlots < w.TotalSlots {
			w.AvailableSlots++
			_ = s.repo.UpsertWorker(ctx, w)
		}
	}

	slog.Info("job result committed",
		"job_id", job.ID,
		"attempt_id", attempt.ID,
		"final_state", job.State,
		"exit_code", exitCode,
	)

	return job, nil
}
