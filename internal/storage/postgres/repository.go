package postgres

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ensure Repository satisfies storage.Repository at compile time.
var _ storage.Repository = (*Repository)(nil)

// Repository implements storage.Repository backed by PostgreSQL.
//
// DISTRIBUTED LOCKING ORDER INVARIANT:
// To guarantee mathematical deadlock prevention across all concurrent multi-table transactions,
// row-level locks (SELECT ... FOR UPDATE) MUST ALWAYS be acquired in this strict order:
//  1. jobs
//  2. workers
//  3. attempts
//  4. results (insert-only)
//  5. job_events (insert-only)
//
// Never acquire locks in reverse or mixed order (e.g., workers before jobs).
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository constructs a PostgreSQL repository wrapping a connection pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// CreateJob persists a new job directly without audit events.
func (r *Repository) CreateJob(ctx context.Context, job *domain.Job) error {
	if job == nil || job.ID == "" {
		return domain.ErrInvalidRequest
	}

	query := `
		INSERT INTO jobs (id, task_type, payload, state, max_attempts, timeout_seconds, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 
			COALESCE(NULLIF($7, '0001-01-01 00:00:00+00'::timestamptz), CURRENT_TIMESTAMP),
			COALESCE(NULLIF($8, '0001-01-01 00:00:00+00'::timestamptz), CURRENT_TIMESTAMP)
		);
	`
	_, err := r.pool.Exec(ctx, query,
		job.ID,
		job.TaskType,
		job.Payload,
		string(job.State),
		job.MaxAttempts,
		job.TimeoutSeconds,
		job.CreatedAt,
		job.UpdatedAt,
	)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

// GetJob retrieves a job by ID, including its associated terminal result if one exists.
func (r *Repository) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	query := `
		SELECT id, task_type, payload, state, max_attempts, timeout_seconds, current_attempt_id, created_at, updated_at
		FROM jobs
		WHERE id = $1;
	`
	var (
		job              domain.Job
		stateStr         string
		currentAttemptID *string
	)
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&job.ID,
		&job.TaskType,
		&job.Payload,
		&stateStr,
		&job.MaxAttempts,
		&job.TimeoutSeconds,
		&currentAttemptID,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrJobNotFound
		}
		return nil, mapPgError(err)
	}
	job.State = domain.JobState(stateStr)
	if currentAttemptID != nil {
		job.CurrentAttemptID = *currentAttemptID
	}

	// Fetch terminal result if present
	resQuery := `
		SELECT attempt_id, exit_code, output, error, committed_at
		FROM results
		WHERE job_id = $1;
	`
	var res domain.Result
	var resAttemptID string
	var committedAt time.Time
	err = r.pool.QueryRow(ctx, resQuery, id).Scan(
		&resAttemptID,
		&res.ExitCode,
		&res.Output,
		&res.Error,
		&committedAt,
	)
	if err == nil {
		job.Result = &res
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, mapPgError(err)
	}

	return &job, nil
}

