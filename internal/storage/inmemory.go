package storage

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// InMemoryRepository is a thread-safe, in-memory implementation of the Repository interface.
//
// NOTE: This storage implementation is NON-DURABLE and TEMPORARY for Milestone 1 / Slice 3a.
// State will not survive process restarts. PostgreSQL durable persistence arrives in Phase 3 Slice 3c.
type InMemoryRepository struct {
	mu         sync.RWMutex
	jobs       map[string]*domain.Job
	attempts   map[string]*domain.Attempt
	workers    map[string]*domain.Worker
	events     []*domain.JobEvent
	jobOrder   []string // Preserves FIFO insertion order for queued jobs
	idSeq      int64
	eventIDSeq int64
	clock      Clock

	leaseDuration time.Duration
}

// InMemoryOption allows configuring an InMemoryRepository.
type InMemoryOption func(*InMemoryRepository)

// WithClock injects a custom clock into the InMemoryRepository for deterministic testing.
func WithClock(c Clock) InMemoryOption {
	return func(r *InMemoryRepository) {
		if c != nil {
			r.clock = c
		}
	}
}

// WithLeaseDuration sets the attempt lease length stamped at dispatch and applied on renewal
// (default domain.DefaultLeaseDuration, 30 s). Non-positive values are ignored.
func WithLeaseDuration(d time.Duration) InMemoryOption {
	return func(r *InMemoryRepository) {
		if d > 0 {
			r.leaseDuration = d
		}
	}
}

