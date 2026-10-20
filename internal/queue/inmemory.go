package queue

import (
	"context"
	"fmt"
	"sync"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// InMemoryQueue implements Queue using concurrent-safe in-memory collections.
type InMemoryQueue struct {
	mu       sync.Mutex
	items    []*Item
	inFlight map[string]*Item
}

// NewInMemoryQueue constructs a new InMemoryQueue.
func NewInMemoryQueue() *InMemoryQueue {
	return &InMemoryQueue{
		items:    make([]*Item, 0),
		inFlight: make(map[string]*Item),
	}
}

// Enqueue adds a job delivery signal to the queue.
func (q *InMemoryQueue) Enqueue(ctx context.Context, item *Item) error {
	if item == nil || item.JobID == "" {
		return fmt.Errorf("%w: invalid queue item", domain.ErrInvalidRequest)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// Clone to maintain boundary isolation
	itemCopy := *item
	q.items = append(q.items, &itemCopy)
	return nil
}

// Claim finds the first pending item supported by the worker, marks it in-flight,
// and returns it. Returns ErrNoWork if no supported item is available.
func (q *InMemoryQueue) Claim(ctx context.Context, worker *domain.Worker) (*Item, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, it := range q.items {
		if worker.SupportsTask(it.TaskType) {
			// Remove from pending items
			q.items = append(q.items[:i], q.items[i+1:]...)

			// Track in inFlight
			q.inFlight[it.JobID] = it

			itemCopy := *it
			return &itemCopy, nil
		}
	}

	return nil, ErrNoWork
}

// Release returns an in-flight item back to the head of pending items for redelivery.
func (q *InMemoryQueue) Release(ctx context.Context, jobID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if item, exists := q.inFlight[jobID]; exists {
		delete(q.inFlight, jobID)
		// Prepend to pending items to prioritize redelivery
		q.items = append([]*Item{item}, q.items...)
	}
	return nil
}

// Ack permanently removes a settled item from in-flight tracking.
func (q *InMemoryQueue) Ack(ctx context.Context, jobID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.inFlight, jobID)
	// Also clean from pending if it was somehow present
	for i, it := range q.items {
		if it.JobID == jobID {
			q.items = append(q.items[:i], q.items[i+1:]...)
			break
		}
	}
	return nil
}

// Len returns the count of pending items.
func (q *InMemoryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// InFlightCount returns the count of currently claimed in-flight items.
func (q *InMemoryQueue) InFlightCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.inFlight)
}
