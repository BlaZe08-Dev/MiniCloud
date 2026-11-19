package domain

import (
	"time"
)

// Worker represents a registered worker process executing on a host.
type Worker struct {
	ID              string       `json:"id"`
	Version         string       `json:"version"`
	Status          WorkerStatus `json:"status"`
	TotalSlots      int          `json:"total_slots"`
	AvailableSlots  int          `json:"available_slots"`
	SupportedTasks  []string     `json:"supported_tasks"`
	LastHeartbeatAt time.Time    `json:"last_heartbeat_at"`
	RegisteredAt    time.Time    `json:"registered_at"`

	// ExecutesConcurrently is the optional register field executes_concurrently (ADR-0020
	// Section 2.6): true when the worker executes up to TotalSlots attempts at the same time.
	// Absent (false) means the round-robin pass treats the worker as having one slot, which keeps
	// the v1.0.0 and V2-0 workers, which do not send it, at their one-slot behaviour.
	ExecutesConcurrently bool `json:"executes_concurrently"`

	// LastPollAt is the time of the last poll that reached this worker while it was HEALTHY
	// (ADR-0017 Section 2.2). nil means the worker has never polled, so it is not assignable.
	// It is internal: it is not part of any response.
	LastPollAt *time.Time `json:"-"`
}

// SupportsTask returns true if the worker advertises support for the task type.
func (w *Worker) SupportsTask(taskType string) bool {
	for _, t := range w.SupportedTasks {
		if t == taskType {
			return true
		}
	}
	return false
}

// HasCapacity returns true if the worker has at least one available execution slot.
func (w *Worker) HasCapacity() bool {
	return w.AvailableSlots > 0 && w.Status == WorkerStatusHealthy
}
