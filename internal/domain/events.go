package domain

import "time"

// JobEventType defines the standard types of lifecycle events.
type JobEventType string

const (
	JobEventSubmitted       JobEventType = "JOB_SUBMITTED"
	JobEventQueued          JobEventType = "JOB_QUEUED"
	JobEventDispatched      JobEventType = "JOB_DISPATCHED"
	JobEventCompleted       JobEventType = "JOB_COMPLETED"
	JobEventFailed          JobEventType = "JOB_FAILED"
	JobEventCancelled       JobEventType = "JOB_CANCELLED"
	JobEventCancelRequested JobEventType = "JOB_CANCEL_REQUESTED"

	// JobEventDiagnosticLateResult records a result rejected because its job was already
	// terminal (state-machine.md event catalogue).
	JobEventDiagnosticLateResult JobEventType = "DIAGNOSTIC_LATE_RESULT"
)

// JobEvent represents an immutable, append-only lifecycle event for audit and observability.
type JobEvent struct {
	ID        int64          `json:"id"`
	JobID     string         `json:"job_id"`
	AttemptID *string        `json:"attempt_id,omitempty"`
	EventType JobEventType   `json:"event_type"`
	FromState *JobState      `json:"from_state,omitempty"`
	ToState   *JobState      `json:"to_state,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}
