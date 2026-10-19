package storage

import (
	"context"
	"sync"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// InMemoryRepository is a thread-safe, in-memory implementation of the Repository interface.
//
// NOTE: This storage implementation is NON-DURABLE and TEMPORARY for Milestone 1.
// State will not survive process restarts. PostgreSQL durable persistence arrives in Phase 3.
type InMemoryRepository struct {
	mu       sync.RWMutex
	jobs     map[string]*domain.Job
	attempts map[string]*domain.Attempt
	workers  map[string]*domain.Worker
	jobOrder []string // Preserves FIFO insertion order for queued jobs
}

// NewInMemoryRepository constructs an initialized InMemoryRepository.
func NewInMemoryRepository() *InMemoryRepository {
	return &InMemoryRepository{
		jobs:     make(map[string]*domain.Job),
		attempts: make(map[string]*domain.Attempt),
		workers:  make(map[string]*domain.Worker),
		jobOrder: make([]string, 0),
	}
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

// CreateAttempt persists a new execution attempt.
func (r *InMemoryRepository) CreateAttempt(ctx context.Context, attempt *domain.Attempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	copied := *attempt
	r.attempts[attempt.ID] = &copied
	return nil
}

// GetAttempt retrieves an attempt by ID.
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
	r.workers[worker.ID] = &copied
	return nil
}

// GetWorker retrieves a worker by ID.
func (r *InMemoryRepository) GetWorker(ctx context.Context, id string) (*domain.Worker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	w, exists := r.workers[id]
	if !exists {
		return nil, domain.ErrWorkerNotFound
	}

	copied := *w
	return &copied, nil
}

// ListWorkers returns all registered workers.
func (r *InMemoryRepository) ListWorkers(ctx context.Context) ([]*domain.Worker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	workers := make([]*domain.Worker, 0, len(r.workers))
	for _, w := range r.workers {
		copied := *w
		workers = append(workers, &copied)
	}
	return workers, nil
}