// ListQueuedJobs returns all jobs currently in StateQueued in FIFO order by creation time.
func (r *Repository) ListQueuedJobs(ctx context.Context) ([]*domain.Job, error) {
	query := `
		SELECT id, task_type, payload, state, max_attempts, timeout_seconds, current_attempt_id, created_at, updated_at
		FROM jobs
		WHERE state = 'QUEUED'
		ORDER BY created_at ASC;
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	var jobs []*domain.Job
	for rows.Next() {
		var (
			job              domain.Job
			stateStr         string
			currentAttemptID *string
		)
		err := rows.Scan(
			&job.ID,
			&job.TaskType,
			&job.Payload,
			&stateStr,
			&job.MaxAttempts,
			&job.TimeoutSeconds,
			&currentAttemptID,
			&job.CreatedAt,
			&job.UpdatedAt,
		)
		if err != nil {
			return nil, mapPgError(err)
		}
		job.State = domain.JobState(stateStr)
		if currentAttemptID != nil {
			job.CurrentAttemptID = *currentAttemptID
		}
		jobs = append(jobs, &job)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return jobs, nil
}

// GetAttempt retrieves an attempt by ID with the stored hash (raw token is empty).
func (r *Repository) GetAttempt(ctx context.Context, id string) (*domain.Attempt, error) {
	query := `
		SELECT id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at, finished_at, exit_code, output, error
		FROM attempts
		WHERE id = $1;
	`
	var (
		att        domain.Attempt
		stateStr   string
		finishedAt *time.Time
		exitCode   *int
		output     *string
		errSummary *string
	)
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&att.ID,
		&att.JobID,
		&att.AttemptNo,
		&att.WorkerID,
		&att.LeaseTokenHash,
		&stateStr,
		&att.StartedAt,
		&finishedAt,
		&exitCode,
		&output,
		&errSummary,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrAttemptNotFound
		}
		return nil, mapPgError(err)
	}

	att.State = domain.AttemptState(stateStr)
	att.FinishedAt = finishedAt
	if exitCode != nil {
		att.ExitCode = *exitCode
	}
	if output != nil {
		att.Output = *output
	}
	if errSummary != nil {
		att.Error = *errSummary
	}
	return &att, nil
}

// ListAttemptsByJob returns all attempts for a job ordered monotonically by attempt number.
func (r *Repository) ListAttemptsByJob(ctx context.Context, jobID string) ([]*domain.Attempt, error) {
	query := `
		SELECT id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at, finished_at, exit_code, output, error
		FROM attempts
		WHERE job_id = $1
		ORDER BY attempt_no ASC;
	`
	rows, err := r.pool.Query(ctx, query, jobID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	var attempts []*domain.Attempt
	for rows.Next() {
		var (
			att        domain.Attempt
			stateStr   string
			finishedAt *time.Time
			exitCode   *int
			output     *string
			errSummary *string
		)
		err := rows.Scan(
			&att.ID,
			&att.JobID,
			&att.AttemptNo,
			&att.WorkerID,
			&att.LeaseTokenHash,
			&stateStr,
			&att.StartedAt,
			&finishedAt,
			&exitCode,
			&output,
			&errSummary,
		)
		if err != nil {
			return nil, mapPgError(err)
		}
		att.State = domain.AttemptState(stateStr)
		att.FinishedAt = finishedAt
		if exitCode != nil {
			att.ExitCode = *exitCode
		}
		if output != nil {
			att.Output = *output
		}
		if errSummary != nil {
			att.Error = *errSummary
		}
		attempts = append(attempts, &att)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return attempts, nil
}

// UpsertWorker registers or updates a worker. AvailableSlots is dynamically derived in worker_capacity.
func (r *Repository) UpsertWorker(ctx context.Context, worker *domain.Worker) error {
	if worker == nil || worker.ID == "" {
		return domain.ErrInvalidRequest
	}

	status := worker.Status
	if status == "" {
		status = domain.WorkerStatusHealthy
	}

	query := `
		INSERT INTO workers (id, version, total_slots, status, supported_tasks, registered_at, last_heartbeat_at)
		VALUES ($1, $2, $3, $4, $5, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT (id) DO UPDATE 
		SET version = EXCLUDED.version,
			total_slots = EXCLUDED.total_slots,
			status = EXCLUDED.status,
			supported_tasks = EXCLUDED.supported_tasks,
			last_heartbeat_at = CURRENT_TIMESTAMP;
	`
	_, err := r.pool.Exec(ctx, query,
		worker.ID,
		worker.Version,
		worker.TotalSlots,
		string(status),
		worker.SupportedTasks,
	)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

// RecordHeartbeat updates a worker's last_heartbeat_at to CURRENT_TIMESTAMP and restores HEALTHY status.
func (r *Repository) RecordHeartbeat(ctx context.Context, workerID string, _ int) error {
	query := `
		UPDATE workers
		SET last_heartbeat_at = CURRENT_TIMESTAMP, status = 'HEALTHY'
		WHERE id = $1;
	`
	tag, err := r.pool.Exec(ctx, query, workerID)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrWorkerNotFound
	}
	return nil
}

// GetWorker retrieves a worker by ID with AvailableSlots computed from active attempts.
func (r *Repository) GetWorker(ctx context.Context, id string) (*domain.Worker, error) {
	query := `
		SELECT id, version, total_slots, status, supported_tasks, registered_at, last_heartbeat_at, available_slots
		FROM worker_capacity
		WHERE id = $1;
	`
	var (
		w         domain.Worker
		statusStr string
	)
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&w.ID,
		&w.Version,
		&w.TotalSlots,
		&statusStr,
		&w.SupportedTasks,
		&w.RegisteredAt,
		&w.LastHeartbeatAt,
		&w.AvailableSlots,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrWorkerNotFound
		}
		return nil, mapPgError(err)
	}
	w.Status = domain.WorkerStatus(statusStr)
	return &w, nil
}

// ListWorkers returns all workers with AvailableSlots derived from active attempts.
func (r *Repository) ListWorkers(ctx context.Context) ([]*domain.Worker, error) {
	query := `
		SELECT id, version, total_slots, status, supported_tasks, registered_at, last_heartbeat_at, available_slots
		FROM worker_capacity
		ORDER BY id ASC;
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	var workers []*domain.Worker
	for rows.Next() {
		var (
			w         domain.Worker
			statusStr string
		)
		err := rows.Scan(
			&w.ID,
			&w.Version,
			&w.TotalSlots,
			&statusStr,
			&w.SupportedTasks,
			&w.RegisteredAt,
			&w.LastHeartbeatAt,
			&w.AvailableSlots,
		)
		if err != nil {
			return nil, mapPgError(err)
		}
		w.Status = domain.WorkerStatus(statusStr)
		workers = append(workers, &w)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return workers, nil
}

// ReapStaleWorkers transitions HEALTHY workers whose last_heartbeat_at is older than olderThan
// to UNHEALTHY using database time. Returns reaped worker IDs.
func (r *Repository) ReapStaleWorkers(ctx context.Context, olderThan time.Duration) ([]string, error) {
	query := `
		UPDATE workers
		SET status = 'UNHEALTHY'
		WHERE status = 'HEALTHY'
		  AND last_heartbeat_at < (clock_timestamp() - make_interval(secs => $1::double precision))
		RETURNING id;
	`
	rows, err := r.pool.Query(ctx, query, olderThan.Seconds())
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	var reaped []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, mapPgError(err)
		}
		reaped = append(reaped, id)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return reaped, nil
}

// SubmitJob atomically inserts a job and records its initial lifecycle audit events.
func (r *Repository) SubmitJob(ctx context.Context, job *domain.Job) error {
	if job == nil || job.ID == "" {
		return domain.ErrInvalidRequest
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapPgError(err)
	}
	defer tx.Rollback(ctx)

	jobQuery := `
		INSERT INTO jobs (id, task_type, payload, state, max_attempts, timeout_seconds, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6,
			COALESCE(NULLIF($7, '0001-01-01 00:00:00+00'::timestamptz), CURRENT_TIMESTAMP),
			COALESCE(NULLIF($8, '0001-01-01 00:00:00+00'::timestamptz), CURRENT_TIMESTAMP)
		);
	`
	_, err = tx.Exec(ctx, jobQuery,
		job.ID,
		job.TaskType,
		job.Payload,
		string(job.State),
		job.MaxAttempts,
		job.TimeoutSeconds,
		job.CreatedAt,
		job.UpdatedAt,
	)
	if err != nil {
		return mapPgError(err)
	}

	// Record EventJobSubmitted
	submittedDetails, _ := json.Marshal(map[string]any{
		"task_type": job.TaskType,
		"state":     string(job.State),
	})
	eventQuery := `
		INSERT INTO job_events (job_id, event_type, details, created_at)
		VALUES ($1, $2, $3, clock_timestamp());
	`
	_, err = tx.Exec(ctx, eventQuery, job.ID, domain.JobEventSubmitted, submittedDetails)
	if err != nil {
		return mapPgError(err)
	}

	// Record EventJobQueued if queued
	if job.State == domain.StateQueued {
		_, err = tx.Exec(ctx, eventQuery, job.ID, domain.JobEventQueued, []byte("{}"))
		if err != nil {
			return mapPgError(err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return mapPgError(err)
	}
	return nil
}

// DispatchJob atomically locks the job and worker rows in strict order (jobs -> workers -> attempts),
// checks dispatch preconditions in exact precedence, generates a secure lease token, transitions the job
// to RUNNING, and returns the attempt with the raw lease token populated once.
//
// Precondition Error Precedence (strictly matching InMemoryRepository):
//  1. Job existence (ErrJobNotFound)
//  2. Job state == QUEUED (ErrJobNotQueued)
//  3. Worker existence (ErrWorkerNotFound)
//  4. Worker status == HEALTHY (ErrWorkerUnhealthy)
//  5. Worker supports task type (ErrWorkerTaskUnsupported)
//  6. Worker has capacity derived from active attempts (ErrWorkerNoCapacity)
//  7. At most one active attempt (ErrInvalidRequest)
func (r *Repository) DispatchJob(ctx context.Context, jobID string, workerID string) (*domain.Attempt, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer tx.Rollback(ctx)

	// Step 1: Lock Job row FOR UPDATE
	jobLockQuery := `
		SELECT task_type, state
		FROM jobs
		WHERE id = $1
		FOR UPDATE;
	`
	var taskType string
	var jobState string
	err = tx.QueryRow(ctx, jobLockQuery, jobID).Scan(&taskType, &jobState)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrJobNotFound
		}
		return nil, mapPgError(err)
	}
	if jobState != string(domain.StateQueued) {
		return nil, domain.ErrJobNotQueued
	}

	// Step 2: Lock Worker row FOR UPDATE
	workerLockQuery := `
		SELECT total_slots, status, supported_tasks
		FROM workers
		WHERE id = $1
		FOR UPDATE;
	`
	var (
		totalSlots     int
		workerStatus   string
		supportedTasks []string
	)
	err = tx.QueryRow(ctx, workerLockQuery, workerID).Scan(&totalSlots, &workerStatus, &supportedTasks)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrWorkerNotFound
		}
		return nil, mapPgError(err)
	}
	if workerStatus != string(domain.WorkerStatusHealthy) {
		return nil, domain.ErrWorkerUnhealthy
	}

	// Step 3: Check task support
	taskSupported := false
	for _, st := range supportedTasks {
		if st == taskType {
			taskSupported = true
			break
		}
	}
	if !taskSupported {
		return nil, domain.ErrWorkerTaskUnsupported
	}

	// Step 4: Check worker capacity from active attempts (PENDING or RUNNING)
	var activeWorkerAttempts int
	capQuery := `
		SELECT COUNT(*)
		FROM attempts
		WHERE worker_id = $1 AND state IN ('PENDING', 'RUNNING');
	`
	if err := tx.QueryRow(ctx, capQuery, workerID).Scan(&activeWorkerAttempts); err != nil {
		return nil, mapPgError(err)
	}
	if totalSlots-activeWorkerAttempts <= 0 {
		return nil, domain.ErrWorkerNoCapacity
	}

	// Step 5: Check invariant: at most one active attempt for this job
	var activeJobAttempts int
	jobAttQuery := `
		SELECT COUNT(*)
		FROM attempts
		WHERE job_id = $1 AND state IN ('PENDING', 'RUNNING');
	`
	if err := tx.QueryRow(ctx, jobAttQuery, jobID).Scan(&activeJobAttempts); err != nil {
		return nil, mapPgError(err)
	}
	if activeJobAttempts > 0 {
		return nil, domain.ErrInvalidRequest
	}

	// Step 6: Derive monotonic attempt number
	var nextAttemptNo int
	attNoQuery := `
		SELECT COALESCE(MAX(attempt_no), 0) + 1
		FROM attempts
		WHERE job_id = $1;
	`
	if err := tx.QueryRow(ctx, attNoQuery, jobID).Scan(&nextAttemptNo); err != nil {
		return nil, mapPgError(err)
	}

	// Step 7: Generate cryptographic lease token and attempt ID
	rawLeaseToken, err := domain.NewLeaseToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate secure lease token: %w", err)
	}
	tokenHash := domain.HashLeaseToken(rawLeaseToken)
	attemptID := fmt.Sprintf("att_%s_%d", jobID, nextAttemptNo)

	// Step 8: Insert attempt row
	insertAttemptQuery := `
		INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at)
		VALUES ($1, $2, $3, $4, $5, 'RUNNING', CURRENT_TIMESTAMP)
		RETURNING started_at;
	`
	var startedAt time.Time
	err = tx.QueryRow(ctx, insertAttemptQuery,
		attemptID,
		jobID,
		nextAttemptNo,
		workerID,
		tokenHash,
	).Scan(&startedAt)
	if err != nil {
		return nil, mapPgError(err)
	}

	// Step 9: Transition job to RUNNING
	updateJobQuery := `
		UPDATE jobs
		SET state = 'RUNNING', current_attempt_id = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1;
	`
	if _, err := tx.Exec(ctx, updateJobQuery, jobID, attemptID); err != nil {
		return nil, mapPgError(err)
	}

	// Step 10: Record EventJobDispatched audit event with honest FIFO pull details
	dispatchDetails, _ := json.Marshal(map[string]any{
		"worker_id":  workerID,
		"attempt_no": nextAttemptNo,
		"policy":     "fifo_pull",
		"reason":     "claimed by polling worker with capacity",
	})
	fromState := "QUEUED"
	toState := "RUNNING"
	insertEventQuery := `
		INSERT INTO job_events (job_id, attempt_id, event_type, from_state, to_state, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, clock_timestamp());
	`
	_, err = tx.Exec(ctx, insertEventQuery,
		jobID,
		attemptID,
		domain.JobEventDispatched,
		fromState,
		toState,
		dispatchDetails,
	)
	if err != nil {
		return nil, mapPgError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, mapPgError(err)
	}

	// Return attempt to worker with raw token populated once
	retAttempt := domain.Attempt{
		ID:             attemptID,
		JobID:          jobID,
		AttemptNo:      nextAttemptNo,
		WorkerID:       workerID,
		LeaseToken:     rawLeaseToken,
		LeaseTokenHash: tokenHash,
		State:          domain.AttemptRunning,
		StartedAt:      startedAt,
	}
	return &retAttempt, nil
}

