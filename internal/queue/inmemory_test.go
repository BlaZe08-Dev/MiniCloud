package queue_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/queue"
)

func TestInMemoryQueue_FIFO(t *testing.T) {
	ctx := context.Background()
	q := queue.NewInMemoryQueue()

	w := &domain.Worker{
		ID:             "worker-1",
		SupportedTasks: []string{"echo"},
	}

	_ = q.Enqueue(ctx, &queue.Item{JobID: "job-1", TaskType: "echo"})
	_ = q.Enqueue(ctx, &queue.Item{JobID: "job-2", TaskType: "echo"})
	_ = q.Enqueue(ctx, &queue.Item{JobID: "job-3", TaskType: "echo"})

	if q.Len() != 3 {
		t.Fatalf("expected 3 items, got %d", q.Len())
	}

	for _, expectedID := range []string{"job-1", "job-2", "job-3"} {
		item, err := q.Claim(ctx, w)
		if err != nil {
			t.Fatalf("Claim failed: %v", err)
		}
		if item.JobID != expectedID {
			t.Fatalf("expected JobID %s, got %s", expectedID, item.JobID)
		}
	}

	if q.Len() != 0 {
		t.Fatalf("expected 0 pending items, got %d", q.Len())
	}
	if q.InFlightCount() != 3 {
		t.Fatalf("expected 3 in-flight items, got %d", q.InFlightCount())
	}
}

func TestInMemoryQueue_TaskFiltering(t *testing.T) {
	ctx := context.Background()
	q := queue.NewInMemoryQueue()

	wSleep := &domain.Worker{
		ID:             "worker-sleep",
		SupportedTasks: []string{"sleep"},
	}
	wEcho := &domain.Worker{
		ID:             "worker-echo",
		SupportedTasks: []string{"echo"},
	}

	_ = q.Enqueue(ctx, &queue.Item{JobID: "job-echo", TaskType: "echo"})
	_ = q.Enqueue(ctx, &queue.Item{JobID: "job-sleep", TaskType: "sleep"})

	// Worker supporting only sleep should skip echo and claim job-sleep
	item, err := q.Claim(ctx, wSleep)
	if err != nil {
		t.Fatalf("Claim failed for wSleep: %v", err)
	}
	if item.JobID != "job-sleep" {
		t.Fatalf("expected job-sleep, got %s", item.JobID)
	}

	// Worker supporting echo claims job-echo
	itemEcho, err := q.Claim(ctx, wEcho)
	if err != nil {
		t.Fatalf("Claim failed for wEcho: %v", err)
	}
	if itemEcho.JobID != "job-echo" {
		t.Fatalf("expected job-echo, got %s", itemEcho.JobID)
	}

	// Empty queue returns ErrNoWork
	_, err = q.Claim(ctx, wEcho)
	if !errors.Is(err, queue.ErrNoWork) {
		t.Fatalf("expected ErrNoWork, got %v", err)
	}
}

func TestInMemoryQueue_ReleaseAndAck(t *testing.T) {
	ctx := context.Background()
	q := queue.NewInMemoryQueue()

	w := &domain.Worker{
		ID:             "worker-1",
		SupportedTasks: []string{"echo"},
	}

	_ = q.Enqueue(ctx, &queue.Item{JobID: "job-1", TaskType: "echo"})

	// Claim
	item, err := q.Claim(ctx, w)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if q.InFlightCount() != 1 {
		t.Fatalf("expected 1 in-flight, got %d", q.InFlightCount())
	}

	// Release (dispatch failed) returns item to queue
	if err := q.Release(ctx, item.JobID); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if q.InFlightCount() != 0 {
		t.Fatalf("expected 0 in-flight after release, got %d", q.InFlightCount())
	}
	if q.Len() != 1 {
		t.Fatalf("expected 1 pending item after release, got %d", q.Len())
	}

	// Claim again (redelivery)
	redelivered, err := q.Claim(ctx, w)
	if err != nil {
		t.Fatalf("Claim failed on redelivery: %v", err)
	}
	if redelivered.JobID != "job-1" {
		t.Fatalf("expected job-1 on redelivery, got %s", redelivered.JobID)
	}

	// Ack settles item
	if err := q.Ack(ctx, redelivered.JobID); err != nil {
		t.Fatalf("Ack failed: %v", err)
	}
	if q.InFlightCount() != 0 {
		t.Fatalf("expected 0 in-flight after ack, got %d", q.InFlightCount())
	}
	if q.Len() != 0 {
		t.Fatalf("expected 0 pending items after ack, got %d", q.Len())
	}
}

func TestInMemoryQueue_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	q := queue.NewInMemoryQueue()

	w := &domain.Worker{
		ID:             "worker-concurrent",
		SupportedTasks: []string{"echo"},
	}

	var wg sync.WaitGroup
	numProducers := 10
	itemsPerProducer := 20

	// Concurrent Enqueuers
	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				_ = q.Enqueue(ctx, &queue.Item{
					JobID:    fmt.Sprintf("job-%d-%d", producerID, i),
					TaskType: "echo",
				})
			}
		}(p)
	}

	// Concurrent Claimers
	claimedCount := 0
	var claimMu sync.Mutex
	numConsumers := 4
	for c := 0; c < numConsumers; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				item, err := q.Claim(ctx, w)
				if err == nil {
					claimMu.Lock()
					claimedCount++
					claimMu.Unlock()
					_ = q.Ack(ctx, item.JobID)
				}
				claimMu.Lock()
				done := claimedCount >= numProducers*itemsPerProducer
				claimMu.Unlock()
				if done {
					return
				}
			}
		}()
	}

	wg.Wait()

	if claimedCount != numProducers*itemsPerProducer {
		t.Fatalf("expected %d total claimed items, got %d", numProducers*itemsPerProducer, claimedCount)
	}
	if q.Len() != 0 {
		t.Fatalf("expected 0 remaining items, got %d", q.Len())
	}
}