// NewInMemoryRepository constructs an initialized InMemoryRepository.
func NewInMemoryRepository(opts ...InMemoryOption) *InMemoryRepository {
	r := &InMemoryRepository{
		jobs:     make(map[string]*domain.Job),
		attempts: make(map[string]*domain.Attempt),
		workers:  make(map[string]*domain.Worker),
		events:   make([]*domain.JobEvent, 0),
		jobOrder: make([]string, 0),
		clock:    realClock{},

		leaseDuration: domain.DefaultLeaseDuration,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func generateRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// computeAvailableSlotsLocked dynamically computes worker capacity from RUNNING/PENDING attempts
// matching the PostgreSQL worker_capacity view specification in docs/schema.md and migration 000002.
// Must be called with mu held (either read or write lock).
func (r *InMemoryRepository) computeAvailableSlotsLocked(workerID string, totalSlots int) int {
	activeCount := 0
	for _, att := range r.attempts {
		if att.WorkerID == workerID && (att.State == domain.AttemptRunning || att.State == domain.AttemptPending) {
			activeCount++
		}
	}
	avail := totalSlots - activeCount
	if avail < 0 {
		return 0
	}
	return avail
}

// isDispatchableLocked reports whether a job may be claimed now: QUEUED, or RETRYING once its
// not_before backoff time has been reached on the repository clock (nil means immediately).
func (r *InMemoryRepository) isDispatchableLocked(j *domain.Job) bool {
	switch j.State {
	case domain.StateQueued:
		return true
	case domain.StateRetrying:
		return j.NotBefore == nil || !j.NotBefore.After(r.clock.Now())
	default:
		return false
	}
}

// newLeaseExpiryLocked returns repository-now plus the lease duration.
func (r *InMemoryRepository) newLeaseExpiryLocked() *time.Time {
	expiry := r.clock.Now().Add(r.leaseDuration)
	return &expiry
}

// recordEventLocked appends an immutable lifecycle event to the event log.
// Must be called with mu.Lock held.
func (r *InMemoryRepository) recordEventLocked(event *domain.JobEvent) {
	r.eventIDSeq++
	event.ID = r.eventIDSeq
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	copied := *event
	if event.Details != nil {
		det := make(map[string]any, len(event.Details))
		for k, v := range event.Details {
			det[k] = v
		}
		copied.Details = det
	}
	r.events = append(r.events, &copied)
}

// CreateJob persists a new job.
func (r *InMemoryRepository) CreateJob(ctx context.Context, job *domain.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.jobs[job.ID]; exists {
		return domain.ErrInvalidRequest
	}

	copied := *job
	r.jobs[job.ID] = &copied
	r.jobOrder = append(r.jobOrder, job.ID)
	return nil
}

// GetJob retrieves a job by ID.
func (r *InMemoryRepository) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	j, exists := r.jobs[id]
	if !exists {
		return nil, domain.ErrJobNotFound
	}

	copied := *j
	if j.Result != nil {
		res := *j.Result
		copied.Result = &res
	}
	return &copied, nil
}

// UpdateJob updates an existing job.
func (r *InMemoryRepository) UpdateJob(ctx context.Context, job *domain.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.jobs[job.ID]; !exists {
		return domain.ErrJobNotFound
	}

	copied := *job
	if job.Result != nil {
		res := *job.Result
		copied.Result = &res
	}
	r.jobs[job.ID] = &copied
	return nil
}

// ListQueuedJobs returns all dispatchable jobs in FIFO order: jobs in StateQueued, plus
// StateRetrying jobs whose not_before time has been reached.
func (r *InMemoryRepository) ListQueuedJobs(ctx context.Context) ([]*domain.Job, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var queued []*domain.Job
	for _, id := range r.jobOrder {
		j := r.jobs[id]
		if j != nil && r.isDispatchableLocked(j) {
			copied := *j
			queued = append(queued, &copied)
		}
	}
	return queued, nil
}

// CreateAttempt persists a new execution attempt, hashing the raw lease token (SHA-256)
// so the raw token is never persisted in storage.
func (r *InMemoryRepository) CreateAttempt(ctx context.Context, attempt *domain.Attempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	copied := *attempt
	if copied.LeaseToken != "" && copied.LeaseTokenHash == "" {
		copied.LeaseTokenHash = domain.HashLeaseToken(copied.LeaseToken)
	}
	// Raw lease token is never stored in repository
	copied.LeaseToken = ""
	r.attempts[attempt.ID] = &copied
	return nil
}

// GetAttempt retrieves an attempt by ID with the stored hash (raw token is empty).
func (r *InMemoryRepository) GetAttempt(ctx context.Context, id string) (*domain.Attempt, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	att, exists := r.attempts[id]
	if !exists {
		return nil, domain.ErrAttemptNotFound
	}

	copied := *att
	return &copied, nil
}

// UpdateAttempt updates an existing attempt.
func (r *InMemoryRepository) UpdateAttempt(ctx context.Context, attempt *domain.Attempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.attempts[attempt.ID]; !exists {
		return domain.ErrAttemptNotFound
	}

	copied := *attempt
	if copied.LeaseToken != "" && copied.LeaseTokenHash == "" {
		copied.LeaseTokenHash = domain.HashLeaseToken(copied.LeaseToken)
	}
	copied.LeaseToken = ""
	r.attempts[attempt.ID] = &copied
	return nil
}

// ListAttemptsByJob returns all attempts recorded for a specific job.
func (r *InMemoryRepository) ListAttemptsByJob(ctx context.Context, jobID string) ([]*domain.Attempt, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var list []*domain.Attempt
	for _, att := range r.attempts {
		if att.JobID == jobID {
			copied := *att
			list = append(list, &copied)
		}
	}
	return list, nil
}

// UpsertWorker inserts or updates a registered worker.
// AvailableSlots is not stored as a mutable counter; it is computed dynamically upon read.
func (r *InMemoryRepository) UpsertWorker(ctx context.Context, worker *domain.Worker) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	copied := *worker
	if existing, exists := r.workers[worker.ID]; exists {
		copied.RegisteredAt = existing.RegisteredAt
	} else if copied.RegisteredAt.IsZero() {
		copied.RegisteredAt = time.Now().UTC()
	}
	if copied.LastHeartbeatAt.IsZero() {
		copied.LastHeartbeatAt = time.Now().UTC()
	}
	copied.AvailableSlots = 0 // Stored counter never persisted
	r.workers[worker.ID] = &copied
	return nil
}

// GetWorker retrieves a worker by ID with dynamically computed AvailableSlots.
func (r *InMemoryRepository) GetWorker(ctx context.Context, id string) (*domain.Worker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	w, exists := r.workers[id]
	if !exists {
		return nil, domain.ErrWorkerNotFound
	}

	copied := *w
	copied.AvailableSlots = r.computeAvailableSlotsLocked(w.ID, w.TotalSlots)
	return &copied, nil
}

// RecordHeartbeat atomically updates a worker's last heartbeat time using the repository clock
// and restores HEALTHY status.
// AvailableSlots is dynamically derived from RUNNING/PENDING attempts, matching docs/schema.md.
func (r *InMemoryRepository) RecordHeartbeat(ctx context.Context, workerID string, _ int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, exists := r.workers[workerID]
	if !exists {
		return domain.ErrWorkerNotFound
	}
	w.LastHeartbeatAt = r.clock.Now()
	w.Status = domain.WorkerStatusHealthy
	return nil
}

// ListWorkers returns all registered workers with dynamically computed AvailableSlots.
func (r *InMemoryRepository) ListWorkers(ctx context.Context) ([]*domain.Worker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	workers := make([]*domain.Worker, 0, len(r.workers))
	for _, w := range r.workers {
		copied := *w
		copied.AvailableSlots = r.computeAvailableSlotsLocked(w.ID, w.TotalSlots)
		workers = append(workers, &copied)
	}
	return workers, nil
}