// DispatchNextJob atomically selects the next eligible QUEUED job matching the worker's supported tasks,
// locks it and the worker row following strict deadlock-free lock ordering (jobs -> workers -> attempts),
// transitions the job to RUNNING, creates an attempt, and records an audit event.
//
// Deadlock Prevention Lock Order:
//  1. Unlocked pre-check of worker (worker_capacity) to preserve error precedence (SEC-002) without taking locks.
//  2. Begin transaction and lock candidate job row FOR UPDATE SKIP LOCKED.
//  3. Lock worker row FOR UPDATE (jobs row is already locked, preserving jobs -> workers hierarchy).
//  4. Re-check worker health and capacity under lock. If recheck fails, transaction rolls back.
//  5. Insert attempt and update job.
//
// Precondition Error Precedence (strictly matching InMemoryRepository):
//  1. Worker existence (ErrWorkerNotFound)
//  2. Worker status == HEALTHY (ErrWorkerUnhealthy)
//  3. Worker has available capacity (ErrWorkerNoCapacity)
//  4. Eligible job exists (ErrNoQueuedJobs)
func (r *Repository) DispatchNextJob(ctx context.Context, workerID string) (*domain.Job, *domain.Attempt, error) {
	// Step 1: Unlocked pre-check of worker to preserve error precedence (SEC-002)
	const workerPreCheckQuery = `
		SELECT status, available_slots, supported_tasks
		FROM worker_capacity
		WHERE id = $1;
	`
	var (
		statusStr      string
		availableSlots int
		supportedTasks []string
	)
	err := r.pool.QueryRow(ctx, workerPreCheckQuery, workerID).Scan(
		&statusStr,
		&availableSlots,
		&supportedTasks,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, domain.ErrWorkerNotFound
		}
		return nil, nil, mapPgError(err)
	}
	if statusStr != string(domain.WorkerStatusHealthy) {
		return nil, nil, domain.ErrWorkerUnhealthy
	}
	if availableSlots <= 0 {
		return nil, nil, domain.ErrWorkerNoCapacity
	}

	// Step 2: Begin transaction
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, mapPgError(err)
	}
	defer tx.Rollback(ctx)

	// Step 3: SELECT next eligible QUEUED job FOR UPDATE SKIP LOCKED
	// Lock candidate job row FIRST to adhere to jobs -> workers lock order.
	const selectJobQuery = `
		SELECT id, task_type, payload, state, max_attempts, timeout_seconds, created_at, updated_at
		FROM jobs
		WHERE state = 'QUEUED'
		  AND task_type = ANY($1::text[])
		ORDER BY created_at ASC, id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED;
	`
	var (
		job      domain.Job
		jobState string
	)
	err = tx.QueryRow(ctx, selectJobQuery, supportedTasks).Scan(
		&job.ID,
		&job.TaskType,
		&job.Payload,
		&jobState,
		&job.MaxAttempts,
		&job.TimeoutSeconds,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, domain.ErrNoQueuedJobs
		}
		return nil, nil, mapPgError(err)
	}
	job.State = domain.JobState(jobState)

	// Step 4: Lock Worker row FOR UPDATE (jobs is already locked)
	const workerLockQuery = `
		SELECT total_slots, status, supported_tasks
		FROM workers
		WHERE id = $1
		FOR UPDATE;
	`
	var (
		totalSlots           int
		workerStatus         string
		lockedSupportedTasks []string
	)
	err = tx.QueryRow(ctx, workerLockQuery, workerID).Scan(&totalSlots, &workerStatus, &lockedSupportedTasks)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, domain.ErrWorkerNotFound
		}
		return nil, nil, mapPgError(err)
	}
	if workerStatus != string(domain.WorkerStatusHealthy) {
		return nil, nil, domain.ErrWorkerUnhealthy
	}

	// Re-verify that worker supports this candidate job's task type
	taskSupported := false
	for _, st := range lockedSupportedTasks {
		if st == job.TaskType {
			taskSupported = true
			break
		}
	}
	if !taskSupported {
		return nil, nil, domain.ErrWorkerTaskUnsupported
	}

	// Step 5: Re-check worker capacity under lock
	var activeWorkerAttempts int
	const capQuery = `
		SELECT COUNT(*)
		FROM attempts
		WHERE worker_id = $1 AND state IN ('PENDING', 'RUNNING');
	`
	if err := tx.QueryRow(ctx, capQuery, workerID).Scan(&activeWorkerAttempts); err != nil {
		return nil, nil, mapPgError(err)
	}
	if totalSlots-activeWorkerAttempts <= 0 {
		return nil, nil, domain.ErrWorkerNoCapacity
	}

	// Step 6: Check invariant: at most one active attempt for this job
	var activeJobAttempts int
	const jobAttQuery = `
		SELECT COUNT(*)
		FROM attempts
		WHERE job_id = $1 AND state IN ('PENDING', 'RUNNING');
	`
	if err := tx.QueryRow(ctx, jobAttQuery, job.ID).Scan(&activeJobAttempts); err != nil {
		return nil, nil, mapPgError(err)
	}
	if activeJobAttempts > 0 {
		return nil, nil, domain.ErrInvalidRequest
	}

	// Step 7: Derive monotonic attempt number (max existing attempt_no + 1)
	var nextAttemptNo int
	const attNoQuery = `
		SELECT COALESCE(MAX(attempt_no), 0) + 1
		FROM attempts
		WHERE job_id = $1;
	`
	if err := tx.QueryRow(ctx, attNoQuery, job.ID).Scan(&nextAttemptNo); err != nil {
		return nil, nil, mapPgError(err)
	}

	// Step 8: Generate cryptographic lease token and attempt ID
	rawLeaseToken, err := domain.NewLeaseToken()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate secure lease token: %w", err)
	}
	tokenHash := domain.HashLeaseToken(rawLeaseToken)
	attemptID := fmt.Sprintf("att_%s_%d", job.ID, nextAttemptNo)

	// Step 9: Insert attempt row
	const insertAttemptQuery = `
		INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at)
		VALUES ($1, $2, $3, $4, $5, 'RUNNING', clock_timestamp())
		RETURNING started_at;
	`
	var startedAt time.Time
	err = tx.QueryRow(ctx, insertAttemptQuery,
		attemptID,
		job.ID,
		nextAttemptNo,
		workerID,
		tokenHash,
	).Scan(&startedAt)
	if err != nil {
		return nil, nil, mapPgError(err)
	}

	// Step 10: Transition job to RUNNING
	const updateJobQuery = `
		UPDATE jobs
		SET state = 'RUNNING', current_attempt_id = $2, updated_at = clock_timestamp()
		WHERE id = $1;
	`
	if _, err := tx.Exec(ctx, updateJobQuery, job.ID, attemptID); err != nil {
		return nil, nil, mapPgError(err)
	}

	// Step 11: Record EventJobDispatched audit event with honest FIFO pull details
	dispatchDetails, _ := json.Marshal(map[string]any{
		"worker_id":  workerID,
		"attempt_no": nextAttemptNo,
		"policy":     "fifo_pull",
		"reason":     "claimed by polling worker with capacity",
	})
	fromState := "QUEUED"
	toState := "RUNNING"
	const insertEventQuery = `
		INSERT INTO job_events (job_id, attempt_id, event_type, from_state, to_state, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, clock_timestamp());
	`
	_, err = tx.Exec(ctx, insertEventQuery,
		job.ID,
		attemptID,
		domain.JobEventDispatched,
		fromState,
		toState,
		dispatchDetails,
	)
	if err != nil {
		return nil, nil, mapPgError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, mapPgError(err)
	}

	job.State = domain.StateRunning
	job.CurrentAttemptID = attemptID
	job.UpdatedAt = startedAt

	attempt := domain.Attempt{
		ID:             attemptID,
		JobID:          job.ID,
		AttemptNo:      nextAttemptNo,
		WorkerID:       workerID,
		LeaseToken:     rawLeaseToken,
		LeaseTokenHash: tokenHash,
		State:          domain.AttemptRunning,
		StartedAt:      startedAt,
	}
	return &job, &attempt, nil
}

