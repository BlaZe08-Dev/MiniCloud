package storage

import (
	"context"
	"time"
)

// AssignmentPassStaleness answers whether the pass heartbeat is stale, on the repository's own
// clock (ADR-0017 implementation note 8). The poll-time refresh uses it for the 2 x
// MINICLOUD_ASSIGNMENT_INTERVAL threshold of Section 3.2, so the coordinator never compares its
// process clock with a time read from the repository. In PostgreSQL the comparison runs in SQL on
// the database clock; in memory it uses the injected repository clock that also writes the
// heartbeat.
//
// It is a separate interface so the AssignmentRepository surface of slice 5a stays unchanged.
type AssignmentPassStaleness interface {
	// AssignmentPassStale reports whether the last completed pass finished more than threshold
	// ago (last_completed_at < now - threshold). A repository with no completed pass is stale.
	AssignmentPassStale(ctx context.Context, threshold time.Duration) (bool, error)
}

// AssignmentPassStale implements AssignmentPassStaleness on the repository clock.
func (r *InMemoryRepository) AssignmentPassStale(ctx context.Context, threshold time.Duration) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.passCompletedAt.Before(r.clock.Now().Add(-threshold)), nil
}

var _ AssignmentPassStaleness = (*InMemoryRepository)(nil)
