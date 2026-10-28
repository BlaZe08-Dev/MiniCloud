package domain

// JobState represents the state of a job in the MiniCloud lifecycle.
type JobState string

const (
	StateSubmitted       JobState = "SUBMITTED"
	StateQueued          JobState = "QUEUED"
	StateRunning         JobState = "RUNNING"
	StateCancelRequested JobState = "CANCEL_REQUESTED"
	StateRetrying        JobState = "RETRYING"
	StateSucceeded       JobState = "SUCCEEDED"
	StateFailed          JobState = "FAILED"
	StateCancelled       JobState = "CANCELLED"
	StateRejected        JobState = "REJECTED"
)

// AttemptState represents the execution lifecycle of a single attempt. The values match
// the chk_attempts_state constraint in migration 000001_initial_schema.sql.
type AttemptState string

const (
	AttemptPending   AttemptState = "PENDING"
	AttemptRunning   AttemptState = "RUNNING"
	AttemptCompleted AttemptState = "COMPLETED"
	AttemptFailed    AttemptState = "FAILED"
	AttemptTimedOut  AttemptState = "TIMED_OUT"
	AttemptCancelled AttemptState = "CANCELLED"
	AttemptLost      AttemptState = "LOST"
)

// WorkerStatus represents the health and readiness of a worker.
type WorkerStatus string

const (
	WorkerStatusHealthy   WorkerStatus = "HEALTHY"
	WorkerStatusUnhealthy WorkerStatus = "UNHEALTHY"
	WorkerStatusDraining  WorkerStatus = "DRAINING"
)

// AllJobStates returns all valid job states defined in MiniCloud.
func AllJobStates() []JobState {
	return []JobState{
		StateSubmitted,
		StateQueued,
		StateRunning,
		StateCancelRequested,
		StateRetrying,
		StateSucceeded,
		StateFailed,
		StateCancelled,
		StateRejected,
	}
}

// IsTerminal returns true if the job state cannot transition to any other state.
func (s JobState) IsTerminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled, StateRejected:
		return true
	default:
		return false
	}
}