// CommitAttempt validates the commit, updates the attempt, sets the job terminal state,
// inserts the terminal result, and logs the audit events, all in a single transaction.
func (r *Repository) CommitAttempt(ctx context.Context, jobID, attemptID, leaseToken, workerID string, outcome domain.AttemptOutcome) (*domain.Job, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer tx.Rollback(ctx)

	// Step 1: Lock Job row FOR UPDATE
	jobQuery := `
		SELECT id, task_type, payload, state, max_attempts, timeout_seconds, current_attempt_id, created_at, updated_at
		FROM jobs
		WHERE id = $1
		FOR UPDATE;
	`
	var (
		job              domain.Job
		jobStateStr      string
		currentAttemptID *string
	)
	err = tx.QueryRow(ctx, jobQuery, jobID).Scan(
		&job.ID,
		&job.TaskType,
		&job.Payload,
		&jobStateStr,
		&job.MaxAttempts,
		&job.TimeoutSeconds,
		&currentAttemptID,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrJobNotFound
		}
		return nil, mapPgError(err)
	}
	job.State = domain.JobState(jobStateStr)
	if job.State.IsTerminal() {
		return nil, domain.ErrJobAlreadyTerminal
	}
	if currentAttemptID == nil || *currentAttemptID != attemptID {
		return nil, domain.ErrStaleAttempt
	}
	job.CurrentAttemptID = *currentAttemptID

	// Step 2: Lock Attempt row FOR UPDATE
	attemptQuery := `
		SELECT id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at
		FROM attempts
		WHERE id = $1
		FOR UPDATE;
	`
	var (
		attID          string
		attJobID       string
		attNo          int
		attWorkerID    string
		leaseTokenHash string
		attStateStr    string
		startedAt      time.Time
	)
	err = tx.QueryRow(ctx, attemptQuery, attemptID).Scan(
		&attID,
		&attJobID,
		&attNo,
		&attWorkerID,
		&leaseTokenHash,
		&attStateStr,
		&startedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrAttemptNotFound
		}
		return nil, mapPgError(err)
	}

	if attStateStr != string(domain.AttemptRunning) {
		return nil, domain.ErrStaleAttempt
	}

	// Constant-time hash comparison in Go to prevent timing leaks
	providedHash := domain.HashLeaseToken(leaseToken)
	if subtle.ConstantTimeCompare([]byte(leaseTokenHash), []byte(providedHash)) != 1 {
		return nil, domain.ErrStaleAttempt
	}

	if attWorkerID != workerID {
		return nil, &domain.WorkerMismatchError{
			AssignedWorker:  attWorkerID,
			ReportingWorker: workerID,
		}
	}

	// Step 3: Determine outcomes
	var (
		targetAttemptState string
		targetJobState     domain.JobState
		jobEventType       domain.JobEventType
	)
	if outcome.ExitCode == 0 && outcome.Error == "" {
		targetAttemptState = string(domain.AttemptCompleted)
		targetJobState = domain.StateSucceeded
		jobEventType = domain.JobEventCompleted
	} else {
		targetAttemptState = string(domain.AttemptFailed)
		targetJobState = domain.StateFailed
		jobEventType = domain.JobEventFailed
	}

	// Step 4: Update attempt
	updateAttemptQuery := `
		UPDATE attempts
		SET state = $1, finished_at = CURRENT_TIMESTAMP, exit_code = $2, output = $3, error = $4
		WHERE id = $5;
	`
	_, err = tx.Exec(ctx, updateAttemptQuery,
		targetAttemptState,
		outcome.ExitCode,
		outcome.Output,
		outcome.Error,
		attemptID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}

	// Step 5: Update job
	fromState := string(job.State)
	toState := string(targetJobState)
	updateJobQuery := `
		UPDATE jobs
		SET state = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
		RETURNING updated_at;
	`
	var updatedAt time.Time
	err = tx.QueryRow(ctx, updateJobQuery, toState, jobID).Scan(&updatedAt)
	if err != nil {
		return nil, mapPgError(err)
	}
	job.State = targetJobState
	job.UpdatedAt = updatedAt

	// Step 6: Insert into results table
	insertResultQuery := `
		INSERT INTO results (job_id, attempt_id, exit_code, output, error, committed_at)
		VALUES ($1, $2, $3, $4, $5, CURRENT_TIMESTAMP);
	`
	_, err = tx.Exec(ctx, insertResultQuery,
		jobID,
		attemptID,
		outcome.ExitCode,
		outcome.Output,
		outcome.Error,
	)
	if err != nil {
		return nil, mapPgError(err)
	}

	job.Result = &domain.Result{
		ExitCode: outcome.ExitCode,
		Output:   outcome.Output,
		Error:    outcome.Error,
	}

	// Step 7: Record audit event
	insertEventQuery := `
		INSERT INTO job_events (job_id, attempt_id, event_type, from_state, to_state, created_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp());
	`
	_, err = tx.Exec(ctx, insertEventQuery,
		jobID,
		attemptID,
		jobEventType,
		fromState,
		toState,
	)
	if err != nil {
		return nil, mapPgError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, mapPgError(err)
	}

	return &job, nil
}

