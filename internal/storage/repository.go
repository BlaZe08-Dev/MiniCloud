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
	// CommitAttempt records a worker's outcome for its RUNNING attempt. A success completes the
	// job. A failure is resolved by domain.ResolveFailure under the job lock: cancel wins on a
	// CANCEL_REQUESTED job, a PERMANENT class or an exhausted max_attempts budget fails the job, and
	// otherwise the job goes to RETRYING with not_before = now + backoff(attempt_no). backoff is
	// required for a failure that may retry (nil is rejected with ErrInvalidRequest, so no hidden
	// randomness is used) and is ignored otherwise. The error class is validated with
	// domain.NormalizeWorkerErrorClass; LEASE_EXPIRED is never accepted here.
	CommitAttempt(ctx context.Context, jobID, attemptID, leaseToken, workerID string, outcome domain.AttemptOutcome, backoff domain.BackoffFunc) (*domain.Job, error)
	CancelJob(ctx context.Context, jobID string) (*domain.Job, error)

	// Lease operations (Phase 4, ADR-0014). Time is read from the repository clock: database
	// time in PostgreSQL, the injected Clock in memory.
	//
	// RenewLease extends the lease of a RUNNING attempt to now + the configured lease duration,
	// under the symmetric accept-until-swept rule: a lease that has passed but not yet been swept
	// is still renewed. On a CANCEL_REQUESTED job it returns CancelRequested without extending the
	// lease. Errors follow the 409 evaluation order: ErrJobAlreadyTerminal, ErrLeaseExpired (the
	// attempt was swept to LOST), then ErrStaleAttempt (superseded, not RUNNING, wrong token),
	// then ErrWorkerMismatch. A renewal records no event.
	RenewLease(ctx context.Context, jobID, attemptID, leaseToken, workerID string) (*domain.LeaseRenewal, error)
	// SweepExpiredLeases closes RUNNING attempts whose lease has passed and resolves their jobs
	// (retry with backoff, fail, or cancel). It receives the caller-owned grace state and performs
	// the only authoritative grace check inside the row locks. A lock timeout or SKIP LOCKED skip
	// on one candidate is reported in the result, not as an error; only a returned error is a
	// failed tick.
	SweepExpiredLeases(ctx context.Context, params domain.SweepParams) (*domain.SweepResult, error)

	// Now returns the repository clock: database time in PostgreSQL, the injected Clock in
	// memory. The lease sweeper derives every grace instant from it so that grace state and the
	// in-lock grace check share one clock domain (owner decision Q-1).
	Now(ctx context.Context) (time.Time, error)

	// Node operations (ADR-0022, slice V2-1b). Time is the repository clock: database time in
	// PostgreSQL, the injected Clock in memory.
	//
	// RegisterNode registers or re-registers a node (ADR-0022 Section 8.1). It is idempotent per
	// node ID. The hardware content hash decides: no hardware row yet inserts version 1; the same
	// hash inserts nothing and refreshes display_name and last_seen_at only when the name changed
	// or last_seen_at is at least minInterval old; a different hash is limited by
	// domain.HardwareUpdateLimit (a rejection returns *domain.NodeRateLimitedError, matches
	// domain.ErrNodeRateLimited, and writes nothing) and otherwise inserts version + 1 and prunes
	// to the newest domain.MaxHardwareHistory rows in the same transaction. An invalid
	// registration returns domain.ErrInvalidRequest and writes nothing.
	RegisterNode(ctx context.Context, reg domain.NodeRegistration, minInterval time.Duration) (*domain.NodeRegistrationResult, error)
	// RecordNodeTelemetry stores the latest telemetry sample of a registered node. An unknown node
	// returns domain.ErrNodeNotFound (never ErrTelemetryOutOfOrder). A report of the same session
	// whose seq is not greater than the stored one returns domain.ErrTelemetryOutOfOrder and
	// changes nothing, received_at included (ADR-0022 Section 3.3).
	RecordNodeTelemetry(ctx context.Context, nodeID string, report domain.NodeTelemetryReport) error
	// GetNode returns the read model of one node, or domain.ErrNodeNotFound.
	GetNode(ctx context.Context, nodeID string) (*domain.NodeView, error)
	// ListNodes returns every node ordered by ID.
	ListNodes(ctx context.Context) ([]*domain.NodeView, error)

	// Event operations
	ListEventsByJob(ctx context.Context, jobID string) ([]*domain.JobEvent, error)

	// Liveness and health checks
	Ping(ctx context.Context) error
}
