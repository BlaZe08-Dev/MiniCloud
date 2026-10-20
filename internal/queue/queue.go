package queue

import (
	"context"
	"errors"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

var (
	// ErrNoWork indicates that no queued items match the worker's criteria or the queue is empty.
	ErrNoWork = errors.New("no eligible work available in queue")
)

// Item represents a lightweight dispatch delivery signal.
//
// CRITICAL ARCHITECTURAL INVARIANT:
// The Item carries ONLY JobID and TaskType (as a scheduling/routing hint).
// It intentionally does NOT carry Payload or TimeoutSeconds. The repository
// remains the single authoritative source of truth for job data; the queue holds
// delivery signals only (ADR-0004, ADR-0006, ADR-0008).
type Item struct {
	JobID    string `json:"job_id"`
	TaskType string `json:"task_type"`
}

// Queue defines the dispatch-transport abstraction for MiniCloud.
//
// Delivery Semantics:
//   - At-Least-Once Delivery: Claim returns a delivery signal that remains in-flight
//     until explicitly Acked (on accepted result commit) or Released (on dispatch failure).
//   - Unacknowledged deliveries may be redelivered.
//   - Redelivery of an already-dispatched job must not create duplicate attempts;
//     the coordinator re-validates the repository state at claim time (job.State == StateQueued)
//     to enforce deduplication.
type Queue interface {
	// Enqueue adds a job delivery signal to the dispatch transport.
	Enqueue(ctx context.Context, item *Item) error

	// Claim retrieves and claims the next eligible item for the specified worker.
	// Returns ErrNoWork if no eligible item is available.
	// Claimed items remain in-flight until Acked or Released.
	Claim(ctx context.Context, worker *domain.Worker) (*Item, error)

	// Release returns an in-flight claimed item back to the queue (e.g. if attempt
	// creation or repository update fails after Claim).
	Release(ctx context.Context, jobID string) error

	// Ack permanently acknowledges and settles a completed delivery after an accepted
	// result commit.
	Ack(ctx context.Context, jobID string) error

	// Len returns the number of pending (unclaimed) items in the queue.
	Len() int
}