// CancelJob atomically cancels a queued or running job and logs the corresponding audit event.
func (r *Repository) CancelJob(ctx context.Context, jobID string) (*domain.Job, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer tx.Rollback(ctx)

	jobQuery := `
		SELECT id, task_type, payload, state, max_attempts, timeout_seconds, current_attempt_id, created_at, updated_at
		FROM jobs
		WHERE id = $1
		FOR UPDATE;
	`
	var (
		job              domain.Job
		jobStateStr      string
		currentAttemptID *string
	)
	err = tx.QueryRow(ctx, jobQuery, jobID).Scan(
		&job.ID,
		&job.TaskType,
		&job.Payload,
		&jobStateStr,
		&job.MaxAttempts,
		&job.TimeoutSeconds,
		&currentAttemptID,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrJobNotFound
		}
		return nil, mapPgError(err)
	}
	job.State = domain.JobState(jobStateStr)
	if currentAttemptID != nil {
		job.CurrentAttemptID = *currentAttemptID
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

	updateJobQuery := `
		UPDATE jobs
		SET state = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
		RETURNING updated_at;
	`
	var updatedAt time.Time
	err = tx.QueryRow(ctx, updateJobQuery, string(targetState), jobID).Scan(&updatedAt)
	if err != nil {
		return nil, mapPgError(err)
	}
	job.State = targetState
	job.UpdatedAt = updatedAt

	insertEventQuery := `
		INSERT INTO job_events (job_id, attempt_id, event_type, from_state, to_state, created_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp());
	`
	var attemptIDParam *string
	if job.CurrentAttemptID != "" {
		attemptIDParam = &job.CurrentAttemptID
	}
	_, err = tx.Exec(ctx, insertEventQuery,
		jobID,
		attemptIDParam,
		eventType,
		string(fromState),
		string(targetState),
	)
	if err != nil {
		return nil, mapPgError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, mapPgError(err)
	}

	return &job, nil
}

