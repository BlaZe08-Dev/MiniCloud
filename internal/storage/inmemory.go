package storage

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
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

// NewInMemoryRepository constructs an initialized InMemoryRepository.
func NewInMemoryRepository(opts ...InMemoryOption) *InMemoryRepository {
	r := &InMemoryRepository{
		jobs:     make(map[string]*domain.Job),
		attempts: make(map[string]*domain.Attempt),
		workers:  make(map[string]*domain.Worker),
		events:   make([]*domain.JobEvent, 0),
		jobOrder: make([]string, 0),
		clock:    realClock{},
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

// ListQueuedJobs returns all jobs currently in StateQueued in FIFO order.
func (r *InMemoryRepository) ListQueuedJobs(ctx context.Context) ([]*domain.Job, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var queued []*domain.Job
	for _, id := range r.jobOrder {
		j := r.jobs[id]
		if j != nil && j.State == domain.StateQueued {
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
	if job.State != domain.StateQueued {
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
	if err := job.TransitionTo(domain.StateRunning); err != nil {
		return nil, err
	}

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
	}
	r.attempts[attemptID] = storedAttempt
	job.CurrentAttemptID = attemptID
	job.UpdatedAt = now

	fromState := domain.StateQueued
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
		if j != nil && j.State == domain.StateQueued && worker.SupportsTask(j.TaskType) {
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
	if err := selectedJob.TransitionTo(domain.StateRunning); err != nil {
		return nil, nil, err
	}

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
	}
	r.attempts[attemptID] = storedAttempt
	selectedJob.CurrentAttemptID = attemptID
	selectedJob.UpdatedAt = now

	fromState := domain.StateQueued
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
// Invariants enforced atomically:
// 1. Job exists (ErrJobNotFound)
// 2. Job is not already terminal (ErrJobAlreadyTerminal)
// 3. Attempt matches job's active current attempt (ErrStaleAttempt)
// 4. Attempt exists and is RUNNING (ErrStaleAttempt)
// 5. SHA-256 hash of provided lease token matches stored hash in constant time (ErrStaleAttempt)
// 6. Reporting worker matches assigned worker (ErrWorkerMismatch)
func (r *InMemoryRepository) CommitAttempt(ctx context.Context, jobID, attemptID, leaseToken, workerID string, outcome domain.AttemptOutcome) (*domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, exists := r.jobs[jobID]
	if !exists {
		return nil, domain.ErrJobNotFound
	}
	if job.State.IsTerminal() {
		return nil, domain.ErrJobAlreadyTerminal
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

	now := time.Now().UTC()
	att.FinishedAt = &now
	att.ExitCode = outcome.ExitCode
	att.Output = outcome.Output
	att.Error = outcome.Error

	var targetState domain.JobState
	var eventType domain.JobEventType
	if outcome.ExitCode == 0 && outcome.Error == "" {
		att.State = domain.AttemptCompleted
		targetState = domain.StateSucceeded
		eventType = domain.JobEventCompleted
	} else {
		att.State = domain.AttemptFailed
		targetState = domain.StateFailed
		eventType = domain.JobEventFailed
	}

	fromState := job.State
	if err := job.TransitionTo(targetState); err != nil {
		return nil, err
	}
	job.Result = &domain.Result{
		ExitCode: outcome.ExitCode,
		Output:   outcome.Output,
		Error:    outcome.Error,
	}
	job.UpdatedAt = now

	r.recordEventLocked(&domain.JobEvent{
		JobID:     jobID,
		AttemptID: &attemptID,
		EventType: eventType,
		FromState: &fromState,
		ToState:   &targetState,
		CreatedAt: now,
		Details: map[string]any{
			"worker_id": workerID,
			"exit_code": outcome.ExitCode,
		},
	})

	copied := *job
	if job.Result != nil {
		res := *job.Result
		copied.Result = &res
	}
	return &copied, nil
}

// CancelJob atomically cancels a queued or running job and records an audit event.
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

	fromState := job.State
	var targetState domain.JobState
	var eventType domain.JobEventType
	if job.State == domain.StateQueued {
		targetState = domain.StateCancelled
		eventType = domain.JobEventCancelled
	} else if job.State == domain.StateRunning {
		targetState = domain.StateCancelRequested
		eventType = domain.JobEventCancelRequested
	} else {
		targetState = domain.StateCancelled
		eventType = domain.JobEventCancelled
	}

	if err := job.TransitionTo(targetState); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	job.UpdatedAt = now

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