// ReapStaleWorkers atomically checks workers and transitions any HEALTHY worker to UNHEALTHY
// whose LastHeartbeatAt is older than olderThan (using r.clock). Returns the IDs of the reaped workers.
func (r *InMemoryRepository) ReapStaleWorkers(ctx context.Context, olderThan time.Duration) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := r.clock.Now().Add(-olderThan)
	var reaped []string
	for _, w := range r.workers {
		if w.Status == domain.WorkerStatusHealthy && w.LastHeartbeatAt.Before(cutoff) {
			w.Status = domain.WorkerStatusUnhealthy
			reaped = append(reaped, w.ID)
		}
	}
	return reaped, nil
}

// SubmitJob atomically inserts a job and records the initial audit event(s).
func (r *InMemoryRepository) SubmitJob(ctx context.Context, job *domain.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if job == nil || job.ID == "" {
		return domain.ErrInvalidRequest
	}
	if len(job.Payload) > 65536 {
		return domain.ErrPayloadTooLarge
	}
	if _, exists := r.jobs[job.ID]; exists {
		return domain.ErrInvalidRequest
	}

	copied := *job
	r.jobs[job.ID] = &copied
	r.jobOrder = append(r.jobOrder, job.ID)

	r.recordEventLocked(&domain.JobEvent{
		JobID:     job.ID,
		EventType: domain.JobEventSubmitted,
		CreatedAt: job.CreatedAt,
		Details: map[string]any{
			"task_type": job.TaskType,
			"state":     string(job.State),
		},
	})

	if job.State == domain.StateQueued {
		r.recordEventLocked(&domain.JobEvent{
			JobID:     job.ID,
			EventType: domain.JobEventQueued,
			CreatedAt: job.CreatedAt,
		})
	}

	return nil
}

// DispatchJob atomically checks all dispatch preconditions, transitions the job from QUEUED to RUNNING,
// creates an attempt with a hashed lease token, records an audit event, and returns the attempt
// with the raw lease token populated once for the worker.
//
// Preconditions enforced atomically (matching future SQL transaction):
// 1. Job exists (ErrJobNotFound)
// 2. Job is in QUEUED state (ErrJobNotQueued)
// 3. Worker exists (ErrWorkerNotFound)
// 4. Worker is HEALTHY (ErrWorkerUnhealthy)
// 5. Worker supports the task type (ErrWorkerTaskUnsupported)
// 6. Worker has free slot derived from active RUNNING attempts (ErrWorkerNoCapacity)
//
// Does NOT call the scheduler.
func (r *InMemoryRepository) DispatchJob(ctx context.Context, jobID string, workerID string) (*domain.Attempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, exists := r.jobs[jobID]
	if !exists {
		return nil, domain.ErrJobNotFound
	}
	if !r.isDispatchableLocked(job) {
		return nil, domain.ErrJobNotQueued
	}

	worker, exists := r.workers[workerID]
	if !exists {
		return nil, domain.ErrWorkerNotFound
	}
	if worker.Status != domain.WorkerStatusHealthy {
		return nil, domain.ErrWorkerUnhealthy
	}
	if !worker.SupportsTask(job.TaskType) {
		return nil, domain.ErrWorkerTaskUnsupported
	}

	availSlots := r.computeAvailableSlotsLocked(worker.ID, worker.TotalSlots)
	if availSlots <= 0 {
		return nil, domain.ErrWorkerNoCapacity
	}

	// Invariant: At most one active (RUNNING) attempt per job
	for _, att := range r.attempts {
		if att.JobID == jobID && att.State == domain.AttemptRunning {
			return nil, domain.ErrInvalidRequest
		}
	}

	now := time.Now().UTC()
	fromState := job.State
	if err := job.TransitionTo(domain.StateRunning); err != nil {
		return nil, err
	}
	job.NotBefore = nil

	// Calculate monotonic attempt number
	attemptNo := 1
	for _, att := range r.attempts {
		if att.JobID == jobID {
			attemptNo++
		}
	}

	r.idSeq++
	attemptID := fmt.Sprintf("att_%d_%s", r.idSeq, generateRandomHex(6))
	rawLeaseToken, err := domain.NewLeaseToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate secure lease token: %w", err)
	}

	tokenHashHex := domain.HashLeaseToken(rawLeaseToken)

	storedAttempt := &domain.Attempt{
		ID:             attemptID,
		JobID:          jobID,
		AttemptNo:      attemptNo,
		WorkerID:       workerID,
		LeaseToken:     "", // Raw token NEVER stored
		LeaseTokenHash: tokenHashHex,
		State:          domain.AttemptRunning,
		StartedAt:      now,
		LeaseExpiresAt: r.newLeaseExpiryLocked(),
	}
	r.attempts[attemptID] = storedAttempt
	job.CurrentAttemptID = attemptID
	job.UpdatedAt = now

	toState := domain.StateRunning
	r.recordEventLocked(&domain.JobEvent{
		JobID:     jobID,
		AttemptID: &attemptID,
		EventType: domain.JobEventDispatched,
		FromState: &fromState,
		ToState:   &toState,
		CreatedAt: now,
		Details: map[string]any{
			"worker_id":  workerID,
			"attempt_no": attemptNo,
		},
	})

	// Return attempt to caller with raw lease token populated once
	retAttempt := *storedAttempt
	retAttempt.LeaseToken = rawLeaseToken
	return &retAttempt, nil
}