// ListEventsByJob retrieves all audit events for a job in chronological order.
func (r *Repository) ListEventsByJob(ctx context.Context, jobID string) ([]*domain.JobEvent, error) {
	query := `
		SELECT id, job_id, attempt_id, event_type, from_state, to_state, details, created_at
		FROM job_events
		WHERE job_id = $1
		ORDER BY id ASC;
	`
	rows, err := r.pool.Query(ctx, query, jobID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	var events []*domain.JobEvent
	for rows.Next() {
		var (
			event        domain.JobEvent
			eventTypeStr string
			fromStateStr *string
			toStateStr   *string
			detailsRaw   []byte
		)
		err := rows.Scan(
			&event.ID,
			&event.JobID,
			&event.AttemptID,
			&eventTypeStr,
			&fromStateStr,
			&toStateStr,
			&detailsRaw,
			&event.CreatedAt,
		)
		if err != nil {
			return nil, mapPgError(err)
		}
		event.EventType = domain.JobEventType(eventTypeStr)
		if fromStateStr != nil {
			fs := domain.JobState(*fromStateStr)
			event.FromState = &fs
		}
		if toStateStr != nil {
			ts := domain.JobState(*toStateStr)
			event.ToState = &ts
		}
		if len(detailsRaw) > 0 {
			var details map[string]any
			if err := json.Unmarshal(detailsRaw, &details); err == nil {
				event.Details = details
			}
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return events, nil
}

// mapPgError maps PostgreSQL SQLSTATE and constraint codes to typed domain errors.
func mapPgError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrJobNotFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			if strings.Contains(pgErr.ConstraintName, "results_pkey") {
				return domain.ErrJobAlreadyTerminal
			}
			if strings.Contains(pgErr.ConstraintName, "idx_attempts_active_per_job") {
				return domain.ErrInvalidRequest
			}
			if strings.Contains(pgErr.ConstraintName, "jobs_pkey") {
				return domain.ErrInvalidRequest
			}
			return domain.ErrInvalidRequest
		case "23503": // foreign_key_violation
			if strings.Contains(pgErr.ConstraintName, "attempts_job_id_fkey") {
				return domain.ErrJobNotFound
			}
			if strings.Contains(pgErr.ConstraintName, "attempts_worker_id_fkey") {
				return domain.ErrWorkerNotFound
			}
			return domain.ErrInvalidRequest
		case "23514": // check_violation
			if strings.Contains(pgErr.ConstraintName, "chk_jobs_payload_size") {
				return domain.ErrPayloadTooLarge
			}
			if strings.Contains(pgErr.ConstraintName, "chk_attempts_output_size") {
				return domain.ErrOutputTooLarge
			}
			if strings.Contains(pgErr.ConstraintName, "chk_jobs_max_attempts") {
				return fmt.Errorf("%w: max_attempts must be between 1 and 20", domain.ErrInvalidRequest)
			}
			if strings.Contains(pgErr.ConstraintName, "chk_jobs_timeout") {
				return fmt.Errorf("%w: timeout_seconds must be between 1 and 86400", domain.ErrInvalidRequest)
			}
			return domain.ErrInvalidRequest
		}
	}
	return err
}
