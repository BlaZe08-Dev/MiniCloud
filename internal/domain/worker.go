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