// DispatchNextJob atomically selects the next eligible QUEUED job matching the worker's supported tasks
// in FIFO order, transitions it to RUNNING, creates an attempt, and records an audit event.
//
// Precondition Error Precedence (strictly matching PostgreSQL transaction):
//  1. Worker existence (ErrWorkerNotFound)
//  2. Worker status == HEALTHY (ErrWorkerUnhealthy)
//  3. Worker has available capacity (ErrWorkerNoCapacity)
//  4. Eligible job exists (ErrNoQueuedJobs)
//  5. Job transition validity (ErrInvalidStateTransition)
func (r *InMemoryRepository) DispatchNextJob(ctx context.Context, workerID string) (*domain.Job, *domain.Attempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker, exists := r.workers[workerID]
	if !exists {
		return nil, nil, domain.ErrWorkerNotFound
	}
	if worker.Status != domain.WorkerStatusHealthy {
		return nil, nil, domain.ErrWorkerUnhealthy
	}

	availSlots := r.computeAvailableSlotsLocked(worker.ID, worker.TotalSlots)
	if availSlots <= 0 {
		return nil, nil, domain.ErrWorkerNoCapacity
	}

	// Select next eligible QUEUED job in FIFO order matching worker supported tasks
	var selectedJob *domain.Job
	for _, id := range r.jobOrder {
		j := r.jobs[id]
		if j != nil && r.isDispatchableLocked(j) && worker.SupportsTask(j.TaskType) {
			selectedJob = j
			break
		}
	}
	if selectedJob == nil {
		return nil, nil, domain.ErrNoQueuedJobs
	}

	// Invariant: At most one active (RUNNING) attempt per job
	for _, att := range r.attempts {
		if att.JobID == selectedJob.ID && (att.State == domain.AttemptRunning || att.State == domain.AttemptPending) {
			return nil, nil, domain.ErrInvalidRequest
		}
	}

	now := r.clock.Now()
	fromState := selectedJob.State
	if err := selectedJob.TransitionTo(domain.StateRunning); err != nil {
		return nil, nil, err
	}
	selectedJob.NotBefore = nil

	// Calculate monotonic attempt number derived from max existing attempt_no + 1
	attemptNo := 0
	for _, att := range r.attempts {
		if att.JobID == selectedJob.ID && att.AttemptNo > attemptNo {
			attemptNo = att.AttemptNo
		}
	}
	attemptNo++

	r.idSeq++
	attemptID := fmt.Sprintf("att_%d_%s", r.idSeq, generateRandomHex(6))
	rawLeaseToken, err := domain.NewLeaseToken()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate secure lease token: %w", err)
	}

	tokenHash := domain.HashLeaseToken(rawLeaseToken)
	storedAttempt := &domain.Attempt{
		ID:             attemptID,
		JobID:          selectedJob.ID,
		AttemptNo:      attemptNo,
		WorkerID:       workerID,
		LeaseTokenHash: tokenHash,
		State:          domain.AttemptRunning,
		StartedAt:      now,
		LeaseExpiresAt: r.newLeaseExpiryLocked(),
	}
	r.attempts[attemptID] = storedAttempt
	selectedJob.CurrentAttemptID = attemptID
	selectedJob.UpdatedAt = now

	toState := domain.StateRunning
	r.recordEventLocked(&domain.JobEvent{
		JobID:     selectedJob.ID,
		AttemptID: &attemptID,
		EventType: domain.JobEventDispatched,
		FromState: &fromState,
		ToState:   &toState,
		CreatedAt: now,
		Details: map[string]any{
			"worker_id":  workerID,
			"attempt_no": attemptNo,
		},
	})

	retJob := *selectedJob
	retAttempt := *storedAttempt
	retAttempt.LeaseToken = rawLeaseToken
	return &retJob, &retAttempt, nil
}

