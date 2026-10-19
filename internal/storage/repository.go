package storage

import (
	"context"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// Repository defines the interface for persisting jobs, attempts, and workers.
// In Milestone 1, this is implemented in-memory. PostgreSQL will be introduced in Phase 3.
type Repository interface {
	// Job operations
	CreateJob(ctx context.Context, job *domain.Job) error
	GetJob(ctx context.Context, id string) (*domain.Job, error)
	UpdateJob(ctx context.Context, job *domain.Job) error
	ListQueuedJobs(ctx context.Context) ([]*domain.Job, error)

	// Attempt operations
	CreateAttempt(ctx context.Context, attempt *domain.Attempt) error
	GetAttempt(ctx context.Context, id string) (*domain.Attempt, error)
	UpdateAttempt(ctx context.Context, attempt *domain.Attempt) error
	ListAttemptsByJob(ctx context.Context, jobID string) ([]*domain.Attempt, error)

	// Worker operations
	UpsertWorker(ctx context.Context, worker *domain.Worker) error
	GetWorker(ctx context.Context, id string) (*domain.Worker, error)
	ListWorkers(ctx context.Context) ([]*domain.Worker, error)
}
