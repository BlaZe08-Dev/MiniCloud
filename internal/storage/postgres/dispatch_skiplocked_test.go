package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// TestPostgresRepository_DispatchNextJob_SkipsLockedHead guards the FOR UPDATE SKIP LOCKED of
// DispatchNextJob. A second connection holds the row lock of the head queued job (a concurrent
// dispatch, cancel or commit in flight). Dispatch for another worker must return the NEXT queued
// job at once instead of waiting for the lock or reporting that no job is queued. Without SKIP
// LOCKED the call blocks until its context ends and this test fails on the 2 s bound.
func TestPostgresRepository_DispatchNextJob_SkipsLockedHead(t *testing.T) {
	ctx := context.Background()
	pool, _ := setupTestPool(t)
	repo := postgres.NewRepository(pool)
	if err := repo.UpsertWorker(ctx, &domain.Worker{ID: "w_sl", Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"j_sl1", "j_sl2"} {
		if err := repo.SubmitJob(ctx, &domain.Job{ID: id, TaskType: "echo", Payload: []byte(`{}`), State: domain.StateQueued, MaxAttempts: 3, TimeoutSeconds: 30}); err != nil {
			t.Fatal(err)
		}
	}

	lock := lockRow(t, pool, "jobs", "j_sl1") // the head of the queue, locked by another transaction
	defer func() { _ = lock.Rollback(context.Background()) }()

	type result struct {
		job *domain.Job
		err error
		dur time.Duration
	}
	done := make(chan result, 1)
	go func() {
		dctx, cancel := context.WithTimeout(ctx, 6*time.Second) // the wait a missing SKIP LOCKED would hit
		defer cancel()
		start := time.Now()
		job, _, err := repo.DispatchNextJob(dctx, "w_sl")
		done <- result{job, err, time.Since(start)}
	}()

	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("DispatchNextJob did not return")
	}
	if res.dur > 2*time.Second {
		t.Fatalf("DispatchNextJob took %v with the head job locked; SKIP LOCKED must not wait", res.dur)
	}
	if res.err != nil || res.job == nil {
		t.Fatalf("DispatchNextJob = %v, %v after %v; want the next queued job", res.job, res.err, res.dur)
	}
	if res.job.ID != "j_sl2" {
		t.Fatalf("dispatched %s, want j_sl2 (j_sl1 is locked)", res.job.ID)
	}

	// The locked job was left untouched: still QUEUED once the lock is released.
	_ = lock.Rollback(ctx)
	if j, err := repo.GetJob(ctx, "j_sl1"); err != nil || j.State != domain.StateQueued {
		t.Fatalf("locked head job = %v, %v; want QUEUED", j, err)
	}
}
