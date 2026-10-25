package coordinator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/queue"
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
	mu              sync.Mutex
	repo            storage.Repository
	scheduler       scheduler.Scheduler
	executorReg     *executor.Registry
	queue           queue.Queue
	clock           Clock
	workerTimeout   time.Duration
	dispatchViaRepo bool
}

// NewService constructs a new Coordinator Service with the default in-memory queue.
func NewService(repo storage.Repository, sched scheduler.Scheduler, reg *executor.Registry, workerTimeout time.Duration) *Service {
	return NewServiceWithQueue(repo, sched, reg, queue.NewInMemoryQueue(), workerTimeout)
}

// NewServiceWithQueue constructs a new Coordinator Service with a specified dispatch Queue.
func NewServiceWithQueue(repo storage.Repository, sched scheduler.Scheduler, reg *executor.Registry, q queue.Queue, workerTimeout time.Duration) *Service {
	if workerTimeout <= 0 {
		workerTimeout = 10 * time.Second
	}
	if q == nil {
		q = queue.NewInMemoryQueue()
	}
	return &Service{
		repo:            repo,
		scheduler:       sched,
		executorReg:     reg,
		queue:           q,
		clock:           realClock{},
		workerTimeout:   workerTimeout,
		dispatchViaRepo: false,
	}
}

// NewPostgresService constructs a new Coordinator Service wired directly to PostgreSQL repository dispatch (ADR-0011 Option A).
// In this mode, no in-memory queue is used, preventing unbounded memory growth (SEC-010), and all dispatches flow
// through repo.DispatchNextJob.
func NewPostgresService(repo storage.Repository, sched scheduler.Scheduler, reg *executor.Registry, workerTimeout time.Duration) *Service {
	if workerTimeout <= 0 {
		workerTimeout = 10 * time.Second
	}
	return &Service{
		repo:            repo,
		scheduler:       sched,
		executorReg:     reg,
		queue:           nil,
		clock:           realClock{},
		workerTimeout:   workerTimeout,
		dispatchViaRepo: true,
	}
}

// Ping checks repository reachability for readiness probes.
func (s *Service) Ping(ctx context.Context) error {
	return s.repo.Ping(ctx)
}

// SetClock injects a custom clock (e.g. for deterministic unit tests).
func (s *Service) SetClock(c Clock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = c
}