// CommitAttempt atomically validates the commit invariants, updates the attempt, transitions the job
// to terminal state, stores the result, and records an audit event.
//
// Invariants enforced atomically, in this order:
// 1. Job exists (ErrJobNotFound)
// 2. Job is not already terminal (ErrJobAlreadyTerminal)
// 3. Attempt was not swept to LOST (ErrLeaseExpired; records one bounded diagnostic event)
// 4. Attempt matches job's active current attempt (ErrStaleAttempt)
// 5. Attempt exists and is RUNNING (ErrStaleAttempt)
// 6. SHA-256 hash of provided lease token matches stored hash in constant time (ErrStaleAttempt)
// 7. Reporting worker matches assigned worker (ErrWorkerMismatch)
//
// An attempt whose lease has passed but which the sweeper has not yet swept is still accepted
// (symmetric accept-until-swept rule).
func (r *InMemoryRepository) CommitAttempt(ctx context.Context, jobID, attemptID, leaseToken, workerID string, outcome domain.AttemptOutcome, backoff domain.BackoffFunc) (*domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, exists := r.jobs[jobID]
	if !exists {
		return nil, domain.ErrJobNotFound
	}
	if job.State.IsTerminal() {
		r.rejectTerminalCommitLocked(job, attemptID, workerID)
		return nil, domain.ErrJobAlreadyTerminal
	}
	// ADR-0014 / protocol 6.3 evaluation order: a swept (LOST) attempt of this job is
	// LEASE_EXPIRED, even when it was also superseded or the token is wrong.
	if lost, ok := r.attempts[attemptID]; ok && lost.JobID == job.ID && lost.State == domain.AttemptLost {
		r.recordLateResultLocked(job.ID, attemptID, workerID)
		return nil, domain.ErrLeaseExpired
	}
	if job.CurrentAttemptID != attemptID {
		return nil, domain.ErrStaleAttempt
	}

	att, exists := r.attempts[attemptID]
	if !exists {
		return nil, domain.ErrAttemptNotFound
	}
	if att.State != domain.AttemptRunning {
		return nil, domain.ErrStaleAttempt
	}

	// Constant-time compare SHA-256 hash of provided lease token
	providedHashHex := domain.HashLeaseToken(leaseToken)
	if subtle.ConstantTimeCompare([]byte(att.LeaseTokenHash), []byte(providedHashHex)) != 1 {
		return nil, domain.ErrStaleAttempt
	}

	// Verify worker match
	if att.WorkerID != workerID {
		return nil, &domain.WorkerMismatchError{
			AssignedWorker:  att.WorkerID,
			ReportingWorker: workerID,
		}
	}

	failed := domain.IsFailedOutcome(outcome)
	class, err := domain.NormalizeWorkerErrorClass(outcome.ErrorClassification, failed)
	if err != nil {
		return nil, err
	}
	var resolution domain.FailureResolution
	if failed {
		resolution = domain.ResolveFailure(job.State, class, att.AttemptNo, job.MaxAttempts)
		if resolution.Retry && backoff == nil {
			return nil, fmt.Errorf("%w: a retryable failure needs a backoff function", domain.ErrInvalidRequest)
		}
	}

	now := r.clock.Now()
	fromState := job.State
	var (
		targetState domain.JobState
		eventType   domain.JobEventType
		attState    domain.AttemptState
		details     = map[string]any{"worker_id": workerID, "exit_code": outcome.ExitCode}
	)
	switch {
	case !failed:
		attState, targetState, eventType = domain.AttemptCompleted, domain.StateSucceeded, domain.JobEventCompleted
	case resolution.Retry:
		attState, targetState, eventType = domain.AttemptFailed, domain.StateRetrying, domain.JobEventRetrying
	case resolution.JobState == domain.StateCancelled:
		attState, targetState, eventType = domain.AttemptFailed, domain.StateCancelled, domain.JobEventCancelled
	default:
		attState, targetState, eventType = domain.AttemptFailed, domain.StateFailed, domain.JobEventFailed
	}
	if err := job.TransitionTo(targetState); err != nil {
		return nil, err
	}

	att.State = attState
	att.FinishedAt = &now
	att.ExitCode = outcome.ExitCode
	att.Output = outcome.Output
	att.Error = outcome.Error
	att.ErrorClass = class
	job.UpdatedAt = now
	job.NotBefore = nil
	if failed {
		details["error_class"] = string(class)
		details["attempt_no"] = att.AttemptNo
	}
	if resolution.Retry {
		delay := max(backoff(att.AttemptNo), 0)
		notBefore := now.Add(delay)
		job.NotBefore = &notBefore
		details["not_before"] = notBefore.UTC().Format(time.RFC3339Nano)
		details["backoff_seconds"] = delay.Seconds()
	} else {
		job.Result = &domain.Result{
			ExitCode: outcome.ExitCode,
			Output:   outcome.Output,
			Error:    outcome.Error,
		}
		if resolution.Exhausted {
			details["error"] = "max attempts exhausted"
		}
	}

	r.recordEventLocked(&domain.JobEvent{
		JobID:     jobID,
		AttemptID: &attemptID,
		EventType: eventType,
		FromState: &fromState,
		ToState:   &targetState,
		CreatedAt: now,
		Details:   details,
	})

	copied := *job
	if job.Result != nil {
		res := *job.Result
		copied.Result = &res
	}
	return &copied, nil
}

