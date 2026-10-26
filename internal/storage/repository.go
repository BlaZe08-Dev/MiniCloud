package storage

import (
	"context"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// Repository defines the interface for persisting jobs, attempts, and workers.
// In Milestone 1-2, this is implemented in-memory. PostgreSQL implementation arrives in Phase 3.
type Repository interface {
	// Job operations
	CreateJob(ctx context.Context, job *domain.Job) error
	GetJob(ctx context.Context, id string) (*domain.Job, error)
	ListQueuedJobs(ctx context.Context) ([]*domain.Job, error)

	// Attempt operations
	GetAttempt(ctx context.Context, id string) (*domain.Attempt, error)
	ListAttemptsByJob(ctx context.Context, jobID string) ([]*domain.Attempt, error)

	// Worker operations
	UpsertWorker(ctx context.Context, worker *domain.Worker) error
	RecordHeartbeat(ctx context.Context, workerID string, availableSlots int) error
	GetWorker(ctx context.Context, id string) (*domain.Worker, error)
	ListWorkers(ctx context.Context) ([]*domain.Worker, error)
	ReapStaleWorkers(ctx context.Context, olderThan time.Duration) ([]string, error)

	// Atomic multi-entity lifecycle operations (Slice 3a / 3c / 3d)
	SubmitJob(ctx context.Context, job *domain.Job) error
	DispatchJob(ctx context.Context, jobID string, workerID string) (*domain.Attempt, error)
	DispatchNextJob(ctx context.Context, workerID string) (*domain.Job, *domain.Attempt, error)
	CommitAttempt(ctx context.Context, jobID, attemptID, leaseToken, workerID string, outcome domain.AttemptOutcome) (*domain.Job, error)
	CancelJob(ctx context.Context, jobID string) (*domain.Job, error)

	// Event operations
	ListEventsByJob(ctx context.Context, jobID string) ([]*domain.JobEvent, error)

	// Liveness and health checks
	Ping(ctx context.Context) error
}
