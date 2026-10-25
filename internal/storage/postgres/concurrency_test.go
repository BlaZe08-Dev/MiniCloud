package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// TestPostgresRepository_Concurrency_SameJobDispatchRaced verifies that when 50 goroutines
// concurrently attempt to dispatch the same queued job onto a worker with ample capacity (50 slots),
// row-level locking ensures that exactly one attempt is created and every failure is from the expected error set.
func TestPostgresRepository_Concurrency_SameJobDispatchRaced(t *testing.T) {
	ctx := context.Background()
	pool, schemaName := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	const concurrency = 50

	worker := &domain.Worker{
		ID:             "worker_concur_a",
		Version:        "v1.0.0",
		TotalSlots:     concurrency,
		Status:         domain.WorkerStatusHealthy,
		SupportedTasks: []string{"echo"},
	}
	if err := repo.UpsertWorker(ctx, worker); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}

	job := &domain.Job{
		ID:             "job_concur_a",
		TaskType:       "echo",
		Payload:        []byte("concurrent dispatch test"),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	}
	if err := repo.SubmitJob(ctx, job); err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	type result struct {
		attempt *domain.Attempt
		err     error
	}
	results := make([]result, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier
			att, err := repo.DispatchJob(ctx, "job_concur_a", "worker_concur_a")
			results[idx] = result{attempt: att, err: err}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	var successCount, failureCount int
	for _, res := range results {
		if res.err == nil && res.attempt != nil {
			successCount++
		} else {
			failureCount++
			// Every failure must be in the expected error set
			isExpected := errors.Is(res.err, domain.ErrJobNotQueued) ||
				errors.Is(res.err, domain.ErrInvalidRequest)
			if !isExpected {
				t.Fatalf("unexpected error during concurrent dispatch: %v", res.err)
			}
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful dispatch, got %d", successCount)
	}
	if failureCount != concurrency-1 {
		t.Fatalf("expected %d failed dispatches, got %d", concurrency-1, failureCount)
	}

	// Verify authoritative database state: exactly one attempt row exists for this job
	var attemptCount int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s.attempts WHERE job_id = 'job_concur_a';", schemaName)
	if err := pool.QueryRow(ctx, query).Scan(&attemptCount); err != nil {
		t.Fatalf("failed to query attempts: %v", err)
	}
	if attemptCount != 1 {
		t.Fatalf("expected exactly 1 attempt in database, got %d", attemptCount)
	}
}

// TestPostgresRepository_Concurrency_WorkerCapacityRaced verifies that when 50 concurrent jobs
// race to be dispatched onto a worker with exactly ONE free slot, exactly one dispatch succeeds,
// 49 fail with ErrWorkerNoCapacity, and the derived available capacity never goes negative.
func TestPostgresRepository_Concurrency_WorkerCapacityRaced(t *testing.T) {
	ctx := context.Background()
	pool, schemaName := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	const concurrency = 50

	worker := &domain.Worker{
		ID:             "worker_concur_b",
		Version:        "v1.0.0",
		TotalSlots:     1,
		Status:         domain.WorkerStatusHealthy,
		SupportedTasks: []string{"echo"},
	}
	if err := repo.UpsertWorker(ctx, worker); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}

	// Submit 50 jobs
	for i := 0; i < concurrency; i++ {
		jobID := fmt.Sprintf("job_concur_b_%d", i)
		job := &domain.Job{
			ID:             jobID,
			TaskType:       "echo",
			Payload:        []byte("capacity race"),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, job); err != nil {
			t.Fatalf("SubmitJob %s failed: %v", jobID, err)
		}
	}

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	type result struct {
		jobID   string
		attempt *domain.Attempt
		err     error
	}
	results := make([]result, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			jobID := fmt.Sprintf("job_concur_b_%d", idx)
			<-startBarrier
			att, err := repo.DispatchJob(ctx, jobID, "worker_concur_b")
			results[idx] = result{jobID: jobID, attempt: att, err: err}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	var successCount, capacityExhaustedCount int
	for _, res := range results {
		if res.err == nil && res.attempt != nil {
			successCount++
		} else {
			if errors.Is(res.err, domain.ErrWorkerNoCapacity) {
				capacityExhaustedCount++
			} else {
				t.Fatalf("unexpected error for %s: %v", res.jobID, res.err)
			}
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 job dispatched, got %d", successCount)
	}
	if capacityExhaustedCount != concurrency-1 {
		t.Fatalf("expected %d jobs rejected with ErrWorkerNoCapacity, got %d", concurrency-1, capacityExhaustedCount)
	}

	// Verify authoritative available slots: must be 0, never negative
	var availSlots int
	query := fmt.Sprintf("SELECT available_slots FROM %s.worker_capacity WHERE id = 'worker_concur_b';", schemaName)
	if err := pool.QueryRow(ctx, query).Scan(&availSlots); err != nil {
		t.Fatalf("failed to query worker_capacity: %v", err)
	}
	if availSlots != 0 {
		t.Fatalf("expected available_slots to be exactly 0, got %d", availSlots)
	}

	// Verify active attempt count in database
	var attemptCount int
	attQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.attempts WHERE worker_id = 'worker_concur_b' AND state = 'RUNNING';", schemaName)
	if err := pool.QueryRow(ctx, attQuery).Scan(&attemptCount); err != nil {
		t.Fatalf("failed to query running attempts: %v", err)
	}
	if attemptCount != 1 {
		t.Fatalf("expected exactly 1 running attempt, got %d", attemptCount)
	}
}

// TestPostgresRepository_Concurrency_DuplicateCommitRaced verifies that when 50 goroutines
// simultaneously race to commit the exact same attempt, exactly one commit succeeds, exactly one
// result row is inserted, and 49 commits are rejected with terminal/stale attempt errors.
func TestPostgresRepository_Concurrency_DuplicateCommitRaced(t *testing.T) {
	ctx := context.Background()
	pool, schemaName := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	const concurrency = 50

	worker := &domain.Worker{
		ID:             "worker_concur_c",
		Version:        "v1.0.0",
		TotalSlots:     2,
		Status:         domain.WorkerStatusHealthy,
		SupportedTasks: []string{"echo"},
	}
	if err := repo.UpsertWorker(ctx, worker); err != nil {
		t.Fatalf("UpsertWorker failed: %v", err)
	}

	job := &domain.Job{
		ID:             "job_concur_c",
		TaskType:       "echo",
		Payload:        []byte("commit race"),
		State:          domain.StateQueued,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
	}
	if err := repo.SubmitJob(ctx, job); err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}

	att, err := repo.DispatchJob(ctx, "job_concur_c", "worker_concur_c")
	if err != nil {
		t.Fatalf("DispatchJob failed: %v", err)
	}

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	type commitResult struct {
		job *domain.Job
		err error
	}
	results := make([]commitResult, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier
			committedJob, err := repo.CommitAttempt(ctx, "job_concur_c", att.ID, att.LeaseToken, "worker_concur_c", domain.AttemptOutcome{
				ExitCode: 0,
				Output:   fmt.Sprintf("output from worker %d", idx),
			})
			results[idx] = commitResult{job: committedJob, err: err}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	var successCount, rejectionCount int
	for _, res := range results {
		if res.err == nil && res.job != nil {
			successCount++
		} else {
			rejectionCount++
			isExpected := errors.Is(res.err, domain.ErrJobAlreadyTerminal) ||
				errors.Is(res.err, domain.ErrStaleAttempt)
			if !isExpected {
				t.Fatalf("unexpected error during duplicate commit: %v", res.err)
			}
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 commit to succeed, got %d", successCount)
	}
	if rejectionCount != concurrency-1 {
		t.Fatalf("expected %d commits rejected, got %d", concurrency-1, rejectionCount)
	}

	// Verify authoritative database state: exactly one result row exists
	var resultCount int
	resQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.results WHERE job_id = 'job_concur_c';", schemaName)
	if err := pool.QueryRow(ctx, resQuery).Scan(&resultCount); err != nil {
		t.Fatalf("failed to query results: %v", err)
	}
	if resultCount != 1 {
		t.Fatalf("expected exactly 1 result row in database, got %d", resultCount)
	}

	// Verify job state is SUCCEEDED
	var finalState string
	jobQuery := fmt.Sprintf("SELECT state FROM %s.jobs WHERE id = 'job_concur_c';", schemaName)
	if err := pool.QueryRow(ctx, jobQuery).Scan(&finalState); err != nil {
		t.Fatalf("failed to query job state: %v", err)
	}
	if finalState != string(domain.StateSucceeded) {
		t.Fatalf("expected job state SUCCEEDED, got %s", finalState)
	}
}