// rejectTerminalCommitLocked is the SEC-022 cleanup for a commit that hits an already-terminal
// job. It closes the named attempt only while that attempt is still RUNNING and is the job's
// current attempt (CANCELLED if the job is CANCELLED, otherwise LOST), which frees the worker
// slot, and records at most one DIAGNOSTIC_LATE_RESULT event per attempt of this job. An attempt
// that is already COMPLETED, FAILED, CANCELLED or LOST is never modified, and an attempt ID that
// does not belong to the job records nothing. Must be called with mu.Lock held.
func (r *InMemoryRepository) rejectTerminalCommitLocked(job *domain.Job, attemptID, reportingWorker string) {
	att, exists := r.attempts[attemptID]
	if !exists || att.JobID != job.ID {
		return
	}
	now := r.clock.Now()

	if att.State == domain.AttemptRunning && job.CurrentAttemptID == attemptID {
		if job.State == domain.StateCancelled {
			att.State = domain.AttemptCancelled
		} else {
			att.State = domain.AttemptLost
		}
		att.FinishedAt = &now
	}

	r.recordLateResultLocked(job.ID, attemptID, reportingWorker)
}

// recordLateResultLocked records at most one DIAGNOSTIC_LATE_RESULT event per (job, attempt),
// the in-memory counterpart of the unique index idx_job_events_unique_diagnostic_late_result.
// The caller has already established that the attempt belongs to the job. Must be called with
// mu.Lock held.
func (r *InMemoryRepository) recordLateResultLocked(jobID, attemptID, reportingWorker string) {
	for _, ev := range r.events {
		if ev.JobID == jobID && ev.EventType == domain.JobEventDiagnosticLateResult &&
			ev.AttemptID != nil && *ev.AttemptID == attemptID {
			return
		}
	}
	r.recordEventLocked(&domain.JobEvent{
		JobID:     jobID,
		AttemptID: &attemptID,
		EventType: domain.JobEventDiagnosticLateResult,
		CreatedAt: r.clock.Now(),
		Details: map[string]any{
			"attempt_id":       attemptID,
			"reporting_worker": reportingWorker,
			"rejected":         true,
		},
	})
}

// CancelJob atomically cancels a queued, retrying or running job and records an audit event.
// A duplicate cancel on a CANCEL_REQUESTED job is an idempotent no-op (SEC-022): the job is
// returned unchanged, with no state transition and no audit event.
func (r *InMemoryRepository) CancelJob(ctx context.Context, jobID string) (*domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, exists := r.jobs[jobID]
	if !exists {
		return nil, domain.ErrJobNotFound
	}
	if job.State.IsTerminal() {
		return nil, domain.ErrJobAlreadyTerminal
	}

	if job.State == domain.StateCancelRequested {
		copied := *job
		return &copied, nil
	}

	fromState := job.State
	var targetState domain.JobState
	var eventType domain.JobEventType
	if job.State == domain.StateRunning {
		targetState = domain.StateCancelRequested
		eventType = domain.JobEventCancelRequested
	} else {
		// QUEUED and RETRYING cancel immediately.
		targetState = domain.StateCancelled
		eventType = domain.JobEventCancelled
	}

	if err := job.TransitionTo(targetState); err != nil {
		return nil, err
	}
	now := r.clock.Now()
	job.UpdatedAt = now
	if targetState == domain.StateCancelRequested {
		job.CancelRequestedAt = &now
	} else {
		job.NotBefore = nil
	}

	r.recordEventLocked(&domain.JobEvent{
		JobID:     jobID,
		EventType: eventType,
		FromState: &fromState,
		ToState:   &targetState,
		CreatedAt: now,
	})

	copied := *job
	return &copied, nil
}