const (
	DefaultMaxAttempts    = 3
	MinMaxAttempts        = 1
	MaxMaxAttempts        = 20
	DefaultTimeoutSeconds = 60
	MinTimeoutSeconds     = 1
	MaxTimeoutSeconds     = 86400
)

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

	if maxAttempts < MinMaxAttempts || maxAttempts > MaxMaxAttempts {
		return nil, fmt.Errorf("%w: max_attempts must be between %d and %d (got %d)", domain.ErrInvalidRequest, MinMaxAttempts, MaxMaxAttempts, maxAttempts)
	}
	if timeoutSec < MinTimeoutSeconds || timeoutSec > MaxTimeoutSeconds {
		return nil, fmt.Errorf("%w: timeout_seconds must be between %d and %d (got %d)", domain.ErrInvalidRequest, MinTimeoutSeconds, MaxTimeoutSeconds, timeoutSec)
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

	if err := s.repo.SubmitJob(ctx, job); err != nil {
		return nil, err
	}

	// In memory mode: Enqueue lightweight delivery signal in dispatch transport.
	// In postgres mode: Nothing is enqueued into in-memory queue, preventing unbounded queue growth (SEC-010).
	if !s.dispatchViaRepo && s.queue != nil {
		if err := s.queue.Enqueue(ctx, &queue.Item{
			JobID:    job.ID,
			TaskType: job.TaskType,
		}); err != nil {
			return nil, err
		}
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

// CancelJob cancels a queued or running job atomically.
func (s *Service) CancelJob(ctx context.Context, id string) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, err := s.repo.CancelJob(ctx, id)
	if err != nil {
		return nil, err
	}

	if job.State == domain.StateCancelled && !s.dispatchViaRepo && s.queue != nil {
		_ = s.queue.Ack(ctx, job.ID)
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
	return s.repo.RecordHeartbeat(ctx, workerID, availableSlots)
}

// ListWorkers returns all registered workers.
func (s *Service) ListWorkers(ctx context.Context) ([]*domain.Worker, error) {
	return s.repo.ListWorkers(ctx)
}

// ReapInactiveWorkers scans registered workers and marks any worker UNHEALTHY
// whose last heartbeat is older than workerTimeout.
func (s *Service) ReapInactiveWorkers(ctx context.Context) ([]string, error) {
	reaped, err := s.repo.ReapStaleWorkers(ctx, s.workerTimeout)
	if err != nil {
		return nil, err
	}
	if len(reaped) > 0 {
		slog.Warn("reaped inactive workers due to heartbeat timeout",
			"count", len(reaped),
			"worker_ids", reaped,
			"timeout", s.workerTimeout,
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
// A poll from an UNHEALTHY worker does not count as liveness and returns ErrWorkerUnhealthy.
func (s *Service) PollWork(ctx context.Context, workerID string) (*WorkAssignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dispatchViaRepo {
		// Postgres / Repo-direct dispatch path (ADR-0011 Option A, policy fifo_pull)
		job, attempt, err := s.repo.DispatchNextJob(ctx, workerID)
		if err != nil {
			if errors.Is(err, domain.ErrWorkerNoCapacity) {
				return nil, domain.ErrNoWorkersAvailable
			}
			return nil, err
		}

		slog.Info("job dispatched to worker",
			"job_id", attempt.JobID,
			"attempt_id", attempt.ID,
			"attempt_no", attempt.AttemptNo,
			"worker_id", workerID,
			"policy", "fifo_pull",
			"reason", "claimed by polling worker with capacity",
		)

		return &WorkAssignment{
			JobID:          job.ID,
			AttemptID:      attempt.ID,
			AttemptNo:      attempt.AttemptNo,
			LeaseToken:     attempt.LeaseToken,
			TaskType:       job.TaskType,
			Payload:        job.Payload,
			TimeoutSeconds: job.TimeoutSeconds,
		}, nil
	}

	worker, err := s.repo.GetWorker(ctx, workerID)
	if err != nil {
		return nil, err
	}

	// Poll from an UNHEALTHY worker returns ErrWorkerUnhealthy
	if worker.Status != domain.WorkerStatusHealthy {
		return nil, domain.ErrWorkerUnhealthy
	}

	if !worker.HasCapacity() {
		return nil, domain.ErrNoWorkersAvailable
	}

	for {
		item, err := s.queue.Claim(ctx, worker)
		if err != nil {
			if errors.Is(err, queue.ErrNoWork) {
				return nil, domain.ErrNoQueuedJobs
			}
			return nil, err
		}

		attempt, err := s.repo.DispatchJob(ctx, item.JobID, worker.ID)
		if err != nil {
			// Handling of claim outcomes (Adjustment 4):
			// (a) Job no longer QUEUED or not found -> drop and Ack the queue item (never Release), then continue claim loop
			if errors.Is(err, domain.ErrJobNotFound) || errors.Is(err, domain.ErrJobNotQueued) {
				_ = s.queue.Ack(ctx, item.JobID)
				continue
			}

			// (c) Worker lacks capacity or is unhealthy -> Release without creating an attempt, and return error
			if errors.Is(err, domain.ErrWorkerUnhealthy) || errors.Is(err, domain.ErrWorkerNoCapacity) {
				_ = s.queue.Release(ctx, item.JobID)
				return nil, err
			}

			// (b) Transient failure -> Release and return error
			_ = s.queue.Release(ctx, item.JobID)
			return nil, err
		}

		// Dispatch logging per ADR-0011 (fifo_pull):
		// the "job dispatched to worker" log gains policy="fifo_pull" and reason="claimed by polling worker with capacity".
		// Do NOT call scheduler.SelectWorker anywhere in the dispatch path and do NOT emit "scheduler decision" logs from dispatch.
		// internal/scheduler stays unused in production code on purpose.
		slog.Info("job dispatched to worker",
			"job_id", attempt.JobID,
			"attempt_id", attempt.ID,
			"attempt_no", attempt.AttemptNo,
			"worker_id", worker.ID,
			"policy", "fifo_pull",
			"reason", "claimed by polling worker with capacity",
		)

		selectedJob, err := s.repo.GetJob(ctx, attempt.JobID)
		if err != nil {
			return nil, err
		}

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
}

// CommitResult commits an execution outcome reported by a worker atomically.
//
// CRITICAL INVARIANTS:
// 1. Rejects commit if job is already terminal.
// 2. Rejects stale attempts if attempt_id or lease_token does not match active attempt.
// 3. Rejects commit if reporting worker is not the worker assigned to the attempt (WORKER_MISMATCH).
// 4. Accepts commit from a worker marked UNHEALTHY if attempt_id, lease_token, and worker_id match.
func (s *Service) CommitResult(ctx context.Context, jobID, attemptID, leaseToken, workerID string, exitCode int, output, errSummary string) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	outcome := domain.AttemptOutcome{
		ExitCode: exitCode,
		Output:   output,
		Error:    errSummary,
	}

	job, err := s.repo.CommitAttempt(ctx, jobID, attemptID, leaseToken, workerID, outcome)
	if err != nil {
		if errors.Is(err, domain.ErrJobAlreadyTerminal) {
			slog.Warn("rejecting result for already-terminal job",
				"job_id", jobID,
				"attempt_id", attemptID,
			)
			return nil, domain.ErrJobAlreadyTerminal
		}
		if errors.Is(err, domain.ErrStaleAttempt) {
			slog.Warn("stale attempt rejected",
				"job_id", jobID,
				"attempt_id", attemptID,
			)
			return nil, domain.ErrStaleAttempt
		}
		if errors.Is(err, domain.ErrWorkerMismatch) {
			assignedWorker := "unknown"
			var mismatchErr *domain.WorkerMismatchError
			if errors.As(err, &mismatchErr) {
				assignedWorker = mismatchErr.AssignedWorker
			} else if att, getErr := s.repo.GetAttempt(ctx, attemptID); getErr == nil && att != nil {
				assignedWorker = att.WorkerID
			}
			slog.Warn("worker mismatch rejected: reporting worker not assigned to attempt",
				"job_id", jobID,
				"attempt_id", attemptID,
				"assigned_worker", assignedWorker,
				"reporting_worker", workerID,
			)
			return nil, domain.ErrWorkerMismatch
		}
		return nil, err
	}

	// Settle delivery in dispatch transport ONLY after an accepted commit (in memory mode)
	if !s.dispatchViaRepo && s.queue != nil {
		_ = s.queue.Ack(ctx, job.ID)
	}

	slog.Info("job result committed",
		"job_id", job.ID,
		"attempt_id", attemptID,
		"final_state", job.State,
		"exit_code", exitCode,
	)

	return job, nil
}
