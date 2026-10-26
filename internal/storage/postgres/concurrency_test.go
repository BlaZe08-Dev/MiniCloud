package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

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

// TestPostgresRepository_Concurrency_MixedOperationsStress runs 60 concurrent goroutines executing
// an adversarial mix of DispatchJob, CommitAttempt, CancelJob, ReapStaleWorkers, and RecordHeartbeat
// across overlapping jobs and workers. It verifies:
// 1. Deadlock freedom: completes within context deadline.
// 2. State invariants: exactly one active attempt per job, at most one result per job, and available_slots never negative.
func TestPostgresRepository_Concurrency_MixedOperationsStress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	pool, schemaName := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	const (
		numWorkers  = 4
		numJobs     = 8
		concurrency = 60
		rounds      = 10
	)

	// Seed workers
	workerIDs := make([]string, numWorkers)
	for i := 0; i < numWorkers; i++ {
		workerIDs[i] = fmt.Sprintf("worker_stress_%d", i)
		w := &domain.Worker{
			ID:             workerIDs[i],
			Version:        "v1.0.0",
			TotalSlots:     2,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, w); err != nil {
			t.Fatalf("UpsertWorker %d failed: %v", i, err)
		}
	}

	// Seed jobs
	jobIDs := make([]string, numJobs)
	for i := 0; i < numJobs; i++ {
		jobIDs[i] = fmt.Sprintf("job_stress_%d", i)
		j := &domain.Job{
			ID:             jobIDs[i],
			TaskType:       "echo",
			Payload:        []byte(fmt.Sprintf(`{"idx":%d}`, i)),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, j); err != nil {
			t.Fatalf("SubmitJob %d failed: %v", i, err)
		}
	}

	type activeAttemptInfo struct {
		jobID      string
		attemptID  string
		leaseToken string
		workerID   string
	}

	var (
		attemptsMu     sync.Mutex
		activeAttempts []activeAttemptInfo
	)

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			<-startBarrier

			r := rand.New(rand.NewSource(int64(gID*1000 + 42)))

			for iter := 0; iter < rounds; iter++ {
				if ctx.Err() != nil {
					return
				}

				op := (gID + iter) % 6
				targetJobID := jobIDs[r.Intn(numJobs)]
				targetWorkerID := workerIDs[r.Intn(numWorkers)]

				switch op {
				case 0: // DispatchJob
					att, err := repo.DispatchJob(ctx, targetJobID, targetWorkerID)
					if err == nil && att != nil {
						attemptsMu.Lock()
						activeAttempts = append(activeAttempts, activeAttemptInfo{
							jobID:      targetJobID,
							attemptID:  att.ID,
							leaseToken: att.LeaseToken,
							workerID:   targetWorkerID,
						})
						attemptsMu.Unlock()
					}
				case 1, 5: // DispatchNextJob
					j, att, err := repo.DispatchNextJob(ctx, targetWorkerID)
					if err == nil && att != nil && j != nil {
						attemptsMu.Lock()
						activeAttempts = append(activeAttempts, activeAttemptInfo{
							jobID:      j.ID,
							attemptID:  att.ID,
							leaseToken: att.LeaseToken,
							workerID:   targetWorkerID,
						})
						attemptsMu.Unlock()
					}
				case 2: // CommitAttempt
					var candidate activeAttemptInfo
					hasCandidate := false
					attemptsMu.Lock()
					if len(activeAttempts) > 0 {
						candidate = activeAttempts[r.Intn(len(activeAttempts))]
						hasCandidate = true
					}
					attemptsMu.Unlock()

					if hasCandidate {
						outcome := domain.AttemptOutcome{
							ExitCode: 0,
							Output:   "stress ok",
						}
						_, _ = repo.CommitAttempt(ctx, candidate.jobID, candidate.attemptID, candidate.leaseToken, candidate.workerID, outcome)
					}
				case 3: // CancelJob
					_, _ = repo.CancelJob(ctx, targetJobID)
				case 4: // ReapStaleWorkers / RecordHeartbeat
					if iter%2 == 0 {
						_, _ = repo.ReapStaleWorkers(ctx, 1*time.Millisecond)
					} else {
						_ = repo.RecordHeartbeat(ctx, targetWorkerID, 2)
					}
				}
			}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	if ctx.Err() != nil {
		t.Fatalf("concurrency test exceeded deadline, possible deadlock encountered: %v", ctx.Err())
	}

	// Invariant 1: Exactly at most ONE active attempt per job in the entire database
	checkActiveAttempts := fmt.Sprintf(`
		SELECT job_id, COUNT(*) 
		FROM %s.attempts 
		WHERE state IN ('PENDING', 'RUNNING') 
		GROUP BY job_id 
		HAVING COUNT(*) > 1;
	`, schemaName)
	rows, err := pool.Query(ctx, checkActiveAttempts)
	if err != nil {
		t.Fatalf("failed to query active attempts: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var jID string
		var cnt int
		_ = rows.Scan(&jID, &cnt)
		t.Fatalf("INVARIANT VIOLATION: job %s has %d active attempts (>1)", jID, cnt)
	}

	// Invariant 2: At most ONE result per job
	checkResults := fmt.Sprintf(`
		SELECT job_id, COUNT(*) 
		FROM %s.results 
		GROUP BY job_id 
		HAVING COUNT(*) > 1;
	`, schemaName)
	resRows, err := pool.Query(ctx, checkResults)
	if err != nil {
		t.Fatalf("failed to query results: %v", err)
	}
	defer resRows.Close()
	if resRows.Next() {
		var jID string
		var cnt int
		_ = resRows.Scan(&jID, &cnt)
		t.Fatalf("INVARIANT VIOLATION: job %s has %d result rows (>1)", jID, cnt)
	}

	// Invariant 3: Derived available slots never negative
	checkNegativeSlots := fmt.Sprintf(`
		SELECT id, available_slots 
		FROM %s.worker_capacity 
		WHERE available_slots < 0;
	`, schemaName)
	slotRows, err := pool.Query(ctx, checkNegativeSlots)
	if err != nil {
		t.Fatalf("failed to query worker capacity: %v", err)
	}
	defer slotRows.Close()
	if slotRows.Next() {
		var wID string
		var slots int
		_ = slotRows.Scan(&wID, &slots)
		t.Fatalf("INVARIANT VIOLATION: worker %s has negative available_slots: %d", wID, slots)
	}
}