// RenewLease extends the lease of a RUNNING attempt to repository-now plus the lease duration.
//
// Evaluation order (ADR-0014, protocol 6.3), mirroring CommitAttempt:
//  1. Job exists (ErrJobNotFound)
//  2. Job is not terminal (ErrJobAlreadyTerminal)
//  3. Attempt of this job was swept to LOST (ErrLeaseExpired)
//  4. Attempt is the job's current attempt, is RUNNING and the token matches in constant time
//     (ErrStaleAttempt), and the job is RUNNING or CANCEL_REQUESTED
//  5. Reporting worker matches (ErrWorkerMismatch)
//
// A lease that has passed but was not swept is still renewed (accept-until-swept). On a
// CANCEL_REQUESTED job the call reports cancel_requested and returns the current expiry without
// extending it. A renewal records no event and never modifies a rejected attempt.
func (r *InMemoryRepository) RenewLease(ctx context.Context, jobID, attemptID, leaseToken, workerID string) (*domain.LeaseRenewal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, exists := r.jobs[jobID]
	if !exists {
		return nil, domain.ErrJobNotFound
	}
	if job.State.IsTerminal() {
		return nil, domain.ErrJobAlreadyTerminal
	}
	att, attExists := r.attempts[attemptID]
	if attExists && att.JobID == job.ID && att.State == domain.AttemptLost {
		return nil, domain.ErrLeaseExpired
	}
	if job.CurrentAttemptID != attemptID {
		return nil, domain.ErrStaleAttempt
	}
	if !attExists {
		return nil, domain.ErrAttemptNotFound
	}
	if att.State != domain.AttemptRunning {
		return nil, domain.ErrStaleAttempt
	}
	providedHashHex := domain.HashLeaseToken(leaseToken)
	if subtle.ConstantTimeCompare([]byte(att.LeaseTokenHash), []byte(providedHashHex)) != 1 {
		return nil, domain.ErrStaleAttempt
	}
	if att.WorkerID != workerID {
		return nil, &domain.WorkerMismatchError{AssignedWorker: att.WorkerID, ReportingWorker: workerID}
	}

	switch job.State {
	case domain.StateCancelRequested:
		renewal := &domain.LeaseRenewal{CancelRequested: true}
		if att.LeaseExpiresAt != nil {
			renewal.LeaseExpiresAt = *att.LeaseExpiresAt
		}
		return renewal, nil
	case domain.StateRunning:
		att.LeaseExpiresAt = r.newLeaseExpiryLocked()
		return &domain.LeaseRenewal{LeaseExpiresAt: *att.LeaseExpiresAt}, nil
	default:
		return nil, domain.ErrStaleAttempt
	}
}

