package storage_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

func TestInMemoryRepository_JobLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	job := &domain.Job{
		ID:             "job_100",
		TaskType:       "echo",
		Payload:        []byte(`{"message":"hello"}`),
		State:          domain.StateQueued,
		Priority:       0,
		MaxAttempts:    3,
		TimeoutSeconds: 30,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	fetched, err := repo.GetJob(ctx, "job_100")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if fetched.ID != "job_100" || fetched.State != domain.StateQueued {
		t.Fatalf("unexpected fetched job: %+v", fetched)
	}

	queued, err := repo.ListQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("ListQueuedJobs failed: %v", err)
	}
	if len(queued) != 1 || queued[0].ID != "job_100" {
		t.Fatalf("expected 1 queued job, got %d", len(queued))
	}

	// Update job state
	fetched.State = domain.StateRunning
	fetched.CurrentAttemptID = "att_1"
	if err := repo.UpdateJob(ctx, fetched); err != nil {
		t.Fatalf("UpdateJob failed: %v", err)
	}

	queuedAfterRunning, err := repo.ListQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("ListQueuedJobs failed: %v", err)
	}
	if len(queuedAfterRunning) != 0 {
		t.Fatalf("expected 0 queued jobs after transition to RUNNING, got %d", len(queuedAfterRunning))
	}
}

func TestInMemoryRepository_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewInMemoryRepository()

	var wg sync.WaitGroup
	workersCount := 10
	jobsPerWorker := 20

	for w := 0; w < workersCount; w++ {
		workerID := fmt.Sprintf("worker_%d", w)
		wg.Add(1)
		go func(wID string) {
			defer wg.Done()
			_ = repo.UpsertWorker(ctx, &domain.Worker{
				ID:             wID,
				Status:         domain.WorkerStatusHealthy,
				TotalSlots:     4,
				AvailableSlots: 4,
				SupportedTasks: []string{"echo"},
			})
		}(workerID)
	}

	for w := 0; w < workersCount; w++ {
		wg.Add(1)
		go func(workerIndex int) {
			defer wg.Done()
			for j := 0; j < jobsPerWorker; j++ {
				jobID := fmt.Sprintf("job_%d_%d", workerIndex, j)
				_ = repo.CreateJob(ctx, &domain.Job{
					ID:        jobID,
					TaskType:  "echo",
					State:     domain.StateQueued,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
				_, _ = repo.GetJob(ctx, jobID)
			}
		}(w)
	}

	wg.Wait()

	queued, err := repo.ListQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("ListQueuedJobs failed: %v", err)
	}
	if len(queued) != workersCount*jobsPerWorker {
		t.Fatalf("expected %d queued jobs, got %d", workersCount*jobsPerWorker, len(queued))
	}
}