// TestPostgresRepository_Concurrency_DispatchNextJob_StartBarrier tests 10 concurrent workers
// racing across a start barrier to poll and dispatch 40 queued jobs via DispatchNextJob.
// It verifies:
// 1. Exactly-once dispatch: every one of the 40 jobs is dispatched exactly once.
// 2. Zero duplicate attempts: no job ever gets more than 1 attempt.
// 3. Zero jobs lost: every job reaches RUNNING state and all 40 jobs are accounted for.
func TestPostgresRepository_Concurrency_DispatchNextJob_StartBarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, schemaName := setupTestPool(t)
	repo := postgres.NewRepository(pool)

	const (
		numWorkers = 10
		numJobs    = 40
	)

	// Seed 10 workers with ample slots (10 slots each)
	for i := 0; i < numWorkers; i++ {
		wID := fmt.Sprintf("worker_dnj_barrier_%d", i)
		worker := &domain.Worker{
			ID:             wID,
			Version:        "v1.0.0",
			TotalSlots:     numJobs,
			Status:         domain.WorkerStatusHealthy,
			SupportedTasks: []string{"echo"},
		}
		if err := repo.UpsertWorker(ctx, worker); err != nil {
			t.Fatalf("UpsertWorker %s failed: %v", wID, err)
		}
	}

	// Seed 40 queued jobs
	for i := 0; i < numJobs; i++ {
		jobID := fmt.Sprintf("job_dnj_barrier_%02d", i)
		job := &domain.Job{
			ID:             jobID,
			TaskType:       "echo",
			Payload:        []byte(fmt.Sprintf(`{"barrier_idx":%d}`, i)),
			State:          domain.StateQueued,
			MaxAttempts:    3,
			TimeoutSeconds: 30,
		}
		if err := repo.SubmitJob(ctx, job); err != nil {
			t.Fatalf("SubmitJob %s failed: %v", jobID, err)
		}
	}

	var (
		mu              sync.Mutex
		dispatchedJobs  = make(map[string]int) // jobID -> count
		totalDispatched int
	)

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		wID := fmt.Sprintf("worker_dnj_barrier_%d", i)
		go func(workerID string) {
			defer wg.Done()
			<-startBarrier

			for {
				job, att, err := repo.DispatchNextJob(ctx, workerID)
				if err != nil {
					if errors.Is(err, domain.ErrNoQueuedJobs) || errors.Is(err, domain.ErrWorkerNoCapacity) {
						// Queue drained or worker saturated
						return
					}
					t.Errorf("worker %s unexpected dispatch error: %v", workerID, err)
					return
				}
				if job == nil || att == nil {
					t.Errorf("worker %s returned nil job or attempt without error", workerID)
					return
				}

				mu.Lock()
				dispatchedJobs[job.ID]++
				totalDispatched++
				mu.Unlock()
			}
		}(wID)
	}

	close(startBarrier)
	wg.Wait()

	if totalDispatched != numJobs {
		t.Fatalf("expected exactly %d jobs dispatched, got %d", numJobs, totalDispatched)
	}

	// Verify every job was dispatched exactly once
	for i := 0; i < numJobs; i++ {
		jobID := fmt.Sprintf("job_dnj_barrier_%02d", i)
		count := dispatchedJobs[jobID]
		if count != 1 {
			t.Fatalf("job %s dispatched %d times (expected 1)", jobID, count)
		}
	}

	// Verify database authoritative state:
	// 1. All 40 jobs in RUNNING state
	var runningJobCount int
	queryJobs := fmt.Sprintf("SELECT COUNT(*) FROM %s.jobs WHERE state = 'RUNNING';", schemaName)
	if err := pool.QueryRow(ctx, queryJobs).Scan(&runningJobCount); err != nil {
		t.Fatalf("failed to query running jobs: %v", err)
	}
	if runningJobCount != numJobs {
		t.Fatalf("expected %d running jobs in database, got %d", numJobs, runningJobCount)
	}

	// 2. Exactly 40 attempts in database
	var attemptCount int
	queryAttempts := fmt.Sprintf("SELECT COUNT(*) FROM %s.attempts;", schemaName)
	if err := pool.QueryRow(ctx, queryAttempts).Scan(&attemptCount); err != nil {
		t.Fatalf("failed to query attempts: %v", err)
	}
	if attemptCount != numJobs {
		t.Fatalf("expected %d attempts in database, got %d", numJobs, attemptCount)
	}

	// 3. No duplicate attempts per job
	queryDupAttempts := fmt.Sprintf(`
		SELECT job_id, COUNT(*) 
		FROM %s.attempts 
		GROUP BY job_id 
		HAVING COUNT(*) > 1;
	`, schemaName)
	dupRows, err := pool.Query(ctx, queryDupAttempts)
	if err != nil {
		t.Fatalf("failed to query duplicate attempts: %v", err)
	}
	defer dupRows.Close()
	if dupRows.Next() {
		var jID string
		var count int
		_ = dupRows.Scan(&jID, &count)
		t.Fatalf("INVARIANT VIOLATION: job %s has %d attempts (>1)", jID, count)
	}
}