// SweepExpiredLeases closes RUNNING attempts whose lease has passed on the repository clock,
// oldest lease first, at most params.Limit per call, and resolves their jobs:
//   - job already terminal: attempt CANCELLED (job CANCELLED) or LOST, job untouched, LEASE_EXPIRED
//   - job CANCEL_REQUESTED: attempt CANCELLED, job CANCELLED, only a JOB_CANCELLED event
//   - job RUNNING and attempt_no < max_attempts: attempt LOST (LEASE_EXPIRED), job RETRYING with
//     not_before = now + params.Backoff(attempt_no); LEASE_EXPIRED and JOB_RETRYING events
//   - job RUNNING and attempt_no >= max_attempts: attempt LOST, job FAILED; LEASE_EXPIRED and
//     JOB_FAILED events
//
// The grace check (params.Grace) is applied per candidate after the re-check that the attempt is
// still RUNNING and expired; the repository neither owns nor advances the grace state. The single
// mutex stands in for the PostgreSQL row locks, so SkippedLocked is always zero here.
func (r *InMemoryRepository) SweepExpiredLeases(ctx context.Context, params domain.SweepParams) (*domain.SweepResult, error) {
	if params.Backoff == nil {
		return nil, domain.ErrInvalidRequest
	}
	limit := params.Limit
	if limit <= 0 {
		limit = domain.DefaultSweepBatchSize
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.clock.Now()
	var candidates []*domain.Attempt
	for _, a := range r.attempts {
		if a.State == domain.AttemptRunning && a.LeaseExpiresAt != nil && a.LeaseExpiresAt.Before(now) {
			candidates = append(candidates, a)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].LeaseExpiresAt.Equal(*candidates[j].LeaseExpiresAt) {
			return candidates[i].LeaseExpiresAt.Before(*candidates[j].LeaseExpiresAt)
		}
		return candidates[i].ID < candidates[j].ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	res := &domain.SweepResult{Candidates: len(candidates)}
	for _, att := range candidates {
		job, exists := r.jobs[att.JobID]
		if !exists {
			res.SkippedChanged++
			continue
		}
		// Re-check, then the authoritative grace check.
		if att.State != domain.AttemptRunning || att.LeaseExpiresAt == nil || !att.LeaseExpiresAt.Before(now) {
			res.SkippedChanged++
			continue
		}
		if params.Grace.Suppresses(now) {
			res.SuppressedByGrace++
			continue
		}
		outcome, err := r.sweepAttemptLocked(job, att, now, params.Backoff)
		if err != nil {
			return res, err
		}
		if outcome == nil {
			res.SkippedChanged++
			continue
		}
		res.Swept = append(res.Swept, *outcome)
	}
	return res, nil
}

// sweepAttemptLocked resolves one expired attempt. It returns nil when the job is in a state the
// sweeper has no rule for (a RUNNING attempt under a QUEUED, SUBMITTED or RETRYING job cannot be
// produced through the public API); nothing is mutated then. Must be called with mu.Lock held.
func (r *InMemoryRepository) sweepAttemptLocked(job *domain.Job, att *domain.Attempt, now time.Time, backoff domain.BackoffFunc) (*domain.SweepOutcome, error) {
	expiredAt := att.LeaseExpiresAt.UTC().Format(time.RFC3339Nano)
	leaseExpiredEvent := func() {
		attID := att.ID
		r.recordEventLocked(&domain.JobEvent{
			JobID:     job.ID,
			AttemptID: &attID,
			EventType: domain.JobEventLeaseExpired,
			CreatedAt: now,
			Details: map[string]any{
				"attempt_id": att.ID,
				"worker_id":  att.WorkerID,
				"expired_at": expiredAt,
			},
		})
	}
	markLost := func() {
		att.State = domain.AttemptLost
		att.ErrorClass = domain.ClassLeaseExpired
		att.Error = "lease expired: worker failed to renew"
		att.FinishedAt = &now
	}
	jobEvent := func(typ domain.JobEventType, from, to domain.JobState, details map[string]any) {
		attID := att.ID
		r.recordEventLocked(&domain.JobEvent{
			JobID:     job.ID,
			AttemptID: &attID,
			EventType: typ,
			FromState: &from,
			ToState:   &to,
			CreatedAt: now,
			Details:   details,
		})
	}

	switch {
	case job.State.IsTerminal():
		if job.State == domain.StateCancelled {
			att.State = domain.AttemptCancelled
			att.FinishedAt = &now
		} else {
			markLost()
		}
		leaseExpiredEvent()

	case job.State == domain.StateCancelRequested:
		from := job.State
		if err := job.TransitionTo(domain.StateCancelled); err != nil {
			return nil, err
		}
		job.UpdatedAt = now
		job.NotBefore = nil
		att.State = domain.AttemptCancelled
		att.FinishedAt = &now
		jobEvent(domain.JobEventCancelled, from, domain.StateCancelled, map[string]any{
			"resolved_by": "lease_sweeper",
			"reason":      "worker unresponsive",
		})

	case job.State == domain.StateRunning && att.AttemptNo < job.MaxAttempts:
		delay := backoff(att.AttemptNo)
		if delay < 0 {
			delay = 0
		}
		from := job.State
		if err := job.TransitionTo(domain.StateRetrying); err != nil {
			return nil, err
		}
		notBefore := now.Add(delay)
		job.UpdatedAt = now
		job.NotBefore = &notBefore
		markLost()
		leaseExpiredEvent()
		jobEvent(domain.JobEventRetrying, from, domain.StateRetrying, map[string]any{
			"attempt_no":      att.AttemptNo,
			"not_before":      notBefore.UTC().Format(time.RFC3339Nano),
			"backoff_seconds": delay.Seconds(),
		})

	case job.State == domain.StateRunning:
		from := job.State
		if err := job.TransitionTo(domain.StateFailed); err != nil {
			return nil, err
		}
		job.UpdatedAt = now
		job.NotBefore = nil
		markLost()
		leaseExpiredEvent()
		jobEvent(domain.JobEventFailed, from, domain.StateFailed, map[string]any{
			"attempt_no":  att.AttemptNo,
			"error":       "max attempts exhausted",
			"error_class": string(domain.ClassLeaseExpired),
		})

	default:
		return nil, nil
	}

	return &domain.SweepOutcome{JobID: job.ID, AttemptID: att.ID, AttemptState: att.State, JobState: job.State}, nil
}

// ListEventsByJob retrieves all recorded lifecycle events for a specific job in order.
func (r *InMemoryRepository) ListEventsByJob(ctx context.Context, jobID string) ([]*domain.JobEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var events []*domain.JobEvent
	for _, ev := range r.events {
		if ev.JobID == jobID {
			copied := *ev
			events = append(events, &copied)
		}
	}
	return events, nil
}

// Ping implements storage.Repository. For InMemoryRepository it is always healthy.
func (r *InMemoryRepository) Ping(ctx context.Context) error {
	return nil
}

// Now returns the repository clock (the injected Clock, UTC in production).
func (r *InMemoryRepository) Now(ctx context.Context) (time.Time, error) {
	return r.clock.Now(), nil
}
